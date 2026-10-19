package studiolive

import "math"

type Taper string

const (
	TaperLog    Taper = "log"
	TaperLinear Taper = "linear"
)

// Calibration maps a continuous control's 0..1 value to an engineering unit.
// Verified stays false until the mapping has been checked against the desk;
// apply logic must refuse unverified mappings.
type Calibration struct {
	Unit     string
	Taper    Taper
	Min, Max float64
	Source   string
	Verified bool
}

// sourceSessionLabels: the end points and midpoint labels in
// StudioLive1602_OpenStageControl_v26.json. Each frequency midpoint equals the
// geometric mean of its end points, consistent with a log taper.
const sourceSessionLabels = "osc_session_v26_knob_labels"

// InputCalibration covers the input-channel controls with a known model.
// GEQ band gain is absent: its dB range is not yet known.
var InputCalibration = map[string]Calibration{
	"hpffreq":    {Unit: "Hz", Taper: TaperLog, Min: 24, Max: 1000, Source: sourceSessionLabels},
	"eqlowfreq":  {Unit: "Hz", Taper: TaperLog, Min: 36, Max: 465, Source: sourceSessionLabels},
	"eqmidfreq":  {Unit: "Hz", Taper: TaperLog, Min: 260, Max: 3500, Source: sourceSessionLabels},
	"eqhighfreq": {Unit: "Hz", Taper: TaperLog, Min: 1400, Max: 18000, Source: sourceSessionLabels},
	"eqlowgain":  {Unit: "dB", Taper: TaperLinear, Min: -15, Max: 15, Source: sourceSessionLabels},
	"eqmidgain":  {Unit: "dB", Taper: TaperLinear, Min: -15, Max: 15, Source: sourceSessionLabels},
	"eqhighgain": {Unit: "dB", Taper: TaperLinear, Min: -15, Max: 15, Source: sourceSessionLabels},
	"compthresh": {Unit: "dB", Taper: TaperLinear, Min: -56, Max: 0, Source: sourceSessionLabels},
}

// CalibrationFor returns the model for a control, if one exists.
func CalibrationFor(c Control) (Calibration, bool) {
	if _, ok := InputChannelNumber(c.Channel); !ok {
		return Calibration{}, false
	}
	cal, ok := InputCalibration[c.Name]
	return cal, ok
}

func (c Calibration) ToUnit(v float64) float64 {
	v = math.Max(0, math.Min(1, v))
	if c.Taper == TaperLog {
		return c.Min * math.Pow(c.Max/c.Min, v)
	}
	return c.Min + v*(c.Max-c.Min)
}

// FromUnit returns the 0..1 value for x, clamped to the control's range.
func (c Calibration) FromUnit(x float64) float64 {
	x = math.Max(c.Min, math.Min(c.Max, x))
	if c.Taper == TaperLog {
		return math.Log(x/c.Min) / math.Log(c.Max/c.Min)
	}
	return (x - c.Min) / (c.Max - c.Min)
}

// RawForUnit is the nearest 8-bit raw value for x.
func (c Calibration) RawForUnit(x float64) uint8 { return RawFromValue(c.FromUnit(x)) }

// UnitStep is the engineering size of one raw step at raw r.
func (c Calibration) UnitStep(r uint8) float64 {
	if r == 255 {
		r = 254
	}
	return c.ToUnit(float64(r+1)/255) - c.ToUnit(float64(r)/255)
}
