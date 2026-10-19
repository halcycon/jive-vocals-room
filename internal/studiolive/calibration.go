package studiolive

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

type Taper string

const (
	TaperLog    Taper = "log"
	TaperLinear Taper = "linear"
)

// CalPoint is one measured raw-to-unit pair.
type CalPoint struct {
	Raw   int     `json:"raw"`
	Value float64 `json:"value"`
}

// Calibration maps a continuous control's 0..1 value to an engineering unit.
// With Points it interpolates measured values (log-interpolated for log
// tapers); otherwise it uses the Min/Max taper model. Verified stays false
// until the mapping has been measured on the desk; apply logic must refuse
// unverified mappings.
type Calibration struct {
	Unit     string
	Taper    Taper
	Min, Max float64
	Points   []CalPoint
	Source   string
	Verified bool
}

// sourceSessionLabels: the end points and midpoint labels in
// StudioLive1602_OpenStageControl_v26.json. Each frequency midpoint equals the
// geometric mean of its end points, consistent with a log taper.
const sourceSessionLabels = "osc_session_v26_knob_labels"

// modelCalibration is the unverified knob-label model.
var modelCalibration = map[string]Calibration{
	"hpffreq":    {Unit: "Hz", Taper: TaperLog, Min: 24, Max: 1000, Source: sourceSessionLabels},
	"eqlowfreq":  {Unit: "Hz", Taper: TaperLog, Min: 36, Max: 465, Source: sourceSessionLabels},
	"eqmidfreq":  {Unit: "Hz", Taper: TaperLog, Min: 260, Max: 3500, Source: sourceSessionLabels},
	"eqhighfreq": {Unit: "Hz", Taper: TaperLog, Min: 1400, Max: 18000, Source: sourceSessionLabels},
	"eqlowgain":  {Unit: "dB", Taper: TaperLinear, Min: -15, Max: 15, Source: sourceSessionLabels},
	"eqmidgain":  {Unit: "dB", Taper: TaperLinear, Min: -15, Max: 15, Source: sourceSessionLabels},
	"eqhighgain": {Unit: "dB", Taper: TaperLinear, Min: -15, Max: 15, Source: sourceSessionLabels},
	"compthresh": {Unit: "dB", Taper: TaperLinear, Min: -56, Max: 0, Source: sourceSessionLabels},
}

// InputCalibration covers the input-channel controls with a known mapping:
// the model, overlaid by any loaded measurement. GEQ band gain is absent: its
// dB range is not yet known.
var InputCalibration = cloneCalibration(modelCalibration)

func cloneCalibration(m map[string]Calibration) map[string]Calibration {
	out := make(map[string]Calibration, len(m))
	for k, v := range m {
		v.Points = append([]CalPoint(nil), v.Points...)
		out[k] = v
	}
	return out
}

// ResetCalibration restores the unverified model (tests and reloads).
func ResetCalibration() { InputCalibration = cloneCalibration(modelCalibration) }

// CalibrationFor returns the mapping for a control, if one exists.
func CalibrationFor(c Control) (Calibration, bool) {
	if _, ok := InputChannelNumber(c.Channel); !ok {
		return Calibration{}, false
	}
	cal, ok := InputCalibration[c.Name]
	return cal, ok
}

func (c Calibration) logScale() bool { return c.Taper == TaperLog }

func (c Calibration) enc(x float64) float64 {
	if c.logScale() {
		return math.Log(x)
	}
	return x
}

func (c Calibration) dec(y float64) float64 {
	if c.logScale() {
		return math.Exp(y)
	}
	return y
}

func (c Calibration) ToUnit(v float64) float64 {
	v = math.Max(0, math.Min(1, v))
	if len(c.Points) >= 2 {
		r := v * 255
		p := c.Points
		i := sort.Search(len(p), func(i int) bool { return float64(p[i].Raw) >= r })
		i = max(1, min(len(p)-1, i))
		a, b := p[i-1], p[i]
		t := (r - float64(a.Raw)) / float64(b.Raw-a.Raw)
		return c.dec(c.enc(a.Value) + t*(c.enc(b.Value)-c.enc(a.Value)))
	}
	if c.logScale() {
		return c.Min * math.Pow(c.Max/c.Min, v)
	}
	return c.Min + v*(c.Max-c.Min)
}

// FromUnit returns the 0..1 value for x, clamped to the control's range.
func (c Calibration) FromUnit(x float64) float64 {
	if len(c.Points) >= 2 {
		p := c.Points
		x = math.Max(p[0].Value, math.Min(p[len(p)-1].Value, x))
		i := sort.Search(len(p), func(i int) bool { return p[i].Value >= x })
		i = max(1, min(len(p)-1, i))
		a, b := p[i-1], p[i]
		t := (c.enc(x) - c.enc(a.Value)) / (c.enc(b.Value) - c.enc(a.Value))
		return (float64(a.Raw) + t*float64(b.Raw-a.Raw)) / 255
	}
	x = math.Max(c.Min, math.Min(c.Max, x))
	if c.logScale() {
		return math.Log(x/c.Min) / math.Log(c.Max/c.Min)
	}
	return (x - c.Min) / (c.Max - c.Min)
}

// RawForUnit is the nearest 8-bit raw value for x.
func (c Calibration) RawForUnit(x float64) uint8 {
	return uint8(math.Round(math.Max(0, math.Min(1, c.FromUnit(x))) * 255))
}

// UnitStep is the engineering size of one raw step at raw r.
func (c Calibration) UnitStep(r uint8) float64 {
	if r == 255 {
		r = 254
	}
	return c.ToUnit(float64(r+1)/255) - c.ToUnit(float64(r)/255)
}

const CalibrationSchema = "jive-room-studiolive-calibration/v1"

// CalibrationFile is a measured calibration written by -studiolive-calibrate.
type CalibrationFile struct {
	Schema       string                `json:"schema_version"`
	CreatedAt    time.Time             `json:"created_at"`
	Channel      int                   `json:"measured_on_channel"`
	SampleRateHz int                   `json:"sample_rate_hz"`
	Method       string                `json:"method"`
	Controls     map[string][]CalPoint `json:"controls"`
	Notes        []string              `json:"notes,omitempty"`
}

// Validate checks schema, known controls, raw range, and strictly increasing
// raw and values, so interpolation and inversion are well defined.
func (f CalibrationFile) Validate() error {
	if f.Schema != CalibrationSchema {
		return fmt.Errorf("calibration schema %q is not %s", f.Schema, CalibrationSchema)
	}
	if len(f.Controls) == 0 {
		return fmt.Errorf("calibration has no controls")
	}
	for name, pts := range f.Controls {
		model, ok := modelCalibration[name]
		if !ok {
			return fmt.Errorf("calibration for unknown control %q", name)
		}
		if len(pts) < 2 {
			return fmt.Errorf("%s: need at least 2 points", name)
		}
		for i, p := range pts {
			if p.Raw < 0 || p.Raw > 255 || math.IsNaN(p.Value) || math.IsInf(p.Value, 0) || (model.logScale() && p.Value <= 0) {
				return fmt.Errorf("%s: invalid point %+v", name, p)
			}
			if i > 0 && (p.Raw <= pts[i-1].Raw || p.Value <= pts[i-1].Value) {
				return fmt.Errorf("%s: points must increase in both raw and value (at raw %d)", name, p.Raw)
			}
		}
	}
	return nil
}

// UseCalibration overlays measured points onto the model and marks those
// controls verified.
func UseCalibration(f CalibrationFile) error {
	if err := f.Validate(); err != nil {
		return err
	}
	next := cloneCalibration(modelCalibration)
	for name, pts := range f.Controls {
		c := next[name]
		c.Points = append([]CalPoint(nil), pts...)
		c.Min, c.Max = pts[0].Value, pts[len(pts)-1].Value
		c.Source = fmt.Sprintf("measured %s on ch%d", f.CreatedAt.Format("2006-01-02"), f.Channel)
		c.Verified = true
		next[name] = c
	}
	InputCalibration = next
	return nil
}

// LoadCalibration reads and applies a calibration file. A missing file is not
// an error: the unverified model stays in use.
func LoadCalibration(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var f CalibrationFile
	if err := json.Unmarshal(b, &f); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, UseCalibration(f)
}
