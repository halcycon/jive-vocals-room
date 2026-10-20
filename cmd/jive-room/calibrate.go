package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/live"
	"github.com/linuxmatters/jive-vocals/internal/room"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

const (
	calFFT          = 8192
	calHop          = 4096
	calPinkSeconds  = 3.0
	calClipDBFS     = -1.0
	hpfCornerDB     = -3.0
	calBandsPerOct  = 12
	calLowHz        = 25.0
	calHighHz       = 20000.0
	minCalPoints    = 4
	calSettleWrites = 300 * time.Millisecond
)

var alsaDevicePattern = regexp.MustCompile(`^[A-Za-z0-9:,_.=-]{1,64}$`)

// calSweepRaws are the knob positions measured for each control.
var calSweepRaws = []int{0, 32, 64, 96, 128, 160, 192, 224, 255}

// transferDB estimates |H(f)|² in dB at 1/12-octave centres from a reference
// and a capture of it: Welch-averaged power of each, then the band power ratio.
// Magnitude only, so the two need no time alignment for stationary noise.
func transferDB(ref, meas []float64, rate int) (centres, gains []float64) {
	pr, pm := welch(ref), welch(meas)
	bin := float64(rate) / calFFT
	for f := calLowHz; f <= math.Min(calHighHz, 0.45*float64(rate)); f *= math.Pow(2, 1.0/calBandsPerOct) {
		lo := int(math.Floor(f / math.Pow(2, 1.0/(2*calBandsPerOct)) / bin))
		hi := int(math.Ceil(f * math.Pow(2, 1.0/(2*calBandsPerOct)) / bin))
		lo, hi = max(1, lo), min(len(pr)-1, max(lo+1, hi))
		var sr, sm float64
		for k := lo; k <= hi; k++ {
			sr += pr[k]
			sm += pm[k]
		}
		centres = append(centres, f)
		gains = append(gains, 10*math.Log10(math.Max(sm, 1e-30)/math.Max(sr, 1e-30)))
	}
	return centres, gains
}

func welch(x []float64) []float64 {
	out := make([]float64, calFFT/2+1)
	buf := make([]complex128, calFFT)
	n := 0
	for start := 0; start+calFFT <= len(x); start += calHop {
		for i := range calFFT {
			w := .5 - .5*math.Cos(2*math.Pi*float64(i)/float64(calFFT-1))
			buf[i] = complex(x[start+i]*w, 0)
		}
		room.FFT(buf)
		for k := range out {
			out[k] += real(buf[k])*real(buf[k]) + imag(buf[k])*imag(buf[k])
		}
		n++
	}
	for k := range out {
		out[k] /= float64(max(1, n))
	}
	return out
}

func subtract(a, b []float64) []float64 {
	out := make([]float64, len(a))
	for i := range a {
		out[i] = a[i] - b[i]
	}
	return out
}

// peakOf returns the log-frequency-interpolated location and value of the
// largest effect (sign selects boost or cut).
func peakOf(centres, effect []float64, sign float64) (float64, float64) {
	best := 0
	for i := range effect {
		if sign*effect[i] > sign*effect[best] {
			best = i
		}
	}
	if best == 0 || best == len(effect)-1 {
		return centres[best], effect[best]
	}
	a, b, c := effect[best-1], effect[best], effect[best+1]
	den := a - 2*b + c
	d := 0.0
	if den != 0 {
		d = math.Max(-0.5, math.Min(0.5, 0.5*(a-c)/den))
	}
	return centres[best] * math.Pow(2, d/calBandsPerOct), b - 0.25*(a-c)*d
}

func valueAt(centres, effect []float64, f float64) float64 {
	for i := 1; i < len(centres); i++ {
		if centres[i] >= f {
			t := math.Log(f/centres[i-1]) / math.Log(centres[i]/centres[i-1])
			return effect[i-1] + t*(effect[i]-effect[i-1])
		}
	}
	return effect[len(effect)-1]
}

// hpfCorner finds the frequency where a high-pass effect rises through -3 dB,
// scanning down from the top so ripple above the corner is ignored.
func hpfCorner(centres, effect []float64) (float64, bool) {
	for i := len(effect) - 1; i > 0; i-- {
		if effect[i-1] <= hpfCornerDB && effect[i] > hpfCornerDB {
			t := (hpfCornerDB - effect[i-1]) / (effect[i] - effect[i-1])
			return centres[i-1] * math.Pow(centres[i]/centres[i-1], t), true
		}
	}
	return 0, false
}

type calStep struct {
	label string
	set   map[string]int
}

func calBase() map[string]int {
	return map[string]int{
		"hpf": 0, "eqlow": 0, "eqmid": 0, "eqhigh": 0, "eqlowshelf": 0, "eqmidhiq": 0, "eqhighshelf": 0,
		"hpffreq": 128, "eqlowfreq": 128, "eqmidfreq": 128, "eqhighfreq": 128,
		"eqlowgain": 128, "eqmidgain": 128, "eqhighgain": 128,
	}
}

func withSet(over map[string]int) map[string]int {
	m := calBase()
	maps.Copy(m, over)
	return m
}

// calibrationSteps: a flat reference, then per band a frequency sweep at full
// boost and a gain sweep at mid frequency, then the HPF corner sweep.
func calibrationSteps() []calStep {
	steps := []calStep{{label: "flat", set: calBase()}}
	for _, band := range []string{"low", "mid", "high"} {
		for _, r := range calSweepRaws {
			steps = append(steps, calStep{
				label: fmt.Sprintf("eq%sfreq=%d", band, r),
				set:   withSet(map[string]int{"eq" + band: 1, "eq" + band + "freq": r, "eq" + band + "gain": 255}),
			})
		}
		for _, r := range calSweepRaws {
			steps = append(steps, calStep{
				label: fmt.Sprintf("eq%sgain=%d", band, r),
				set:   withSet(map[string]int{"eq" + band: 1, "eq" + band + "freq": 128, "eq" + band + "gain": r}),
			})
		}
	}
	for _, r := range calSweepRaws {
		steps = append(steps, calStep{label: fmt.Sprintf("hpffreq=%d", r), set: withSet(map[string]int{"hpf": 1, "hpffreq": r})})
	}
	return steps
}

type calMeasurement struct {
	centres, effect []float64
	peakDBFS        float64
}

// fitCalibration turns per-step effects into monotonic raw->unit points.
func fitCalibration(m map[string]calMeasurement) (map[string][]studiolive.CalPoint, []string) {
	out := map[string][]studiolive.CalPoint{}
	var notes []string
	add := func(name string, pts []studiolive.CalPoint) {
		sort.Slice(pts, func(i, j int) bool { return pts[i].Raw < pts[j].Raw })
		var mono []studiolive.CalPoint
		for _, p := range pts {
			if len(mono) == 0 || p.Value > mono[len(mono)-1].Value {
				mono = append(mono, p)
			} else {
				notes = append(notes, fmt.Sprintf("%s: dropped non-monotonic point raw %d (%.4g)", name, p.Raw, p.Value))
			}
		}
		if len(mono) >= minCalPoints {
			out[name] = mono
		} else {
			notes = append(notes, fmt.Sprintf("%s: only %d usable points; not calibrated", name, len(mono)))
		}
	}
	for _, band := range []string{"low", "mid", "high"} {
		var fpts, gpts []studiolive.CalPoint
		var centre float64
		for _, r := range calSweepRaws {
			if cm, ok := m[fmt.Sprintf("eq%sfreq=%d", band, r)]; ok {
				f, _ := peakOf(cm.centres, cm.effect, 1)
				fpts = append(fpts, studiolive.CalPoint{Raw: r, Value: f})
				if r == 128 {
					centre = f
				}
			}
		}
		for _, r := range calSweepRaws {
			if cm, ok := m[fmt.Sprintf("eq%sgain=%d", band, r)]; ok && centre > 0 {
				gpts = append(gpts, studiolive.CalPoint{Raw: r, Value: valueAt(cm.centres, cm.effect, centre)})
			}
		}
		add("eq"+band+"freq", fpts)
		add("eq"+band+"gain", gpts)
	}
	var hpts []studiolive.CalPoint
	for _, r := range calSweepRaws {
		if cm, ok := m[fmt.Sprintf("hpffreq=%d", r)]; ok {
			if f, ok := hpfCorner(cm.centres, cm.effect); ok {
				hpts = append(hpts, studiolive.CalPoint{Raw: r, Value: f})
			} else {
				notes = append(notes, fmt.Sprintf("hpffreq: no -3 dB corner found at raw %d", r))
			}
		}
	}
	add("hpffreq", hpts)
	return out, notes
}

func calibrationProposal(ch string, set map[string]int, label string) studiolive.WireProposal {
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	w := studiolive.WireProposal{
		Schema: studiolive.ProposalSchema, Source: studiolive.SourceProfile,
		ID: "calibrate-" + strconv.FormatInt(time.Now().UnixNano(), 36), Summary: "calibration " + label,
	}
	for _, n := range names {
		w.Changes = append(w.Changes, studiolive.WireChange{Address: "/channel/" + ch + "/" + n, Raw: set[n]})
	}
	return w
}

func applyQuietly(s *bridgeSession, w studiolive.WireProposal) error {
	if err := s.c.SubmitProposal(w); err != nil {
		return err
	}
	r, err := s.await("submit", w.ID)
	if err != nil {
		return fmt.Errorf("submit %s: %w", w.ID, err)
	}
	if r.Status != "pending" {
		return fmt.Errorf("submit %s: %s %s", w.ID, r.Status, r.Reason)
	}
	if err := s.c.RequestApply(w.ID); err != nil {
		return err
	}
	r, err = s.await("apply", w.ID)
	if err != nil {
		return err
	}
	if r.Status != "verified" {
		return fmt.Errorf("%s: %s %s", w.Summary, r.Status, r.Reason)
	}
	return nil
}

// externalPlayer plays the reference through an ALSA device (left channel only,
// so a stereo plug into a balanced input does not cancel) instead of JACK.
type externalPlayer struct {
	device string
	path   string
}

func writeLeftOnlyWAV(path string, mono []float64, rate int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data := make([]float32, 2*len(mono))
	for i, v := range mono {
		data[2*i] = float32(v)
	}
	dataBytes := uint32(4 * len(data)) //nolint:gosec // bounded by the calibration signal length
	r := uint32(rate)                  //nolint:gosec // positive JACK rate
	for _, v := range []any{
		[4]byte{'R', 'I', 'F', 'F'},
		uint32(4+26+12+8) + dataBytes,
		[4]byte{'W', 'A', 'V', 'E'},
		[4]byte{'f', 'm', 't', ' '},
		uint32(18), uint16(3), uint16(2), r, r * 8, uint16(8), uint16(32), uint16(0),
		[4]byte{'f', 'a', 'c', 't'},
		uint32(4), uint32(len(mono)), //nolint:gosec // bounded
		[4]byte{'d', 'a', 't', 'a'},
		dataBytes,
	} {
		if err := binary.Write(f, binary.LittleEndian, v); err != nil {
			return err
		}
	}
	return binary.Write(f, binary.LittleEndian, data)
}

// capture plays the reference externally and records the steady middle.
func (e externalPlayer) capture(ctx context.Context, opts live.InputOptions, plan paPlan, rate int) ([]float64, error) {
	frames := plan.endFrame - plan.startFrame
	rec := newRecorder(frames)
	opts.SampleTap = rec.tap
	opts.RingFrames = captureRingFrames
	a, err := live.OpenInputAnalyser(opts)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	if err := a.Start(ctx, rec.observe); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "aplay", "-q", "-D", e.device, e.path) //nolint:gosec // operator flag, validated by alsaDevicePattern; no shell
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("aplay: %w", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	select {
	case <-ctx.Done():
		return nil, errCaptureCancelled
	case <-time.After(time.Duration(float64(plan.startFrame) / float64(rate) * float64(time.Second))):
	}
	rec.arm()
	select {
	case <-ctx.Done():
		return nil, errCaptureCancelled
	case <-rec.full:
	case <-time.After(time.Duration(plan.spec.DurationSeconds+2) * time.Second):
		return nil, fmt.Errorf("capture did not fill")
	}
	_ = a.Close()
	if err := rec.dropError(); err != nil {
		return nil, err
	}
	return rec.samples, nil
}

func runCalibrate(bridgeAddr string, channel int, jackClient, outPath, play string, confirmed bool, w io.Writer) error {
	if channel < 1 || channel > studiolive.MonoInputChannels {
		return usageError{fmt.Errorf("-studiolive-calibrate needs a mono -studiolive-channel 1..%d", studiolive.MonoInputChannels)}
	}
	ch := studiolive.InputChannelName(channel)
	sink, source := fmt.Sprintf("_Ch %d_out", channel), fmt.Sprintf("_Ch %d_in", channel)
	if !confirmed {
		return usageError{fmt.Errorf(`calibration plays pink noise into %s's FireWire return (%s) and steps %s's HPF/EQ through the bridge.
Before running: set %s's FireWire send post-DSP and feed its input (its FireWire return, or a cable from
-calibration-play), pull its fader down, and keep it off anything audible. Its HPF/EQ is snapshotted and restored.
Re-run with -confirm-apply -confirm-playback`, ch, sink, ch, ch)}
	}
	s, err := openBridgeSession(bridgeAddr)
	if err != nil {
		return err
	}
	defer s.close()
	var ext *externalPlayer
	if dev, ok := strings.CutPrefix(play, "alsa:"); ok && alsaDevicePattern.MatchString(dev) {
		ext = &externalPlayer{device: dev, path: outPath + ".reference-left.wav"}
	} else if play != "" {
		return usageError{fmt.Errorf("-calibration-play must be alsa:<device>, e.g. alsa:plughw:0,0")}
	}
	want := map[string]bool{"firewire": true, "post": true}
	if ext != nil {
		want["firewire"] = false
	}
	for sw, on := range want {
		if v, ok := s.c.State.Get(studiolive.Control{Channel: ch, Name: sw}); !ok || v.On() != on {
			return fmt.Errorf("%s/%s must be %s for this loop (FireWire send post-DSP; input %s)", ch, sw, map[bool]string{true: "on", false: "off"}[on],
				map[bool]string{true: "from its FireWire return", false: "analogue, cabled from " + play}[ext == nil])
		}
	}
	before, err := studiolive.ProfileFromState(s.c.State, []int{channel}, false, "before calibration", time.Now())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer func() {
		changes, err := studiolive.ProfileDiff(before, s.c.State)
		if err == nil && len(changes) > 0 {
			time.Sleep(1100 * time.Millisecond)
			p, _ := studiolive.ProposalFromProfile(before, changes, time.Now())
			if err := applyQuietly(s, p); err != nil {
				fmt.Fprintf(w, "WARNING: restoring %s's EQ failed: %v; use -studiolive-profile-push with the saved snapshot\n", ch, err)
				return
			}
		}
		fmt.Fprintf(w, "%s HPF/EQ restored to its pre-calibration state. Switch its FireWire input and post-DSP send back off.\n", ch)
	}()
	if b, err := json.MarshalIndent(before, "", "  "); err == nil {
		_ = os.WriteFile(outPath+".before-"+ch+".json", append(b, '\n'), 0o644)
	}

	rate, err := live.JACKSampleRate(jackClient + "-probe")
	if err != nil {
		return err
	}
	spec := room.DefaultPinkSpec()
	spec.SampleRateHz, spec.DurationSeconds, spec.HeadroomDB = rate, calPinkSeconds, minPlaybackHeadroomDB
	spec.FadeInSeconds, spec.FadeOutSeconds, spec.HighPassHz, spec.LowPassHz, spec.Seed = 0.05, 0.05, 20, 20000, 3
	plan, err := planPAMeasurement(spec, rate)
	if err != nil {
		return err
	}
	ref := plan.reference.Samples[plan.startFrame:plan.endFrame]
	if ext != nil {
		if err := writeLeftOnlyWAV(ext.path, plan.reference.Samples, rate); err != nil {
			return err
		}
		defer os.Remove(ext.path)
		sink = play + " (left channel)"
	}
	opts, err := liveOptions(string(live.BackendJACK), []string{source}, 0, jackClient+"-cal")
	if err != nil {
		return err
	}
	steps := calibrationSteps()
	fmt.Fprintf(w, "Calibrating on %s: %d steps, pink at %.0f dBFS peak to %s, measuring %s.\n", ch, len(steps), -spec.HeadroomDB, sink, source)
	var flat []float64
	results := map[string]calMeasurement{}
	for i, st := range steps {
		if err := ctx.Err(); err != nil {
			return errCaptureCancelled
		}
		if err := applyQuietly(s, calibrationProposal(ch, st.set, st.label)); err != nil {
			return fmt.Errorf("step %s: %w", st.label, err)
		}
		time.Sleep(calSettleWrites)
		var samples []float64
		if ext != nil {
			samples, err = ext.capture(ctx, opts, plan, rate)
		} else {
			var pc playCapture
			pc, err = playAndCapture(ctx, opts, sink, plan)
			samples = pc.samples
		}
		if err != nil {
			return fmt.Errorf("step %s: %w", st.label, err)
		}
		peak := 0.0
		for _, x := range samples {
			peak = math.Max(peak, math.Abs(x))
		}
		peakDB := 20 * math.Log10(math.Max(peak, 1e-12))
		if peakDB >= calClipDBFS {
			return fmt.Errorf("step %s: capture peak %.1f dBFS is clipping", st.label, peakDB)
		}
		centres, gains := transferDB(ref, samples, rate)
		if st.label == "flat" {
			flat = gains
			if peakDB < -60 {
				return fmt.Errorf("no test signal in %s (peak %.1f dBFS): check the FireWire return input and post-DSP send", source, peakDB)
			}
		}
		results[st.label] = calMeasurement{centres: centres, effect: subtract(gains, flat), peakDBFS: peakDB}
		fmt.Fprintf(w, "  [%2d/%d] %-16s peak %6.1f dBFS\n", i+1, len(steps), st.label, peakDB)
	}
	points, notes := fitCalibration(results)
	f := studiolive.CalibrationFile{
		Schema: studiolive.CalibrationSchema, CreatedAt: time.Now().UTC(), Channel: channel,
		SampleRateHz: rate, Method: "electrical loop: pink noise via FireWire return, post-DSP FireWire send, Welch transfer at 1/12 octave",
		Controls: points, Notes: append(notes, "measured on one mono channel; assumed identical on all input channels"),
	}
	if err := f.Validate(); err != nil {
		return fmt.Errorf("calibration not written: %w", err)
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath+".tmp", append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(outPath+".tmp", outPath); err != nil {
		return err
	}
	printCalibration(w, f)
	fmt.Fprintf(w, "Wrote %s.\n", outPath)
	return nil
}

func printCalibration(w io.Writer, f studiolive.CalibrationFile) {
	names := make([]string, 0, len(f.Controls))
	for n := range f.Controls {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		model := studiolive.InputCalibration[n]
		fmt.Fprintf(w, "  %-11s", n)
		for _, p := range f.Controls[n] {
			fmt.Fprintf(w, "  %d:%s", p.Raw, formatUnit(p.Value, model.Unit))
		}
		fmt.Fprintln(w)
	}
	for _, n := range f.Notes {
		fmt.Fprintln(w, "  note:", n)
	}
}
