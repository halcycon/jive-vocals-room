package room

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratePinkIsDeterministicAndBandLimited(t *testing.T) {
	spec := testPinkSpec(7)
	a, err := GeneratePink(spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err := GeneratePink(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Samples) != spec.SampleRateHz*2 || a.SampleRate != spec.SampleRateHz {
		t.Fatalf("pcm meta: samples=%d rate=%d", len(a.Samples), a.SampleRate)
	}
	for i := range a.Samples {
		if a.Samples[i] != b.Samples[i] {
			t.Fatalf("seeded generator drifted at sample %d", i)
		}
	}
	other, err := GeneratePink(testPinkSpec(8))
	if err != nil {
		t.Fatal(err)
	}
	same := 0
	for i := range a.Samples {
		if a.Samples[i] == other.Samples[i] {
			same++
		}
	}
	if same > len(a.Samples)/10 {
		t.Fatal("different seeds produced nearly identical noise")
	}
	peak := 0.0
	for _, x := range a.Samples {
		peak = max(peak, math.Abs(x))
	}
	wantPeak := math.Pow(10, -spec.HeadroomDB/20)
	if peak > wantPeak+1e-3 {
		t.Fatalf("peak %v exceeds headroom target %v", peak, wantPeak)
	}
	if math.Abs(a.Samples[0]) > 1e-3 || math.Abs(a.Samples[len(a.Samples)-1]) > 1e-3 {
		t.Fatalf("fade did not bring ends near zero: start=%v end=%v", a.Samples[0], a.Samples[len(a.Samples)-1])
	}
	low, mid, high := bandEnergy(a, 10, 25), bandEnergy(a, 200, 800), bandEnergy(a, 18000, 22000)
	if low >= mid/6 {
		t.Fatalf("high-pass failed: low=%v mid=%v", low, mid)
	}
	if high >= mid/6 {
		t.Fatalf("low-pass failed: high=%v mid=%v", high, mid)
	}
}

func TestWritePinkWAVWritesSpecSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pink.wav")
	if _, err := WritePinkWAV(path, testPinkSpec(1)); err != nil {
		t.Fatal(err)
	}
	spec, err := LoadTestSignalSpec(path)
	if err != nil || spec == nil || spec.Seed != 1 {
		t.Fatalf("spec sidecar: spec=%+v err=%v", spec, err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() < 1000 {
		t.Fatalf("wav missing or tiny: %v", err)
	}
}

func testPinkSpec(seed int64) TestSignalSpec {
	spec := DefaultPinkSpec()
	spec.DurationSeconds = 2
	spec.Seed = seed
	return spec
}

func bandEnergy(p PCM, loHz, hiHz float64) float64 {
	frames := spectra(p.Samples, p.SampleRate)
	avg := averagePower(frames)
	var sum float64
	n := 0
	for i, v := range avg {
		f := binFrequency(i, p.SampleRate)
		if f >= loHz && f < hiHz {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
