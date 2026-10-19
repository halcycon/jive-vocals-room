package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/live"
	"github.com/linuxmatters/jive-vocals/internal/room"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

const (
	captureSchema     = "jive-room-capture/v1"
	captureRingFrames = 1 << 16
	minCaptureSeconds = 5.0
	maxCaptureSeconds = 60.0
	// MinPlaybackHeadroomDB keeps operator-started pink noise at or below
	// -20 dBFS peak. The operator sets the audible level on the desk.
	minPlaybackHeadroomDB = 20.0
	playbackSettleSeconds = 0.5
	playbackStartDelay    = 500 * time.Millisecond
	clipWarnDBFS          = -1.0
	noSignalDBFS          = -90.0
)

var errCaptureCancelled = errors.New("capture cancelled; nothing saved")

// Stage capture file names inside a session directory.
var stageFiles = map[string]string{
	room.StageEmpty:     "empty.wav",
	room.StageOccupied:  "occupied.wav",
	room.StagePresenter: "presenter.wav",
	room.StageVerify:    "verification.wav",
}

const (
	paReferenceFile = "pa-reference.wav"
	paMeasuredFile  = "pa-measured.wav"
)

var stageAliases = map[string]string{
	"empty": room.StageEmpty, "occupied": room.StageOccupied,
	"presenter": room.StagePresenter, "verification": room.StageVerify, "verify": room.StageVerify,
}

// CaptureMeta is written beside every capture as <name>.capture.json.
type CaptureMeta struct {
	Schema              string    `json:"schema_version"`
	Stage               string    `json:"stage"`
	CreatedAt           time.Time `json:"created_at"`
	SampleRateHz        int       `json:"sample_rate_hz"`
	DurationSeconds     float64   `json:"duration_seconds"`
	JACKSources         []string  `json:"jack_sources"`
	StudioLiveChannel   int       `json:"studiolive_channel,omitempty"`
	XRunsDuringCapture  uint64    `json:"xruns_during_capture"`
	AnalysisDrops       uint64    `json:"analysis_drops_during_capture"`
	RMSDBFS             float64   `json:"rms_dbfs"`
	PeakDBFS            float64   `json:"peak_dbfs"`
	PlaybackSink        string    `json:"playback_sink,omitempty"`
	PlaybackPeakDBFS    *float64  `json:"playback_peak_dbfs,omitempty"`
	PlaybackStartSecond *float64  `json:"capture_start_in_playback_seconds,omitempty"`
	Notes               []string  `json:"notes,omitempty"`
}

func metaPath(wav string) string {
	return wav[:len(wav)-len(filepath.Ext(wav))] + ".capture.json"
}

func writeCapture(path string, samples []float64, rate int, meta CaptureMeta) (CaptureMeta, error) {
	meta.Schema = captureSchema
	meta.CreatedAt = time.Now().UTC()
	meta.SampleRateHz = rate
	meta.DurationSeconds = float64(len(samples)) / float64(rate)
	var sum, peak float64
	for _, x := range samples {
		sum += x * x
		peak = math.Max(peak, math.Abs(x))
	}
	meta.RMSDBFS = finite(20 * math.Log10(math.Sqrt(sum/float64(max(1, len(samples))))))
	meta.PeakDBFS = finite(20 * math.Log10(peak))
	if meta.PeakDBFS >= clipWarnDBFS {
		meta.Notes = append(meta.Notes, fmt.Sprintf("peak %.1f dBFS is at or near clipping; lower the input gain and capture again", meta.PeakDBFS))
	}
	if meta.RMSDBFS <= noSignalDBFS {
		meta.Notes = append(meta.Notes, fmt.Sprintf("RMS %.1f dBFS is at the converter floor; check the microphone, input gain, and channel selection", meta.RMSDBFS))
	}
	if meta.XRunsDuringCapture > 0 {
		meta.Notes = append(meta.Notes, fmt.Sprintf("%d JACK xruns during capture; the audio may contain discontinuities", meta.XRunsDuringCapture))
	}
	if err := room.WriteFloatWAV(path, room.PCM{Samples: samples, SampleRate: rate, Channels: 1}); err != nil {
		return meta, err
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return meta, err
	}
	return meta, os.WriteFile(metaPath(path), append(b, '\n'), 0o644)
}

func finite(v float64) float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return -140
	}
	return math.Max(-140, v)
}

// recorder collects mono samples while armed. tap and observe run on the
// analyser's consumer goroutine (tap first, within each snapshot); arm and
// stop run on the caller's goroutine. samples, xruns, and drops may be read
// once the analyser is closed.
type recorder struct {
	armed    atomic.Bool
	finished atomic.Bool
	sealed   bool
	target   int
	samples  []float64
	full     chan struct{}

	lastXRuns, lastDrops atomic.Uint64
	baseXRuns, baseDrops atomic.Uint64
	xruns, drops         uint64
}

func newRecorder(targetSamples int) *recorder {
	return &recorder{target: targetSamples, full: make(chan struct{})}
}

func (r *recorder) tap(mono []float64, _ int) {
	if !r.armed.Load() || r.finished.Load() {
		return
	}
	need := r.target - len(r.samples)
	if r.target > 0 && len(mono) > need {
		mono = mono[:need]
	}
	r.samples = append(r.samples, mono...)
	if r.target > 0 && len(r.samples) >= r.target && r.finished.CompareAndSwap(false, true) {
		close(r.full)
	}
}

// observe runs after tap for each snapshot. Snapshot counters are cumulative;
// counts are relative to the last snapshot before arming and include the batch
// in which recording finished (conservatively attributing that whole batch).
func (r *recorder) observe(s live.Snapshot) {
	r.lastXRuns.Store(s.XRuns)
	r.lastDrops.Store(s.AnalysisOverruns)
	if !r.armed.Load() || r.sealed {
		return
	}
	r.sealed = r.finished.Load()
	r.xruns = s.XRuns - r.baseXRuns.Load()
	r.drops = s.AnalysisOverruns - r.baseDrops.Load()
}

// arm starts recording from the next captured batch.
func (r *recorder) arm() {
	r.baseXRuns.Store(r.lastXRuns.Load())
	r.baseDrops.Store(r.lastDrops.Load())
	r.armed.Store(true)
}

// stop ends an open-ended recording.
func (r *recorder) stop() {
	if r.finished.CompareAndSwap(false, true) {
		close(r.full)
	}
}

func (r *recorder) dropError() error {
	if r.drops > 0 {
		return fmt.Errorf("%d analysis frames were dropped during capture, so the audio has gaps; capture discarded", r.drops)
	}
	return nil
}

type stagePublisher struct {
	bridge    *studiolive.BridgeClient
	owned     bool
	out       io.Writer
	lastPrint time.Time
}

func newStagePublisher(addr string) *stagePublisher {
	bc, err := studiolive.DialBridge(addr)
	if err != nil {
		return &stagePublisher{out: os.Stdout}
	}
	return &stagePublisher{bridge: bc, owned: true, out: os.Stdout}
}

// say prints and publishes a status line to the ROOM tab.
func (p *stagePublisher) say(format string, args ...any) {
	p.publish(true, format, args...)
}

// progress publishes every call but prints at most once a second.
func (p *stagePublisher) progress(format string, args ...any) {
	p.publish(time.Since(p.lastPrint) >= time.Second, format, args...)
}

func (p *stagePublisher) publish(toStdout bool, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if toStdout && p.out != nil {
		p.lastPrint = time.Now()
		fmt.Fprintln(p.out, msg)
	}
	if p.bridge != nil {
		_ = p.bridge.PublishUI("/dsp/ui/room/stage", msg)
	}
}

func (p *stagePublisher) close() {
	if p.bridge != nil && p.owned {
		_ = p.bridge.Close()
	}
}

func checkCaptureSeconds(s float64) error {
	if s < minCaptureSeconds || s > maxCaptureSeconds {
		return fmt.Errorf("capture length %.1f s is outside %.0f–%.0f s", s, minCaptureSeconds, maxCaptureSeconds)
	}
	return nil
}

func runStageCapture(ctx context.Context, stageArg string, opts live.InputOptions, channel int, seconds float64, dir string, pub *stagePublisher) error {
	stage, ok := stageAliases[stageArg]
	if !ok {
		return usageError{fmt.Errorf("unknown capture stage %q (want empty, occupied, presenter, or verification)", stageArg)}
	}
	if err := checkCaptureSeconds(seconds); err != nil {
		return usageError{err}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	rate, err := live.JACKSampleRate(opts.JACKClientName + "-probe")
	if err != nil {
		return err
	}
	rec := newRecorder(int(math.Round(seconds * float64(rate))))
	opts.SampleTap = rec.tap
	opts.RingFrames = captureRingFrames
	a, err := live.OpenInputAnalyser(opts)
	if err != nil {
		return err
	}
	closed := false
	closeOnce := func() {
		if !closed {
			closed = true
			_ = a.Close()
		}
	}
	defer closeOnce()
	start := time.Now()
	if err := a.Start(ctx, func(s live.Snapshot) {
		rec.observe(s)
		pub.progress("capturing %s on %v: %.0f / %.0f s  xruns %d  drops %d", stage, opts.JACKSources, time.Since(start).Seconds(), seconds, rec.xruns, rec.drops)
	}); err != nil {
		return err
	}
	rec.arm()
	select {
	case <-ctx.Done():
		closeOnce()
		pub.say("capture of %s cancelled; nothing saved", stage)
		return errCaptureCancelled
	case <-rec.full:
	}
	closeOnce()
	if err := rec.dropError(); err != nil {
		pub.say("capture of %s failed: %v", stage, err)
		return err
	}
	path := filepath.Join(dir, stageFiles[stage])
	meta, err := writeCapture(path, rec.samples, rate, CaptureMeta{
		Stage: stage, JACKSources: opts.JACKSources, StudioLiveChannel: channel,
		XRunsDuringCapture: rec.xruns, AnalysisDrops: rec.drops,
	})
	if err != nil {
		return err
	}
	pub.say("saved %s: %.1f s, RMS %.1f dBFS, peak %.1f dBFS", path, meta.DurationSeconds, meta.RMSDBFS, meta.PeakDBFS)
	for _, n := range meta.Notes {
		pub.say("note: %s", n)
	}
	return nil
}

// paPlan is the generated reference and the capture window inside it.
type paPlan struct {
	spec       room.TestSignalSpec
	reference  room.PCM
	startFrame int
	endFrame   int
}

func planPAMeasurement(spec room.TestSignalSpec, rate int) (paPlan, error) {
	spec.SampleRateHz = rate
	if spec.HeadroomDB < minPlaybackHeadroomDB {
		return paPlan{}, fmt.Errorf("playback headroom %.1f dB is below the %.0f dB minimum (peak must be at or below %.0f dBFS)", spec.HeadroomDB, minPlaybackHeadroomDB, -minPlaybackHeadroomDB)
	}
	ref, err := room.GeneratePink(spec)
	if err != nil {
		return paPlan{}, err
	}
	start := int(math.Ceil((spec.FadeInSeconds + playbackSettleSeconds) * float64(rate)))
	end := len(ref.Samples) - int(math.Ceil(spec.FadeOutSeconds*float64(rate)))
	if end-start < 2*rate {
		return paPlan{}, fmt.Errorf("pink duration %.1f s leaves under 2 s of steady signal to measure", spec.DurationSeconds)
	}
	return paPlan{spec: spec, reference: ref, startFrame: start, endFrame: end}, nil
}

func runPAMeasure(ctx context.Context, opts live.InputOptions, channel int, sinkArg string, spec room.TestSignalSpec, confirmed bool, dir string, pub *stagePublisher) error {
	if sinkArg == "" {
		return usageError{fmt.Errorf("-studiolive-pa-measure needs -play-pink-to, e.g. '_Ch 15_out'")}
	}
	if !confirmed {
		return usageError{fmt.Errorf(`this plays pink noise to %q at up to %.0f dBFS peak. Before running:
  1. set the desk so that FireWire return reaches only the PA you intend to measure;
  2. start with that channel or the main fader DOWN and raise it slowly to a safe level;
  3. keep hearing protection in mind; jive-room never changes desk gain.
Re-run with -confirm-playback when ready`, sinkArg, -spec.HeadroomDB)}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	rate, err := live.JACKSampleRate(opts.JACKClientName + "-probe")
	if err != nil {
		return err
	}
	plan, err := planPAMeasurement(spec, rate)
	if err != nil {
		return usageError{err}
	}
	pub.say("PA measurement: pink noise to %s at %.0f dBFS peak for %.0f s, measuring %v. Ctrl+C stops with a fade.", sinkArg, -plan.spec.HeadroomDB, plan.spec.DurationSeconds, opts.JACKSources)
	pc, err := playAndCapture(ctx, opts, sinkArg, plan)
	if errors.Is(err, errCaptureCancelled) {
		pub.say("PA measurement cancelled; playback faded out; nothing saved")
		return err
	}
	if err != nil {
		pub.say("PA measurement failed: %v", err)
		return err
	}
	refPath := filepath.Join(dir, paReferenceFile)
	if err := room.WriteFloatWAV(refPath, plan.reference); err != nil {
		return err
	}
	b, err := json.MarshalIndent(plan.spec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(room.SignalSpecPath(refPath), append(b, '\n'), 0o644); err != nil {
		return err
	}
	peak := -plan.spec.HeadroomDB
	startS := float64(plan.startFrame) / float64(rate)
	measPath := filepath.Join(dir, paMeasuredFile)
	meta, err := writeCapture(measPath, pc.samples, rate, CaptureMeta{
		Stage: room.StagePA, JACKSources: opts.JACKSources, StudioLiveChannel: channel,
		XRunsDuringCapture: pc.xruns, AnalysisDrops: 0,
		PlaybackSink: pc.sink, PlaybackPeakDBFS: &peak, PlaybackStartSecond: &startS,
	})
	if err != nil {
		return err
	}
	pub.say("saved %s and %s: %.1f s measured, RMS %.1f dBFS, peak %.1f dBFS", refPath, measPath, meta.DurationSeconds, meta.RMSDBFS, meta.PeakDBFS)
	for _, n := range meta.Notes {
		pub.say("note: %s", n)
	}
	return nil
}

type captureArgs struct {
	stage     string
	pa        bool
	seconds   float64
	dir       string
	sink      string
	confirmed bool
	pink      room.TestSignalSpec
	bridge    string
	channel   int
	sources   []string
	client    string
}

func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func runStudioLiveCapture(fs *flag.FlagSet, a captureArgs) error {
	if a.stage != "" && a.pa {
		return usageError{fmt.Errorf("use either -studiolive-capture or -studiolive-pa-measure, not both")}
	}
	if a.channel == 0 && len(a.sources) == 0 {
		return usageError{fmt.Errorf("captures need -studiolive-channel or -jack-source")}
	}
	if a.dir == "" {
		a.dir = "."
	}
	opts, err := liveOptions(string(live.BackendJACK), a.sources, a.channel, a.client)
	if err != nil {
		return usageError{err}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pub := newStagePublisher(a.bridge)
	defer pub.close()
	if a.stage != "" {
		return runStageCapture(ctx, a.stage, opts, a.channel, a.seconds, a.dir, pub)
	}
	if !flagSet(fs, "pink-headroom-db") {
		a.pink.HeadroomDB = minPlaybackHeadroomDB
	}
	return runPAMeasure(ctx, opts, a.channel, a.sink, a.pink, a.confirmed, a.dir, pub)
}

type playCapture struct {
	samples []float64
	xruns   uint64
	sink    string
}

// playAndCapture plays plan.reference once to sink and records the analyser's
// sources over plan.startFrame..endFrame of playback. It discards the take if
// any analysis frames were dropped, and always fades playback out before the
// client closes, including on cancellation.
func playAndCapture(ctx context.Context, opts live.InputOptions, sink string, plan paPlan) (playCapture, error) {
	play := make([]float32, len(plan.reference.Samples))
	for i, v := range plan.reference.Samples {
		play[i] = float32(v)
	}
	rec := newRecorder(0)
	opts.SampleTap = rec.tap
	opts.RingFrames = captureRingFrames
	opts.Playback = &live.PlaybackRequest{Sink: sink, Samples: play}
	a, err := live.OpenInputAnalyser(opts)
	if err != nil {
		return playCapture{}, err
	}
	player, ok := a.(live.Player)
	if !ok {
		_ = a.Close()
		return playCapture{}, fmt.Errorf("this build cannot play test signals")
	}
	finish := func() {
		player.StopPlayback()
		deadline := time.Now().Add(2 * time.Second)
		for player.PlaybackState() != live.PlaybackDone && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		_ = a.Close()
	}
	if err := a.Start(ctx, rec.observe); err != nil {
		finish()
		return playCapture{}, err
	}
	time.Sleep(playbackStartDelay)
	if err := player.Play(); err != nil {
		finish()
		return playCapture{}, err
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for armed := false; ; {
		select {
		case <-ctx.Done():
			finish()
			return playCapture{}, errCaptureCancelled
		case <-tick.C:
		}
		pos := player.PlaybackFrames()
		if !armed && pos >= plan.startFrame {
			rec.arm()
			armed = true
		}
		if pos >= plan.endFrame || player.PlaybackState() == live.PlaybackDone {
			rec.stop()
			break
		}
	}
	sinkName := player.PlaybackSink()
	finish()
	if err := rec.dropError(); err != nil {
		return playCapture{}, err
	}
	return playCapture{samples: rec.samples, xruns: rec.xruns, sink: sinkName}, nil
}

// sessionDirInputs fills unset analysis inputs from standard capture names.
func sessionDirInputs(dir string, empty, occupied, presenter, paRef, paMeas, verification *string) {
	set := func(p *string, name string) {
		if *p != "" {
			return
		}
		if path := filepath.Join(dir, name); fileExists(path) {
			*p = path
		}
	}
	set(empty, stageFiles[room.StageEmpty])
	set(occupied, stageFiles[room.StageOccupied])
	set(presenter, stageFiles[room.StagePresenter])
	set(verification, stageFiles[room.StageVerify])
	if *paRef == "" && *paMeas == "" && fileExists(filepath.Join(dir, paReferenceFile)) && fileExists(filepath.Join(dir, paMeasuredFile)) {
		*paRef, *paMeas = filepath.Join(dir, paReferenceFile), filepath.Join(dir, paMeasuredFile)
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
