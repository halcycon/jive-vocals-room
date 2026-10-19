package main

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/feedback"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

// Daemon-side guards for automatic cuts, on top of the bridge's policy.
const (
	autoMinConfidence      = 0.9
	autoMinDurationSeconds = 1.0
	autoMinIntervalSeconds = 10.0
	autoRecutSeconds       = 60.0
	autoMaxCutsPerRun      = 3
	autoSameRingOctaves    = 1.0 / 6
	autoBandInUseOctaves   = 1.0 / 3
	autoBandInUseCutDB     = -0.5
)

type autoCut struct {
	at time.Time
	hz float64
}

// autoCutter decides when a confident, growing ring justifies an automatic
// channel-mid cut. It never writes itself: it returns a proposal for the
// bridge's /dsp/auto/apply, which re-checks everything.
type autoCutter struct {
	mu      sync.Mutex
	enabled bool
	cuts    []autoCut
	last    string
}

func (a *autoCutter) setEnabled(on bool) {
	a.mu.Lock()
	a.enabled = on
	a.mu.Unlock()
}

func (a *autoCutter) isEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.enabled
}

// reset forgets this run's cuts (monitor restart or channel change).
func (a *autoCutter) reset() {
	a.mu.Lock()
	a.cuts, a.last = nil, ""
	a.mu.Unlock()
}

// decide returns a proposal to apply, or a reason why not (empty when there
// is simply nothing to do).
func (a *autoCutter) decide(cs []feedback.Candidate, channel int, st *studiolive.State, now time.Time) (studiolive.WireProposal, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.enabled || channel == 0 {
		return studiolive.WireProposal{}, "", false
	}
	var ring *feedback.Candidate
	for i := range cs {
		c := cs[i]
		if !c.Harmonic && c.Growing && c.Confidence >= autoMinConfidence && c.DurationSeconds >= autoMinDurationSeconds {
			ring = &cs[i]
			break
		}
	}
	if ring == nil {
		return studiolive.WireProposal{}, "", false
	}
	if len(a.cuts) >= autoMaxCutsPerRun {
		return studiolive.WireProposal{}, fmt.Sprintf("ring at %s: %d automatic cuts already made this run; operator attention needed", formatUnit(ring.FrequencyHz, "Hz"), autoMaxCutsPerRun), false
	}
	for _, c := range a.cuts {
		if now.Sub(c.at).Seconds() < autoMinIntervalSeconds {
			return studiolive.WireProposal{}, "", false
		}
		if math.Abs(math.Log2(c.hz/ring.FrequencyHz)) <= autoSameRingOctaves && now.Sub(c.at).Seconds() < autoRecutSeconds {
			return studiolive.WireProposal{}, fmt.Sprintf("ring at %s persists after an automatic cut; pull gain back", formatUnit(ring.FrequencyHz, "Hz")), false
		}
	}
	if st != nil {
		ch := studiolive.InputChannelName(channel)
		on, okOn := st.Get(studiolive.Control{Channel: ch, Name: "eqmid"})
		f, okF := st.Get(studiolive.Control{Channel: ch, Name: "eqmidfreq"})
		g, okG := st.Get(studiolive.Control{Channel: ch, Name: "eqmidgain"})
		if okOn && okF && okG && on.On() {
			fc := studiolive.InputCalibration["eqmidfreq"].ToUnit(float64(f.Raw) / 255)
			gain := studiolive.InputCalibration["eqmidgain"].ToUnit(float64(g.Raw) / 255)
			if gain <= autoBandInUseCutDB && math.Abs(math.Log2(fc/ring.FrequencyHz)) > autoBandInUseOctaves {
				return studiolive.WireProposal{}, fmt.Sprintf("ring at %s, but the mid band already cuts at %s; operator attention needed", formatUnit(ring.FrequencyHz, "Hz"), formatUnit(fc, "Hz")), false
			}
		}
	}
	for _, p := range studiolive.RingOutProposals(ring.FrequencyHz, channel, st) {
		if p.Target != studiolive.TargetChannelMid {
			continue
		}
		if p.CutDB >= 0 {
			return studiolive.WireProposal{}, fmt.Sprintf("ring at %s: mid gain already at the cut limit; pull gain back", formatUnit(ring.FrequencyHz, "Hz")), false
		}
		w, err := studiolive.ProposalForAuto(p, now)
		if err != nil {
			return studiolive.WireProposal{}, fmt.Sprintf("ring at %s: not cut automatically: %v", formatUnit(ring.FrequencyHz, "Hz"), err), false
		}
		a.cuts = append(a.cuts, autoCut{at: now, hz: ring.FrequencyHz})
		return w, "", true
	}
	return studiolive.WireProposal{}, fmt.Sprintf("ring at %s is outside the channel mid band; GEQ cuts are never automatic", formatUnit(ring.FrequencyHz, "Hz")), false
}

// note returns msg only when it differs from the last one, to avoid spam.
func (a *autoCutter) note(msg string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if msg == "" || msg == a.last {
		return "", false
	}
	a.last = msg
	return msg, true
}
