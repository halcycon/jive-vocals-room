package studiolive

import (
	"strings"
	"testing"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/osc"
)

func stateWith(t *testing.T, vals map[string]any) *State {
	t.Helper()
	s := NewState()
	for addr, v := range vals {
		s.Apply(osc.Message{Address: addr, Args: []any{v}}, time.Unix(0, 0))
	}
	return s
}

func notesContain(p Proposal, sub string) bool {
	for _, n := range p.Notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func TestNearestGEQBand(t *testing.T) {
	i, oct := NearestGEQBand(1250)
	if GEQBands[i] != 1300 || oct > 0.06 {
		t.Fatalf("1250 Hz -> %v (%.3f oct)", GEQBands[i], oct)
	}
}

func TestRingOutProposalsAreBoundedCuts(t *testing.T) {
	st := stateWith(t, map[string]any{
		"/channel/geq0/enable":   int32(0),
		"/channel/ch3/eqmid":     int32(0),
		"/channel/ch3/eqmidgain": float32(128.0 / 255),
		"/channel/ch3/post":      int32(0),
	})
	ps := RingOutProposals(1250, 3, st)
	if len(ps) != 2 {
		t.Fatalf("got %d proposals", len(ps))
	}
	geq, mid := ps[0], ps[1]
	if geq.Target != TargetGEQ || geq.CutDB != RingOutStepDB || geq.Mappable || geq.Controls[0].Raw != nil {
		t.Fatalf("geq %+v", geq)
	}
	if geq.Controls[0].Control.Address() != "/channel/geq0/19" {
		t.Fatalf("geq control %v", geq.Controls[0].Control)
	}
	if !notesContain(geq, "GEQ is off") {
		t.Fatalf("missing GEQ-off note: %v", geq.Notes)
	}
	if mid.Target != TargetChannelMid || mid.CutDB > 0 || mid.CutDB < RingOutStepDB-0.5 || !mid.Mappable || mid.Verified {
		t.Fatalf("mid %+v", mid)
	}
	if !notesContain(mid, "pre-DSP") {
		t.Fatalf("missing pre-DSP note: %v", mid.Notes)
	}
	for _, p := range ps {
		for _, c := range p.Controls {
			if !Proposable(c.Control) {
				t.Fatalf("non-proposable control %v", c.Control)
			}
			if c.Unit == "dB" && c.Value > 0 {
				t.Fatalf("boost proposed: %+v", c)
			}
		}
	}
}

func TestRingOutNeverCutsBeyondCap(t *testing.T) {
	cal := InputCalibration["eqmidgain"]
	st := stateWith(t, map[string]any{
		"/channel/ch3/eqmid":     int32(1),
		"/channel/ch3/eqmidfreq": float32(cal.FromUnit(1)),
		"/channel/ch3/eqmidgain": float32(cal.FromUnit(-5)),
	})
	mid := RingOutProposals(1250, 3, st)[1]
	got := mid.Controls[3].Value
	if got < MaxRingOutCutDB-0.01 {
		t.Fatalf("target %.1f dB exceeds cap", got)
	}
	st = stateWith(t, map[string]any{"/channel/ch3/eqmidgain": float32(cal.FromUnit(-9))})
	mid = RingOutProposals(1250, 3, st)[1]
	if mid.CutDB != 0 || !notesContain(mid, "no deeper") {
		t.Fatalf("cut beyond cap proposed: %+v", mid)
	}
}

func TestRingOutOutOfMidRangeAndConflicts(t *testing.T) {
	if ps := RingOutProposals(6000, 3, nil); len(ps) != 1 || ps[0].Target != TargetGEQ {
		t.Fatalf("6 kHz should only propose GEQ: %+v", ps)
	}
	if ps := RingOutProposals(1250, 0, nil); len(ps) != 1 {
		t.Fatalf("channel 0 should skip the channel proposal: %+v", ps)
	}
	cal := InputCalibration["eqmidfreq"]
	st := stateWith(t, map[string]any{
		"/channel/ch2/eqmid":     int32(1),
		"/channel/ch2/eqmidfreq": float32(cal.FromUnit(400)),
		"/channel/ch2/eqmidgain": float32(InputCalibration["eqmidgain"].FromUnit(-4)),
	})
	if mid := RingOutProposals(2000, 2, st)[1]; !notesContain(mid, "already in use") {
		t.Fatalf("conflict note missing: %v", mid.Notes)
	}
}
