package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/live"
	"github.com/linuxmatters/jive-vocals/internal/osc"
	"github.com/linuxmatters/jive-vocals/internal/room"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

const (
	daemonHeartbeat = 2 * time.Second
	paArmWindow     = 10 * time.Second
	resultUISlots   = 6
)

type daemonConfig struct {
	bridge         string
	sessionsRoot   string
	profilesDir    string
	playSink       string
	captureSeconds float64
	pink           room.TestSignalSpec
	jackClient     string
	sources        []string
}

// daemon runs jive-room headless for the ROOM tab. Operator commands arrive
// from Open Stage Control through the bridge as /dsp/cmd/...; results go back
// as /dsp/ui/... display strings. It never writes to the desk: a profile
// restore only submits a pending proposal that the operator must APPLY.
type daemon struct {
	cfg daemonConfig
	bc  *studiolive.BridgeClient
	ctx context.Context
	out io.Writer

	mu            sync.Mutex
	channel       int
	sessionDir    string
	monitorCancel context.CancelFunc
	monitorDone   chan struct{}
	task          string
	taskCancel    context.CancelFunc
	paArmedUntil  time.Time
	lastProfile   string
	auto          *autoCutter
}

func runDaemon(cfg daemonConfig) error {
	if err := checkCaptureSeconds(cfg.captureSeconds); err != nil {
		return usageError{err}
	}
	cfg.pink.HeadroomDB = minPlaybackHeadroomDB
	for _, d := range []string{cfg.sessionsRoot, cfg.profilesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	bc, err := studiolive.DialBridge(cfg.bridge)
	if err != nil {
		return err
	}
	defer bc.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d := &daemon{cfg: cfg, bc: bc, ctx: ctx, out: os.Stdout, channel: 1, auto: &autoCutter{}}
	d.newSession()
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = bc.Run(ctx, studiolive.Events{OnCommand: d.command, OnResult: d.result})
	}()
	fmt.Fprintf(d.out, "jive-room daemon: bridge %s, sessions %s, profiles %s, PA sink %q\n", cfg.bridge, cfg.sessionsRoot, cfg.profilesDir, cfg.playSink)
	tick := time.NewTicker(daemonHeartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			d.stopMonitor()
			d.cancelTask()
			d.status("stopped")
			<-runDone
			return nil
		case <-tick.C:
			d.heartbeat()
		}
	}
}

// result tracks the bridge's automatic-cut state and reports automatic applies.
func (d *daemon) result(r studiolive.ApplyResult) {
	if r.Action != "auto" {
		return
	}
	if r.ID == "" {
		was := d.auto.isEnabled()
		d.auto.setEnabled(r.AutoEnabled())
		if was != r.AutoEnabled() {
			d.say("automatic cuts %s", map[bool]string{true: "ENABLED by the operator", false: "off"}[r.AutoEnabled()])
		}
		return
	}
	msg := fmt.Sprintf("automatic cut %s: %s %s", r.ID, r.Status, r.Reason)
	fmt.Fprintf(d.out, "%s %s\n", time.Now().Format("15:04:05"), msg)
	_ = d.bc.PublishUI("/dsp/ui/auto/last", time.Now().Format("15:04:05")+" "+msg)
}

func (d *daemon) say(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(d.out, "%s %s\n", time.Now().Format("15:04:05"), msg)
	_ = d.bc.PublishUI("/dsp/ui/room/stage", msg)
}

func (d *daemon) status(state string) {
	_ = d.bc.PublishUI("/dsp/ui/room/daemon", fmt.Sprintf("jive-room %s · %s", state, time.Now().Format("15:04:05")))
}

func (d *daemon) heartbeat() {
	d.mu.Lock()
	mon := "monitor off"
	if d.monitorCancel != nil {
		mon = "monitor on"
	}
	task := "idle"
	if d.task != "" {
		task = d.task
	}
	sink := "PA measurement disabled (no sink configured)"
	if d.cfg.playSink != "" {
		sink = "PA sink " + d.cfg.playSink
	}
	line := fmt.Sprintf("CH%d · %s · %s · session %s · %s", d.channel, mon, task, filepath.Base(d.sessionDir), sink)
	d.mu.Unlock()
	d.status(line)
}

func pressed(m osc.Message) bool {
	v, ok := m.Float(0)
	return ok && v >= 0.5
}

// command dispatches one operator command. It must not block the bridge reader.
func (d *daemon) command(m osc.Message) {
	name := strings.TrimPrefix(m.Address, studiolive.CommandPrefix)
	if name == "channel" {
		v, ok := m.Float(0)
		if !ok || v < 1 || v > studiolive.InputChannels || v != float64(int(v)) {
			return
		}
		d.setChannel(int(v))
		return
	}
	if !pressed(m) {
		return
	}
	switch {
	case name == "monitor/start":
		d.startMonitor()
	case name == "monitor/stop":
		d.stopMonitor()
		d.say("monitor stopped")
	case strings.HasPrefix(name, "capture/"):
		stage := strings.TrimPrefix(name, "capture/")
		d.runTask("capturing "+stage, func(ctx context.Context, opts live.InputOptions, ch int, dir string) error {
			return runStageCapture(ctx, stage, opts, ch, d.cfg.captureSeconds, dir, d.publisher())
		})
	case name == "pa/arm":
		d.armPA()
	case name == "pa/confirm":
		d.confirmPA()
	case name == "stop":
		d.cancelTask()
	case name == "session/new":
		d.newSession()
	case name == "analyse":
		d.runTask("analysing", func(ctx context.Context, _ live.InputOptions, _ int, dir string) error { return d.analyse(dir) })
	case name == "profile/save":
		d.runTask("saving profile", func(context.Context, live.InputOptions, int, string) error { return d.saveProfile() })
	case name == "profile/submit":
		d.runTask("submitting profile", func(context.Context, live.InputOptions, int, string) error { return d.submitProfile() })
	default:
		fmt.Fprintf(d.out, "ignored unknown command %s\n", m.Address)
	}
}

func (d *daemon) publisher() *stagePublisher {
	return &stagePublisher{bridge: d.bc, out: d.out}
}

func (d *daemon) inputOptions(ch int) (live.InputOptions, error) {
	if len(d.cfg.sources) > 0 {
		return liveOptions(string(live.BackendJACK), d.cfg.sources, 0, d.cfg.jackClient)
	}
	return liveOptions(string(live.BackendJACK), nil, ch, d.cfg.jackClient)
}

func (d *daemon) setChannel(ch int) {
	d.mu.Lock()
	changed := ch != d.channel
	d.channel = ch
	restart := changed && d.monitorCancel != nil
	d.mu.Unlock()
	if !changed {
		return
	}
	d.say("channel set to CH%d", ch)
	if restart {
		d.stopMonitor()
		d.startMonitor()
	}
}

func (d *daemon) newSession() {
	dir := filepath.Join(d.cfg.sessionsRoot, time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		d.say("cannot create session: %v", err)
		return
	}
	d.mu.Lock()
	d.sessionDir = dir
	d.mu.Unlock()
	d.say("new session %s", filepath.Base(dir))
	d.publishResults(nil)
}

func (d *daemon) startMonitor() {
	d.mu.Lock()
	if d.monitorCancel != nil {
		d.mu.Unlock()
		return
	}
	ch := d.channel
	ctx, cancel := context.WithCancel(d.ctx)
	done := make(chan struct{})
	d.monitorCancel, d.monitorDone = cancel, done
	d.mu.Unlock()
	opts, err := d.inputOptions(ch)
	if err != nil {
		d.say("monitor: %v", err)
		d.clearMonitor(done)
		return
	}
	opts.JACKClientName = d.cfg.jackClient + "-monitor"
	d.auto.reset()
	d.say("monitor started on CH%d: ring-out candidates and live advice (read-only)", ch)
	go func() {
		defer d.clearMonitor(done)
		if err := runMonitor(ctx, opts, ch, d.bc, ringOutSubmitMode{}, d.auto, d.out); err != nil {
			d.say("monitor stopped: %v", err)
		}
	}()
}

func (d *daemon) clearMonitor(done chan struct{}) {
	d.mu.Lock()
	if d.monitorDone == done {
		d.monitorCancel, d.monitorDone = nil, nil
	}
	d.mu.Unlock()
	close(done)
}

// stopMonitor also switches automatic cuts off: they only make sense while
// the monitor listens to the channel they would cut.
func (d *daemon) stopMonitor() {
	d.mu.Lock()
	cancel, done := d.monitorCancel, d.monitorDone
	d.mu.Unlock()
	if cancel == nil {
		return
	}
	if d.auto.isEnabled() {
		_ = d.bc.DisableAuto()
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// runTask runs one capture/analysis/profile job at a time.
func (d *daemon) runTask(name string, fn func(ctx context.Context, opts live.InputOptions, ch int, dir string) error) {
	d.mu.Lock()
	if d.task != "" {
		busy := d.task
		d.mu.Unlock()
		d.say("busy (%s); press STOP first", busy)
		return
	}
	ctx, cancel := context.WithCancel(d.ctx)
	d.task, d.taskCancel = name, cancel
	ch, dir := d.channel, d.sessionDir
	d.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			d.mu.Lock()
			d.task, d.taskCancel = "", nil
			d.mu.Unlock()
		}()
		opts, err := d.inputOptions(ch)
		if err == nil {
			err = fn(ctx, opts, ch, dir)
		}
		if err != nil && !errors.Is(err, errCaptureCancelled) {
			d.say("%s failed: %v", name, err)
		}
	}()
}

func (d *daemon) cancelTask() {
	d.mu.Lock()
	cancel := d.taskCancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
		d.say("stopping current task")
	}
}

func (d *daemon) armPA() {
	if d.cfg.playSink == "" {
		d.say("PA measurement is disabled: the service has no -play-pink-to sink configured")
		return
	}
	d.mu.Lock()
	d.paArmedUntil = time.Now().Add(paArmWindow)
	ch := d.channel
	d.mu.Unlock()
	d.say("PA measurement ARMED: plays pink noise at %.0f dBFS peak to %s and measures CH%d. Check the desk routing and start with the fader down. Press CONFIRM within %.0f s.",
		-d.cfg.pink.HeadroomDB, d.cfg.playSink, ch, paArmWindow.Seconds())
}

func (d *daemon) confirmPA() {
	d.mu.Lock()
	armed := time.Now().Before(d.paArmedUntil)
	d.paArmedUntil = time.Time{}
	d.mu.Unlock()
	if !armed {
		d.say("PA measurement not started: press ARM first, then CONFIRM within %.0f s", paArmWindow.Seconds())
		return
	}
	d.runTask("measuring PA", func(ctx context.Context, opts live.InputOptions, ch int, dir string) error {
		opts.JACKClientName = d.cfg.jackClient + "-pa"
		return runPAMeasure(ctx, opts, ch, d.cfg.playSink, d.cfg.pink, true, dir, d.publisher())
	})
}

func (d *daemon) analyse(dir string) error {
	if !fileExists(filepath.Join(dir, stageFiles[room.StageEmpty])) {
		return fmt.Errorf("capture the empty room first; analysis needs it as the noise reference")
	}
	if err := run([]string{"-session-dir", dir}); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(dir, "jive-room-session.json"))
	if err != nil {
		return err
	}
	var s room.Session
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	d.publishResults(summariseSession(s))
	d.say("analysis written to %s (jive-room-session.json and .md)", filepath.Base(dir))
	return nil
}

// summariseSession turns a session into short display lines.
func summariseSession(s room.Session) []string {
	lines := []string{fmt.Sprintf("stages: %s", strings.Join(s.CompletedStages, ", "))}
	if s.PARoom != nil && s.PARoom.Status != "not_measured" {
		pa := "PA response: " + s.PARoom.Status
		if g := s.PARoom.SignalToNoise; g != nil {
			pa += fmt.Sprintf(" (median %.1f dB above room noise)", g.MedianDB)
		}
		lines = append(lines, pa)
	}
	for _, r := range s.SuggestedEQ {
		line := r.Kind
		if r.FrequencyHz > 0 {
			line += " " + formatUnit(r.FrequencyHz, "Hz")
		}
		if r.GainDB != 0 {
			line += fmt.Sprintf(" %+.1f dB", r.GainDB)
		}
		lines = append(lines, line+": "+r.Reason)
	}
	return lines
}

func (d *daemon) publishResults(lines []string) {
	for i := range resultUISlots {
		v := ""
		if i < len(lines) {
			v = lines[i]
		}
		_ = d.bc.PublishUI(fmt.Sprintf("/dsp/ui/room/result/%d", i+1), v)
	}
}

func (d *daemon) saveProfile() error {
	path := filepath.Join(d.cfg.profilesDir, time.Now().Format("20060102-150405")+".json")
	p, err := studiolive.ProfileFromState(d.bc.State, nil, true, "saved from the ROOM tab", time.Now())
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return err
	}
	d.mu.Lock()
	d.lastProfile = path
	d.mu.Unlock()
	d.say("saved %d desk controls to profile %s (read-only)", len(p.Controls), filepath.Base(path))
	return nil
}

// latestProfile is the last profile saved in this run, else the newest file.
func (d *daemon) latestProfile() (string, error) {
	d.mu.Lock()
	last := d.lastProfile
	d.mu.Unlock()
	if last != "" {
		return last, nil
	}
	matches, err := filepath.Glob(filepath.Join(d.cfg.profilesDir, "*.json"))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("no saved profile in %s", d.cfg.profilesDir)
	}
	sort.Slice(matches, func(i, j int) bool {
		a, _ := os.Stat(matches[i])
		b, _ := os.Stat(matches[j])
		return a.ModTime().After(b.ModTime())
	})
	return matches[0], nil
}

func (d *daemon) submitProfile() error {
	path, err := d.latestProfile()
	if err != nil {
		return err
	}
	p, err := loadProfile(path)
	if err != nil {
		return err
	}
	changes, err := studiolive.ProfileDiff(p, d.bc.State)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		d.say("the desk already matches profile %s; nothing to submit", filepath.Base(path))
		return nil
	}
	w, err := studiolive.ProposalFromProfile(p, changes, time.Now())
	if err != nil {
		return err
	}
	if err := d.bc.SubmitProposal(w); err != nil {
		return err
	}
	d.say("restore of profile %s (%d controls) is pending: double-tap APPLY to write it, or ignore it", filepath.Base(path), len(changes))
	return nil
}
