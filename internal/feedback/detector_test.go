package feedback

import (
	"math"
	"math/rand/v2"
	"testing"
)

const rate = 44100

func db2amp(db float64) float64 { return math.Pow(10, db/20) }

// render produces seconds of audio from fn(t) plus white noise at noiseDBFS RMS.
func render(seconds, noiseDBFS float64, seed uint64, fn func(t float64) float64) []float64 {
	rng := rand.New(rand.NewPCG(seed, seed^0xabcdef)) //nolint:gosec // deterministic test noise
	n := int(seconds * rate)
	out := make([]float64, n)
	sigma := db2amp(noiseDBFS)
	for i := range out {
		t := float64(i) / rate
		out[i] = fn(t) + sigma*rng.NormFloat64()
	}
	return out
}

func run(t *testing.T, x []float64) []Candidate {
	t.Helper()
	d, err := New(rate)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(x); i += 1024 {
		d.Push(x[i:min(len(x), i+1024)])
	}
	return d.Candidates()
}

func ringing(cs []Candidate) []Candidate {
	var out []Candidate
	for _, c := range cs {
		if !c.Harmonic && c.Confidence >= 0.5 {
			out = append(out, c)
		}
	}
	return out
}

func TestGrowingRingIsDetected(t *testing.T) {
	x := render(2.5, -50, 1, func(tt float64) float64 {
		return db2amp(-40+15*tt) * math.Sin(2*math.Pi*1250*tt)
	})
	cs := ringing(run(t, x))
	if len(cs) != 1 {
		t.Fatalf("want one ring, got %+v", cs)
	}
	c := cs[0]
	if math.Abs(c.FrequencyHz-1250) > 2 || !c.Growing || c.GrowthDBPerSecond < 10 || c.Confidence < 0.8 {
		t.Fatalf("candidate %+v", c)
	}
	if c.LevelDBFS < -10 || c.LevelDBFS > 0 {
		t.Fatalf("sine-calibrated level %.1f dBFS, want about -3", c.LevelDBFS)
	}
}

func TestSustainedRingsAtTwoFrequencies(t *testing.T) {
	x := render(2, -55, 2, func(tt float64) float64 {
		return db2amp(-20)*math.Sin(2*math.Pi*800*tt) + db2amp(-26)*math.Sin(2*math.Pi*2900*tt)
	})
	cs := ringing(run(t, x))
	if len(cs) != 2 {
		t.Fatalf("want two rings, got %+v", cs)
	}
	for _, c := range cs {
		if c.Growing {
			t.Fatalf("steady ring marked growing: %+v", c)
		}
		if math.Abs(c.FrequencyHz-800) > 2 && math.Abs(c.FrequencyHz-2900) > 3 {
			t.Fatalf("unexpected frequency %+v", c)
		}
	}
}

func harmonicSeries(f0 func(t float64) float64, partials int, levelDB float64) func(float64) float64 {
	phase := make([]float64, partials+1)
	prev := 0.0
	return func(tt float64) float64 {
		dt := tt - prev
		prev = tt
		var s float64
		for h := 1; h <= partials; h++ {
			phase[h] += 2 * math.Pi * float64(h) * f0(tt) * dt
			s += db2amp(levelDB-3*float64(h-1)) * math.Sin(phase[h])
		}
		return s
	}
}

func TestGlidingVoicedSpeechIsNotRinging(t *testing.T) {
	x := render(2, -55, 3, harmonicSeries(func(tt float64) float64 {
		return 110 + 25*tt + 8*math.Sin(2*math.Pi*3*tt)
	}, 10, -20))
	if cs := run(t, x); len(cs) != 0 {
		t.Fatalf("gliding harmonic series produced candidates: %+v", cs)
	}
}

func TestSteadyHarmonicNoteIsDowngraded(t *testing.T) {
	x := render(2, -55, 4, harmonicSeries(func(float64) float64 { return 220 }, 6, -18))
	cs := run(t, x)
	if len(cs) == 0 {
		t.Fatal("a steady note should still be visible as a candidate")
	}
	if r := ringing(cs); len(r) != 0 {
		t.Fatalf("harmonic note treated as a ring: %+v", r)
	}
	for _, c := range cs {
		if !c.Harmonic || c.Confidence >= 0.5 {
			t.Fatalf("partial not downgraded: %+v", c)
		}
	}
}

func TestIdleConverterTonesAreBelowTheLevelGate(t *testing.T) {
	x := render(2, -116, 5, func(tt float64) float64 {
		return db2amp(-104)*math.Sin(2*math.Pi*rate/4*tt) + db2amp(-106)*math.Sin(2*math.Pi*rate/8*tt)
	})
	if cs := run(t, x); len(cs) != 0 {
		t.Fatalf("idle tones reported: %+v", cs)
	}
}

func TestShortBurstIsNotRinging(t *testing.T) {
	x := render(2, -55, 6, func(tt float64) float64 {
		if tt > 1.0 && tt < 1.3 {
			return db2amp(-15) * math.Sin(2*math.Pi*1600*tt)
		}
		return 0
	})
	d, _ := New(rate)
	worst := 0
	for i := 0; i < len(x); i += 1024 {
		d.Push(x[i:min(len(x), i+1024)])
		worst = max(worst, len(d.Candidates()))
	}
	if worst != 0 {
		t.Fatalf("a 0.3 s burst produced a candidate")
	}
}

func TestNewRejectsLowRate(t *testing.T) {
	if _, err := New(4000); err == nil {
		t.Fatal("4 kHz accepted")
	}
}
