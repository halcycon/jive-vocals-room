package studiolive

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Kind is how osclive encodes a control on the wire.
type Kind int

const (
	KindUnknown Kind = iota
	// KindContinuous is an 8-bit nibble pair: value = raw/255.
	KindContinuous
	// KindSwitch is a bit: on when value >= 0.5.
	KindSwitch
)

// InputControls are osclive 0.3.0's per-channel controls (SL1602CtrlsIn_raw1394).
var InputControls = map[string]Kind{
	"gain": KindContinuous, "pan": KindContinuous, "linkedpan": KindContinuous,
	"aux1": KindContinuous, "aux2": KindContinuous, "aux3": KindContinuous, "aux4": KindContinuous,
	"fxa": KindContinuous, "fxb": KindContinuous,
	"hpf": KindSwitch, "hpffreq": KindContinuous,
	"gate": KindSwitch, "gatethresh": KindContinuous,
	"phantom": KindSwitch, "firewire": KindSwitch, "phase": KindSwitch, "post": KindSwitch,
	"mute": KindSwitch, "solo": KindSwitch,
	"comp": KindSwitch, "compauto": KindSwitch, "complimit": KindSwitch,
	"compthresh": KindContinuous, "compratio": KindContinuous, "compresponse": KindContinuous, "compgain": KindContinuous,
	"eqlow": KindSwitch, "eqmid": KindSwitch, "eqhigh": KindSwitch,
	"eqlowshelf": KindSwitch, "eqmidhiq": KindSwitch, "eqhighshelf": KindSwitch,
	"eqlowfreq": KindContinuous, "eqmidfreq": KindContinuous, "eqhighfreq": KindContinuous,
	"eqlowgain": KindContinuous, "eqmidgain": KindContinuous, "eqhighgain": KindContinuous,
	"toMain": KindSwitch,
}

// GEQBands are the main graphic EQ centres; osclive addresses band i as
// /channel/geq0/<i+1>.
var GEQBands = []float64{
	20, 25, 32, 40, 50, 63, 80, 100, 125, 160, 200, 250, 320, 400, 500, 640, 800,
	1000, 1300, 1600, 2000, 2500, 3200, 4000, 5000, 6400, 8000, 10000, 13000, 16000, 20000,
}

// proposable lists the only input-channel controls a recommendation may ever
// target. Everything else (faders, sends, routing, phantom, mute, solo,
// dynamics, toMain) is never automated.
var proposable = map[string]bool{
	"hpf": true, "hpffreq": true,
	"eqlow": true, "eqlowfreq": true, "eqlowgain": true, "eqlowshelf": true,
	"eqmid": true, "eqmidfreq": true, "eqmidgain": true, "eqmidhiq": true,
	"eqhigh": true, "eqhighfreq": true, "eqhighgain": true, "eqhighshelf": true,
}

// Control identifies one osclive control, e.g. {"ch3", "eqmidgain"} or {"geq0", "12"}.
type Control struct {
	Channel string
	Name    string
}

func (c Control) Address() string { return "/channel/" + c.Channel + "/" + c.Name }

func (c Control) String() string { return c.Channel + "/" + c.Name }

// ParseAddress accepts /channel/<channel>/<control>.
func ParseAddress(addr string) (Control, bool) {
	rest, ok := strings.CutPrefix(addr, "/channel/")
	if !ok {
		return Control{}, false
	}
	ch, name, ok := strings.Cut(rest, "/")
	if !ok || ch == "" || name == "" || strings.Contains(name, "/") {
		return Control{}, false
	}
	return Control{Channel: ch, Name: name}, true
}

func InputChannelName(n int) string { return "ch" + strconv.Itoa(n) }

// InputChannelNumber parses "ch1".."ch12".
func InputChannelNumber(ch string) (int, bool) {
	s, ok := strings.CutPrefix(ch, "ch")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > InputChannels {
		return 0, false
	}
	return n, true
}

// GEQBandControl returns the control for 0-based GEQ band index i.
func GEQBandControl(i int) (Control, error) {
	if i < 0 || i >= len(GEQBands) {
		return Control{}, fmt.Errorf("geq band index %d out of range", i)
	}
	return Control{Channel: "geq0", Name: strconv.Itoa(i + 1)}, nil
}

// GEQBandIndex returns the 0-based band index for a geq0 band control.
func GEQBandIndex(c Control) (int, bool) {
	if c.Channel != "geq0" {
		return 0, false
	}
	n, err := strconv.Atoi(c.Name)
	if err != nil || n < 1 || n > len(GEQBands) {
		return 0, false
	}
	return n - 1, true
}

func KindOf(c Control) Kind {
	if _, ok := InputChannelNumber(c.Channel); ok {
		return InputControls[c.Name]
	}
	if _, ok := GEQBandIndex(c); ok {
		return KindContinuous
	}
	if c.Channel == "geq0" && c.Name == "enable" {
		return KindSwitch
	}
	return KindUnknown
}

// Proposable reports whether a recommendation may ever target c: channel
// HPF/EQ on ch1–12 and the 31 main GEQ bands. GEQ enable and reset are excluded.
func Proposable(c Control) bool {
	if _, ok := InputChannelNumber(c.Channel); ok {
		return proposable[c.Name]
	}
	_, ok := GEQBandIndex(c)
	return ok
}

// RawFromValue recovers the 8-bit raw value osclive stores for a value, using
// osclive's own truncation (int(v*255)). Desk updates arrive as raw/255; the
// immediate echo of a write carries the float that was sent, e.g. the
// mid-step ValueForRaw(r), which must still read back as r. The epsilon
// absorbs float32 rounding just below an exact step.
func RawFromValue(v float64) uint8 {
	return uint8(math.Floor(math.Max(0, math.Min(1, v))*255 + 1e-6))
}

// ValueForRaw is the float32 to send so osclive stores exactly raw r.
// osclive writes int(min(max(v,0),1)*255), which truncates, so an in-between
// value such as 0.5 becomes raw 127. Aiming at the middle of the step leaves
// half a step of margin either side.
func ValueForRaw(r uint8) float32 {
	return float32((float64(r) + 0.5) / 255)
}

// RawStepsFromFlat is a GEQ band's offset from osclive's reset value
// (0.5, stored as raw 127). Its dB size is not calibrated.
func RawStepsFromFlat(raw uint8) int { return int(raw) - GEQFlatRaw }

const GEQFlatRaw = 127
