package liverec

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
)

const rate = 44100

// voice synthesises a voiced, syllabic signal: a harmonic series on a gliding
// f0 with a -6 dB/oct roll-off, amplitude-modulated at about 4 Hz with pauses,
// plus optional extras added per sample.
type voice struct {
	levelDB float64
	extra   func(t float64, rng *rand.Rand) float64
}

func (v voice) render(seconds float64, seed uint64) []float64 {
	rng := rand.New(rand.NewPCG(seed, seed^7)) //nolint:gosec // deterministic test signal
	n := int(seconds * rate)
	out := make([]float64, n)
	phase := make([]float64, 41)
	amp := math.Pow(10, v.levelDB/20)
	for i := range out {
		t := float64(i) / rate
		f0 := 140 + 25*math.Sin(2*math.Pi*0.7*t)
		env := math.Max(0, math.Sin(2*math.Pi*2*t))
		var s float64
		for h := 1; h <= 40 && float64(h)*f0 < 9000; h++ {
			phase[h] += 2 * math.Pi * float64(h) * f0 / rate
			s += math.Sin(phase[h]) / float64(h)
		}
		out[i] = amp*env*s/3 + 1e-4*rng.NormFloat64()
		if v.extra != nil {
			out[i] += v.extra(t, rng)
		}
	}
	return out
}

func newRec(t *testing.T) *Recommender {
	t.Helper()
	r, err := New(rate)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// run pushes x in one-second steps, evaluating each second as the daemon does,
// and returns the final recommendations.
func run(t *testing.T, x []float64, desk Desk) ([]Recommendation, *Recommender) {
	r := newRec(t)
	var recs []Recommendation
	for i := 0; i < len(x); i += rate {
		r.Push(x[i:min(len(x), i+rate)])
		recs = r.Recommendations(desk)
	}
	return recs, r
}

func has(recs []Recommendation, k Kind) *Recommendation {
	for i := range recs {
		if recs[i].Kind == k {
			return &recs[i]
		}
	}
	return nil
}

func TestCleanVoiceGivesNoAdviceAndNeedsSignal(t *testing.T) {
	recs, r := run(t, voice{levelDB: -18}.render(25, 1), Desk{})
	if !r.Status().Ready {
		t.Fatalf("status %+v", r.Status())
	}
	for _, rec := range recs {
		if rec.Kind != KindSibilance || !strings.Contains(rec.Summary, "noticeable") {
			t.Fatalf("clean voice produced advice: %+v", recs)
		}
	}
	silent, r2 := run(t, make([]float64, 20*rate), Desk{})
	if len(silent) != 0 || r2.Status().Ready {
		t.Fatalf("silence produced advice or readiness: %+v %+v", silent, r2.Status())
	}
}

func TestRumbleSuggestsHPFUnlessDeskHasOne(t *testing.T) {
	rumble := voice{levelDB: -18, extra: func(tt float64, _ *rand.Rand) float64 {
		return 0.08 * math.Sin(2*math.Pi*55*tt)
	}}
	x := rumble.render(25, 2)
	recs, _ := run(t, x, Desk{})
	if has(recs, KindHPF) == nil {
		t.Fatalf("no HPF advice for 55 Hz rumble: %+v", recs)
	}
	recs, _ = run(t, x, Desk{HPFHz: func() (float64, bool) { return 100, true }})
	if has(recs, KindHPF) != nil {
		t.Fatal("HPF advice repeated although the desk HPF is on at 100 Hz")
	}
}

func TestResonanceIsLocated(t *testing.T) {
	ring := voice{levelDB: -18}
	base := ring.render(25, 3)
	// Emphasise ~1 kHz with a narrow resonant boost (second-order peak).
	out := make([]float64, len(base))
	w0 := 2 * math.Pi * 1000 / rate
	alpha := math.Sin(w0) / (2 * 4.0)
	A := math.Pow(10, 10.0/40)
	b0, b1, b2 := 1+alpha*A, -2*math.Cos(w0), 1-alpha*A
	a0, a1, a2 := 1+alpha/A, -2*math.Cos(w0), 1-alpha/A
	var x1, x2, y1, y2 float64
	for i, x := range base {
		y := (b0*x + b1*x1 + b2*x2 - a1*y1 - a2*y2) / a0
		x2, x1, y2, y1 = x1, x, y1, y
		out[i] = y
	}
	recs, _ := run(t, out, Desk{})
	res := has(recs, KindResonance)
	if res == nil || res.FrequencyHz < 800 || res.FrequencyHz > 1250 {
		t.Fatalf("resonance near 1 kHz not found: %+v", recs)
	}
	recs, _ = run(t, out, Desk{CutNear: func(f float64) bool { return math.Abs(math.Log2(f/1000)) < 1.0/3 }})
	if has(recs, KindResonance) != nil {
		t.Fatal("resonance advice repeated although the desk already cuts there")
	}
}

func TestStrongSibilance(t *testing.T) {
	sib := voice{levelDB: -18, extra: func(tt float64, rng *rand.Rand) float64 {
		if math.Mod(tt, 1.0) < 0.12 {
			return 0.15 * rng.NormFloat64() * math.Sin(2*math.Pi*7000*tt)
		}
		return 0
	}}
	recs, _ := run(t, sib.render(25, 4), Desk{})
	s := has(recs, KindSibilance)
	if s == nil || !strings.Contains(s.Summary, "strong") {
		t.Fatalf("strong sibilance not reported: %+v", recs)
	}
}

func TestClippingAndLowLevel(t *testing.T) {
	hot := voice{levelDB: 6}.render(25, 5)
	for i := range hot {
		hot[i] = math.Max(-1, math.Min(1, hot[i]))
	}
	recs, _ := run(t, hot, Desk{})
	if has(recs, KindClipping) == nil {
		t.Fatalf("clipping not reported: %+v", recs)
	}
	recs, _ = run(t, voice{levelDB: -55}.render(25, 6), Desk{})
	if has(recs, KindLowLevel) == nil {
		t.Fatalf("low level not reported: %+v", recs)
	}
}

func TestHoldPreventsFlicker(t *testing.T) {
	rumble := voice{levelDB: -18, extra: func(tt float64, _ *rand.Rand) float64 { return 0.08 * math.Sin(2*math.Pi*55*tt) }}
	r := newRec(t)
	x := rumble.render(8, 7)
	for i := 0; i < len(x); i += rate {
		r.Push(x[i:min(len(x), i+rate)])
		if recs := r.Recommendations(Desk{}); has(recs, KindHPF) != nil {
			t.Fatalf("advice shown after only %.0f s", float64(i)/rate)
		}
	}
}

func TestNewRejectsLowRate(t *testing.T) {
	if _, err := New(16000); err == nil {
		t.Fatal("16 kHz accepted")
	}
}
