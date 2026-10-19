package room

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
)

func noisePCM(t *testing.T, rate int, rmsDBFS float64, seed uint64) PCM {
	const seconds = 6.0
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, seed+1)) //nolint:gosec // deterministic test noise
	s := make([]float64, int(seconds*float64(rate)))
	sigma := math.Pow(10, rmsDBFS/20)
	for i := range s {
		s[i] = sigma * rng.NormFloat64()
	}
	return PCM{Samples: s, SampleRate: rate, Channels: 1, Source: "noise"}
}

func mustAnalyse(t *testing.T, p PCM) Measurement {
	t.Helper()
	m, err := Analyse(p)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func addPCM(a, b PCM) PCM {
	out := a
	out.Samples = make([]float64, len(a.Samples))
	for i := range a.Samples {
		out.Samples[i] = a.Samples[i] + b.Samples[i%len(b.Samples)]
	}
	return out
}

// The bench test on the StudioLive: test signal not reaching the mic, so the
// "measured" capture is the converter floor. It must not yield PA EQ.
func TestGatePARejectsNoiseOnlyCapture(t *testing.T) {
	ref := mustPink(t, 21)
	floor := noisePCM(t, ref.SampleRate, -114, 3)
	empty := mustAnalyse(t, noisePCM(t, ref.SampleRate, -114, 4))
	pa, err := AnalysePAResponse(ref, floor, DefaultResponseAnalysisConfig())
	if err != nil {
		t.Fatal(err)
	}
	GatePAResponse(&pa, mustAnalyse(t, floor), empty, StageEmpty)
	if pa.Status != paStatusInsufficientSignal || len(pa.Features) != 0 || pa.SignalToNoise == nil || pa.SignalToNoise.MedianDB > 3 {
		t.Fatalf("noise-only capture not rejected: status %s features %d snr %+v", pa.Status, len(pa.Features), pa.SignalToNoise)
	}
	for _, r := range Recommend(empty, nil, &pa, DefaultParametricMixer()) {
		if r.Kind == "eq_boost" || strings.Contains(r.Evidence, "PA") {
			t.Fatalf("PA recommendation from noise: %+v", r)
		}
	}
	md := Markdown(Session{PARoom: &pa}, nil)
	if !strings.Contains(md, "insufficient_signal") || !strings.Contains(md, "No transfer features") {
		t.Fatalf("report does not explain the rejection:\n%s", md)
	}
}

func TestConverterFloorEmptyRoomGivesNoRoomEQ(t *testing.T) {
	floor := mustAnalyse(t, noisePCM(t, 44100, -114, 7))
	floor.LegacyBands[2].LevelDB += 20 // a low-mid excess that would otherwise trigger a cut
	recs := Recommend(floor, nil, nil, DefaultParametricMixer())
	warned := false
	for _, r := range recs {
		if r.Kind == "eq_cut" || r.Kind == "eq_boost" {
			t.Fatalf("room EQ from converter floor: %+v", r)
		}
		if r.Kind == "warning" && strings.Contains(r.Reason, "converter floor") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no converter-floor warning: %+v", recs)
	}
}

func TestGatePAKeepsCleanMeasurement(t *testing.T) {
	ref := mustPink(t, 22)
	meas := filterPCM(ref, rbjPeaking(float64(ref.SampleRate), 250, 0.7, 6))
	empty := mustAnalyse(t, noisePCM(t, ref.SampleRate, -80, 5))
	pa := mustPA(t, ref, meas)
	before := len(pa.Features)
	GatePAResponse(&pa, mustAnalyse(t, meas), empty, StageEmpty)
	if pa.Status != paStatusMeasured || len(pa.Features) != before || pa.SignalToNoise.ExcludedFeatures != 0 || pa.SignalToNoise.UsableFraction < 0.99 {
		t.Fatalf("clean measurement altered: %+v", pa.SignalToNoise)
	}
	if !hasFeatureNear(pa.Features, FeatureBroadExcess, 250, 0.6) {
		t.Fatal("250 Hz excess lost")
	}
}

func TestGatePAExcludesFeaturesBuriedInRoomNoise(t *testing.T) {
	ref := mustPink(t, 23)
	shaped := filterPCM(filterPCM(ref, rbjPeaking(float64(ref.SampleRate), 250, 0.7, 6)), rbjPeaking(float64(ref.SampleRate), 3000, 0.7, 6))
	lp := rbjLowPass(float64(ref.SampleRate), 400, 0.707)
	rumble := filterPCM(filterPCM(noisePCM(t, ref.SampleRate, -6, 6), lp), lp)
	meas := addPCM(shaped, rumble)
	empty := mustAnalyse(t, rumble)
	pa := mustPA(t, ref, meas)
	GatePAResponse(&pa, mustAnalyse(t, meas), empty, StageEmpty)
	if pa.Status != paStatusMeasured || pa.SignalToNoise.ExcludedFeatures == 0 {
		t.Fatalf("expected low-frequency exclusions: status %s %+v", pa.Status, pa.SignalToNoise)
	}
	if hasFeatureNear(pa.Features, FeatureBroadExcess, 250, 0.4) {
		t.Fatal("a 250 Hz feature inside the rumble survived the gate")
	}
	if !hasFeatureNear(pa.Features, FeatureBroadExcess, 3000, 0.6) {
		t.Fatalf("3 kHz excess above the rumble was removed: %+v", featureKinds(pa.Features))
	}
}
