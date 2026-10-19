package main

import (
	"strings"
	"testing"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/feedback"
	"github.com/linuxmatters/jive-vocals/internal/osc"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

// measuredMid installs a verified calibration for the mid band only.
func measuredMid(t *testing.T) {
	t.Helper()
	f := studiolive.CalibrationFile{Schema: studiolive.CalibrationSchema, Channel: 8, Controls: map[string][]studiolive.CalPoint{
		"eqmidfreq": {{Raw: 0, Value: 260}, {Raw: 128, Value: 950}, {Raw: 255, Value: 3500}},
		"eqmidgain": {{Raw: 0, Value: -15}, {Raw: 128, Value: 0}, {Raw: 255, Value: 15}},
	}}
	if err := studiolive.UseCalibration(f); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(studiolive.ResetCalibration)
}

func ring(hz float64) feedback.Candidate {
	return feedback.Candidate{FrequencyHz: hz, Confidence: 0.95, Growing: true, DurationSeconds: 1.5}
}

func midState(t *testing.T, on bool, freqRaw, gainRaw float64) *studiolive.State {
	t.Helper()
	st := studiolive.NewState()
	sw := int32(0)
	if on {
		sw = 1
	}
	for addr, v := range map[string]any{
		"/channel/ch3/eqmid": sw, "/channel/ch3/eqmidfreq": float32(freqRaw / 255), "/channel/ch3/eqmidgain": float32(gainRaw / 255),
		"/channel/ch3/eqmidhiq": int32(0), "/channel/ch3/post": int32(0),
	} {
		st.Apply(osc.Message{Address: addr, Args: []any{v}}, time.Unix(0, 0))
	}
	return st
}

func TestAutoCutterNeedsEnableAndVerifiedCalibration(t *testing.T) {
	a := &autoCutter{}
	cs := []feedback.Candidate{ring(1250)}
	if _, reason, ok := a.decide(cs, 3, nil, time.Now()); ok || reason != "" {
		t.Fatalf("disabled cutter acted: %q", reason)
	}
	a.setEnabled(true)
	if _, reason, ok := a.decide(cs, 3, nil, time.Now()); ok || !strings.Contains(reason, "not been verified") {
		t.Fatalf("unverified calibration accepted: %q", reason)
	}
}

func TestAutoCutterGuards(t *testing.T) {
	measuredMid(t)
	a := &autoCutter{}
	a.setEnabled(true)
	st := midState(t, false, 128, 128)
	t0 := time.Unix(1000, 0)
	w, reason, ok := a.decide([]feedback.Candidate{ring(1250)}, 3, st, t0)
	if !ok || w.Source != studiolive.SourceAuto || !strings.HasPrefix(w.Summary, "AUTO ") {
		t.Fatalf("first ring not cut: %q %+v", reason, w)
	}
	if w.Changes[0].Address != "/channel/ch3/eqmidgain" || w.Changes[0].Raw >= 128 {
		t.Fatalf("gain must be cut first: %+v", w.Changes)
	}
	for _, c := range w.Changes {
		ctl, _ := studiolive.ParseAddress(c.Address)
		if ctl.Channel != "ch3" || !strings.HasPrefix(ctl.Name, "eqmid") {
			t.Fatalf("automatic cut touched %s", c.Address)
		}
	}
	if _, _, ok := a.decide([]feedback.Candidate{ring(2900)}, 3, st, t0.Add(5*time.Second)); ok {
		t.Fatal("second cut within 10 s")
	}
	if _, reason, ok := a.decide([]feedback.Candidate{ring(1260)}, 3, st, t0.Add(20*time.Second)); ok || !strings.Contains(reason, "persists") {
		t.Fatalf("same ring re-cut within 60 s: %q", reason)
	}
	inUse := midState(t, true, 50, 100)
	if _, reason, ok := a.decide([]feedback.Candidate{ring(2900)}, 3, inUse, t0.Add(30*time.Second)); ok || !strings.Contains(reason, "already cuts") {
		t.Fatalf("retuned a mid band already cutting elsewhere: %q", reason)
	}
	for i, hz := range []float64{2000, 500} {
		if _, reason, ok := a.decide([]feedback.Candidate{ring(hz)}, 3, st, t0.Add(time.Duration(40+20*i)*time.Second)); !ok {
			t.Fatalf("cut %d refused: %q", i+2, reason)
		}
	}
	if _, reason, ok := a.decide([]feedback.Candidate{ring(3000)}, 3, st, t0.Add(200*time.Second)); ok || !strings.Contains(reason, "operator attention") {
		t.Fatalf("more than %d cuts per run: %q", autoMaxCutsPerRun, reason)
	}
	a.reset()
	harm := ring(1250)
	harm.Harmonic = true
	weak := ring(1250)
	weak.Growing = false
	if _, _, ok := a.decide([]feedback.Candidate{harm, weak}, 3, st, t0.Add(300*time.Second)); ok {
		t.Fatal("harmonic or non-growing candidate cut automatically")
	}
	if _, reason, ok := a.decide([]feedback.Candidate{ring(8000)}, 3, st, t0.Add(300*time.Second)); ok || !strings.Contains(reason, "never automatic") {
		t.Fatalf("out-of-band ring: %q", reason)
	}
}

func TestAutoApplyRequiresAutoSource(t *testing.T) {
	c := &studiolive.BridgeClient{}
	if err := c.AutoApply(studiolive.WireProposal{
		Schema: studiolive.ProposalSchema, ID: "x", Source: studiolive.SourceRingOut,
		Changes: []studiolive.WireChange{{Address: "/channel/ch1/eqmidgain", Raw: 100}},
	}); err == nil {
		t.Fatal("non-auto proposal sent as automatic")
	}
}
