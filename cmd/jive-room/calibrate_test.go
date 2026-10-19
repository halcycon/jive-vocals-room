package main

import (
	"fmt"
	"math"
	"testing"

	"github.com/linuxmatters/jive-vocals/internal/room"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

const calRate = 44100

func calPink(t *testing.T) []float64 {
	t.Helper()
	spec := room.DefaultPinkSpec()
	spec.SampleRateHz, spec.DurationSeconds, spec.HeadroomDB, spec.HighPassHz, spec.LowPassHz = calRate, 3, 20, 20, 20000
	p, err := room.GeneratePink(spec)
	if err != nil {
		t.Fatal(err)
	}
	return p.Samples
}

// biquad applies an RBJ peaking (gainDB != 0) or 2nd-order high-pass (hpf) filter.
func biquad(x []float64, f0, q, gainDB float64, hpf bool) []float64 {
	w0 := 2 * math.Pi * f0 / calRate
	alpha := math.Sin(w0) / (2 * q)
	cw := math.Cos(w0)
	var b0, b1, b2, a0, a1, a2 float64
	if hpf {
		b0, b1, b2 = (1+cw)/2, -(1 + cw), (1+cw)/2
		a0, a1, a2 = 1+alpha, -2*cw, 1-alpha
	} else {
		A := math.Pow(10, gainDB/40)
		b0, b1, b2 = 1+alpha*A, -2*cw, 1-alpha*A
		a0, a1, a2 = 1+alpha/A, -2*cw, 1-alpha/A
	}
	out := make([]float64, len(x))
	var x1, x2, y1, y2 float64
	for i, v := range x {
		y := (b0*v + b1*x1 + b2*x2 - a1*y1 - a2*y2) / a0
		x2, x1, y2, y1 = x1, v, y1, y
		out[i] = y
	}
	return out
}

func effectOf(t *testing.T, ref, meas []float64) calMeasurement {
	t.Helper()
	c, flat := transferDB(ref, ref, calRate)
	_, g := transferDB(ref, meas, calRate)
	return calMeasurement{centres: c, effect: subtract(g, flat)}
}

func TestTransferFindsPeakAndGain(t *testing.T) {
	ref := calPink(t)
	for _, tc := range []struct{ f0, gain float64 }{{1000, 12}, {250, 9}, {6000, 12}, {1000, -9}} {
		m := effectOf(t, ref, biquad(ref, tc.f0, 1.0, tc.gain, false))
		f, g := peakOf(m.centres, m.effect, math.Copysign(1, tc.gain))
		if math.Abs(math.Log2(f/tc.f0)) > 1.0/24 || math.Abs(g-tc.gain) > 1.0 {
			t.Fatalf("peak %v Hz %+v dB: measured %.0f Hz %+.1f dB", tc.f0, tc.gain, f, g)
		}
	}
}

func TestHPFCorner(t *testing.T) {
	ref := calPink(t)
	for _, fc := range []float64{60, 120, 300} {
		m := effectOf(t, ref, biquad(ref, fc, math.Sqrt2/2, 0, true))
		got, ok := hpfCorner(m.centres, m.effect)
		if !ok || math.Abs(math.Log2(got/fc)) > 1.0/12 {
			t.Fatalf("HPF %v Hz: corner %v %v", fc, got, ok)
		}
	}
}

func TestFitCalibrationFromSyntheticSweeps(t *testing.T) {
	ref := calPink(t)
	m := map[string]calMeasurement{}
	freqFor := func(r int) float64 { return 260 * math.Pow(3500.0/260, float64(r)/255) }
	gainFor := func(r int) float64 { return -15 + 30*float64(r)/255 }
	for _, r := range calSweepRaws {
		m[fmt.Sprintf("eqmidfreq=%d", r)] = effectOf(t, ref, biquad(ref, freqFor(r), 1.5, 15, false))
		if g := gainFor(r); math.Abs(g) > 0.05 {
			m[fmt.Sprintf("eqmidgain=%d", r)] = effectOf(t, ref, biquad(ref, freqFor(128), 1.5, g, false))
		} else {
			m[fmt.Sprintf("eqmidgain=%d", r)] = effectOf(t, ref, ref)
		}
	}
	pts, _ := fitCalibration(m)
	f := pts["eqmidfreq"]
	g := pts["eqmidgain"]
	if len(f) != len(calSweepRaws) || len(g) != len(calSweepRaws) {
		t.Fatalf("points: freq %d gain %d", len(f), len(g))
	}
	for _, p := range f {
		if math.Abs(math.Log2(p.Value/freqFor(p.Raw))) > 1.0/12 {
			t.Fatalf("freq raw %d: %.0f Hz vs %.0f Hz", p.Raw, p.Value, freqFor(p.Raw))
		}
	}
	for _, p := range g {
		if math.Abs(p.Value-gainFor(p.Raw)) > 1.0 {
			t.Fatalf("gain raw %d: %+.1f dB vs %+.1f dB", p.Raw, p.Value, gainFor(p.Raw))
		}
	}
	file := studiolive.CalibrationFile{Schema: studiolive.CalibrationSchema, Channel: 8, Controls: pts}
	if err := file.Validate(); err != nil {
		t.Fatal(err)
	}
	defer studiolive.ResetCalibration()
	if err := studiolive.UseCalibration(file); err != nil {
		t.Fatal(err)
	}
	cal := studiolive.InputCalibration["eqmidgain"]
	if !cal.Verified || math.Abs(cal.ToUnit(float64(cal.RawForUnit(-3))/255)+3) > 0.5 {
		t.Fatalf("measured mid gain mapping: %+v", cal)
	}
	if studiolive.InputCalibration["eqlowgain"].Verified {
		t.Fatal("unmeasured control marked verified")
	}
}

func TestFitRejectsNonMonotonic(t *testing.T) {
	ref := calPink(t)
	m := map[string]calMeasurement{}
	for _, r := range calSweepRaws {
		m[fmt.Sprintf("eqhighfreq=%d", r)] = effectOf(t, ref, biquad(ref, 2000, 1.5, 15, false))
	}
	pts, notes := fitCalibration(m)
	if _, ok := pts["eqhighfreq"]; ok || len(notes) == 0 {
		t.Fatalf("flat frequency sweep accepted: %v %v", pts, notes)
	}
}
