package studiolive

import (
	"fmt"
	"math"
)

const (
	// RingOutStepDB is the size of one proposed ring-out cut. Repeat only after
	// re-testing; never cut deeper than MaxRingOutCutDB per control.
	RingOutStepDB   = -3.0
	MaxRingOutCutDB = -6.0
	geqBandsPerOct  = 3.0
	// midBandHiQNote: the Hi-Q mid is the narrowest channel filter on the desk;
	// it is still far wider than a dedicated feedback notch.
	midBandHiQNote = "Hi-Q mid is the narrowest channel filter on this desk but still much wider than a notch"
)

type Target string

const (
	TargetGEQ        Target = "main_geq"
	TargetChannelMid Target = "channel_mid"
)

// ProposedControl is one control change in a proposal. Raw is nil when no
// calibration exists to map the engineering value to the desk.
type ProposedControl struct {
	Control    Control
	Value      float64
	Unit       string
	Raw        *uint8
	CurrentRaw *uint8
}

// Proposal is a bounded, explainable ring-out cut. It is never applied by
// jive-room; phase 4 will route operator-approved proposals through the bridge.
type Proposal struct {
	Target   Target
	CutDB    float64
	Controls []ProposedControl
	// Mappable is true only when every control has a raw value.
	Mappable bool
	// Verified is true only when every mapping is verified against the desk.
	Verified bool
	Summary  string
	Notes    []string
}

func current(st *State, c Control) *uint8 {
	if st == nil {
		return nil
	}
	if v, ok := st.Get(c); ok {
		return new(v.Raw)
	}
	return nil
}

// NearestGEQBand returns the band index closest to f on a log axis and the
// distance in octaves.
func NearestGEQBand(f float64) (int, float64) {
	best, bestOct := 0, math.Inf(1)
	for i, c := range GEQBands {
		if d := math.Abs(math.Log2(f / c)); d < bestOct {
			best, bestOct = i, d
		}
	}
	return best, bestOct
}

// RingOutProposals maps a ringing frequency to a main-GEQ cut and, when the
// frequency is in range, a channel Hi-Q mid cut. channel 0 skips the channel
// proposal. st may be nil when mixer state is unavailable.
func RingOutProposals(freqHz float64, channel int, st *State) []Proposal {
	out := []Proposal{geqProposal(freqHz, st)}
	if channel != 0 {
		if p, ok := channelMidProposal(freqHz, channel, st); ok {
			out = append(out, p)
		}
	}
	return out
}

func geqProposal(freqHz float64, st *State) Proposal {
	i, oct := NearestGEQBand(freqHz)
	c, _ := GEQBandControl(i)
	p := Proposal{
		Target: TargetGEQ,
		CutDB:  RingOutStepDB,
		Controls: []ProposedControl{{
			Control: c, Value: RingOutStepDB, Unit: "dB", CurrentRaw: current(st, c),
		}},
		Summary: fmt.Sprintf("GEQ %s band %+.0f dB", formatHz(GEQBands[i]), RingOutStepDB),
		Notes: []string{
			"GEQ dB per raw step is not calibrated, so no raw target is proposed yet",
			"pull gain back until the ring stops before cutting; jive-room never changes gain",
		},
	}
	if oct > 1/(2*geqBandsPerOct) {
		p.Notes = append(p.Notes, fmt.Sprintf("ring at %s lies between GEQ bands; the nearest is %s", formatHz(freqHz), formatHz(GEQBands[i])))
	}
	if st != nil {
		if v, ok := st.Get(Control{Channel: "geq0", Name: "enable"}); ok && !v.On() {
			p.Notes = append(p.Notes, "GEQ is off; a band cut has no effect until you enable it on the desk")
		}
		if cur := p.Controls[0].CurrentRaw; cur != nil && RawStepsFromFlat(*cur) < 0 {
			p.Notes = append(p.Notes, fmt.Sprintf("band already cut by %d raw steps", -RawStepsFromFlat(*cur)))
		}
	} else {
		p.Notes = append(p.Notes, "mixer state unavailable; current GEQ settings unknown")
	}
	return p
}

func channelMidProposal(freqHz float64, channel int, st *State) (Proposal, bool) {
	freqCal := InputCalibration["eqmidfreq"]
	gainCal := InputCalibration["eqmidgain"]
	if freqHz < freqCal.Min || freqHz > freqCal.Max {
		return Proposal{}, false
	}
	ch := InputChannelName(channel)
	ctl := func(name string) Control { return Control{Channel: ch, Name: name} }
	currentGain := 0.0
	notes := []string{midBandHiQNote, "channel EQ mapping uses the unverified session-label model"}
	if st != nil {
		if v, ok := st.Get(ctl("eqmidgain")); ok {
			currentGain = gainCal.ToUnit(v.Value)
		}
		on, okOn := st.Get(ctl("eqmid"))
		f, okF := st.Get(ctl("eqmidfreq"))
		if okOn && on.On() && okF && math.Abs(currentGain) >= 0.5 {
			if used := freqCal.ToUnit(f.Value); math.Abs(math.Log2(used/freqHz)) > 1.0/3 {
				notes = append(notes, fmt.Sprintf("mid band is already in use at %s (%+.1f dB); retuning it would undo that", formatHz(used), currentGain))
			}
		}
		if s, ok := st.Get(ctl("post")); ok && !s.On() {
			notes = append(notes, "FireWire send is pre-DSP, so this channel's capture will not show the cut; verify by ear or with a measurement mic")
		}
	} else {
		notes = append(notes, "mixer state unavailable; current channel EQ unknown")
	}
	target := math.Max(math.Round(currentGain)+RingOutStepDB, MaxRingOutCutDB)
	if currentGain <= MaxRingOutCutDB {
		notes = append(notes, fmt.Sprintf("mid gain is already %+.1f dB; no deeper ring-out cut is proposed", currentGain))
		target = currentGain
	}
	p := Proposal{
		Target: TargetChannelMid,
		CutDB:  target - currentGain,
		Controls: []ProposedControl{
			{Control: ctl("eqmid"), Value: 1, Unit: "switch", Raw: new(uint8(1)), CurrentRaw: current(st, ctl("eqmid"))},
			{Control: ctl("eqmidhiq"), Value: 1, Unit: "switch", Raw: new(uint8(1)), CurrentRaw: current(st, ctl("eqmidhiq"))},
			{Control: ctl("eqmidfreq"), Value: freqHz, Unit: "Hz", Raw: new(freqCal.RawForUnit(freqHz)), CurrentRaw: current(st, ctl("eqmidfreq"))},
			{Control: ctl("eqmidgain"), Value: target, Unit: "dB", Raw: new(gainCal.RawForUnit(target)), CurrentRaw: current(st, ctl("eqmidgain"))},
		},
		Mappable: true,
		Verified: freqCal.Verified && gainCal.Verified,
		Summary:  fmt.Sprintf("%s mid %s Hi-Q %+.0f dB", ch, formatHz(freqHz), target),
		Notes:    notes,
	}
	for _, c := range p.Controls {
		if !Proposable(c.Control) {
			panic("ring-out proposal targets a non-proposable control: " + c.Control.String())
		}
	}
	return p, true
}

func formatHz(f float64) string {
	if f >= 1000 {
		return fmt.Sprintf("%.2f kHz", f/1000)
	}
	return fmt.Sprintf("%.0f Hz", f)
}
