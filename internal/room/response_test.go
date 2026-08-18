package room

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestAnalysePAResponseRejectsMismatchedSampleRate(t *testing.T) {
	ref := mustPink(t, 1)
	meas := mustPink(t, 1)
	meas.SampleRate = 44100
	if _, err := AnalysePAResponse(ref, meas, DefaultResponseAnalysisConfig()); err == nil {
		t.Fatal("expected sample-rate mismatch")
	}
}

func TestAnalysePAResponseCutsBroadExcess(t *testing.T) {
	ref := mustPink(t, 2)
	meas := filterPCM(ref, rbjPeaking(float64(ref.SampleRate), 250, 0.7, 6))
	pa := mustPA(t, ref, meas)
	if !hasFeatureNear(pa.Features, FeatureBroadExcess, 250, 0.5) {
		t.Fatalf("missing broad excess: %+v", featureKinds(pa.Features))
	}
	recs := Recommend(Measurement{}, nil, &pa, DefaultParametricMixer())
	cut := findRec(recs, "eq_cut")
	if cut == nil {
		t.Fatalf("expected cut, got %+v", recs)
	}
	if cut.GainDB >= 0 || cut.GainDB < -maxAutoCutDB || !cut.StartingPoint {
		t.Fatalf("cut bounds: %+v", cut)
	}
	if cut.FrequencyHz < 80 || cut.FrequencyHz > 500 {
		t.Fatalf("cut not mapped through mixer low band: %v", cut.FrequencyHz)
	}
	if findRec(recs, "eq_boost") != nil {
		t.Fatalf("unexpected boost for an excess-only transfer: %+v", recs)
	}
}

func TestAnalysePAResponseFindsNarrowResonance(t *testing.T) {
	ref := mustPink(t, 3)
	meas := filterPCM(ref, rbjPeaking(float64(ref.SampleRate), 1000, 8, 8))
	pa := mustPA(t, ref, meas)
	if !hasFeatureNear(pa.Features, FeatureNarrowResonance, 1000, 0.25) && !hasFeatureNear(pa.Features, FeatureBroadExcess, 1000, 0.5) {
		t.Fatalf("missing resonance: %+v", featureKinds(pa.Features))
	}
	for _, rec := range Recommend(Measurement{}, nil, &pa, DefaultParametricMixer()) {
		if rec.GainDB > 0 {
			t.Fatalf("boosted a resonance: %+v", rec)
		}
	}
}

func TestAnalysePAResponseDoesNotBoostDeepNull(t *testing.T) {
	ref := mustPink(t, 4)
	meas := filterPCM(ref, rbjPeaking(float64(ref.SampleRate), 400, 10, -24))
	pa := mustPA(t, ref, meas)
	if !hasDoNotBoostNear(pa.Features, 400, 0.35) {
		t.Fatalf("expected do_not_boost null around 400 Hz: %+v", featureKinds(pa.Features))
	}
	for _, rec := range Recommend(Measurement{}, nil, &pa, DefaultParametricMixer()) {
		if rec.Kind == "eq_boost" && math.Abs(math.Log2(rec.FrequencyHz/400)) < 0.5 {
			t.Fatalf("boosted a deep null: %+v", rec)
		}
		if rec.GainDB > maxAutoBoostDB {
			t.Fatalf("boost exceeded +3 dB: %+v", rec)
		}
	}
}

func TestAnalysePAResponseMarksCombNotches(t *testing.T) {
	ref := mustPink(t, 5)
	meas := ref
	meas.Samples = feedforwardComb(append([]float64(nil), ref.Samples...), 120, 0.9)
	meas.Source = "comb"
	pa := mustPA(t, ref, meas)
	found := 0
	for _, f := range pa.Features {
		if f.Kind == FeatureCombNotch && f.DoNotBoost {
			found++
		}
	}
	if found < 2 {
		t.Fatalf("expected comb notches, got %+v", featureKinds(pa.Features))
	}
	for _, rec := range Recommend(Measurement{}, nil, &pa, DefaultParametricMixer()) {
		if rec.Kind == "eq_boost" {
			t.Fatalf("boosted comb region: %+v", rec)
		}
	}
}

func TestAnalysePAResponseSurvivesNoiseContamination(t *testing.T) {
	ref := mustPink(t, 6)
	meas := filterPCM(ref, rbjPeaking(float64(ref.SampleRate), 250, 0.7, 6))
	for i := range meas.Samples {
		meas.Samples[i] += 0.02 * math.Sin(2*math.Pi*float64(i)*1733/float64(meas.SampleRate))
	}
	pa := mustPA(t, ref, meas)
	if !hasFeatureNear(pa.Features, FeatureBroadExcess, 250, 0.6) {
		t.Fatalf("excess lost under contamination: %+v", featureKinds(pa.Features))
	}
}

func TestRecommendPABoundsBoostAndTotalGain(t *testing.T) {
	ref := mustPink(t, 9)
	meas := cascade(ref.Samples,
		rbjPeaking(float64(ref.SampleRate), 4000, 0.7, -8),
		rbjPeaking(float64(ref.SampleRate), 250, 0.7, -8),
	)
	filtered := ref
	filtered.Samples = meas
	pa := mustPA(t, ref, filtered)
	recs := Recommend(Measurement{}, nil, &pa, DefaultParametricMixer())
	var pos, abs float64
	for _, rec := range recs {
		if rec.Kind != "eq_boost" && rec.Kind != "eq_cut" {
			continue
		}
		if rec.GainDB > maxAutoBoostDB {
			t.Fatalf("boost %v exceeds +3 dB", rec.GainDB)
		}
		if rec.GainDB > 0 {
			pos += rec.GainDB
		}
		abs += math.Abs(rec.GainDB)
		if !rec.StartingPoint {
			t.Fatalf("recommendation missing starting_point: %+v", rec)
		}
	}
	if pos > maxTotalPositiveGainDB+1e-9 {
		t.Fatalf("total positive gain %v", pos)
	}
	if abs > maxTotalAbsCorrectionDB+1e-9 {
		t.Fatalf("total |correction| %v", abs)
	}
}

func TestPAMarkdownDistinguishesFactsFromAdvice(t *testing.T) {
	ref := mustPink(t, 10)
	meas := filterPCM(ref, rbjPeaking(float64(ref.SampleRate), 250, 0.7, 6))
	pa := mustPA(t, ref, meas)
	pa.TestSignal = &TestSignalSpec{Kind: pinkKind, SampleRateHz: 48000, DurationSeconds: 2, HeadroomDB: 12, Seed: 10, HighPassHz: 40, LowPassHz: 16000}
	md := Markdown(Session{PARoom: &pa, SuggestedEQ: Recommend(Measurement{}, nil, &pa, DefaultParametricMixer())}, nil)
	if !strings.Contains(md, "## PA / room response") || !strings.Contains(md, "not an SPL calibration") {
		t.Fatalf("markdown missing PA facts:\n%s", md)
	}
	if strings.Contains(md, "flat room") || strings.Contains(md, "zero latency") {
		t.Fatal("unsupported claim in markdown")
	}
}

func mustPink(t *testing.T, seed int64) PCM {
	t.Helper()
	p, err := GeneratePink(testPinkSpec(seed))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustPA(t *testing.T, ref, meas PCM) PAResponse {
	t.Helper()
	pa, err := AnalysePAResponse(ref, meas, DefaultResponseAnalysisConfig())
	if err != nil {
		t.Fatal(err)
	}
	if pa.Status != paStatusMeasured || len(pa.TransferSmoothedDB) == 0 || len(pa.TransferFineDB) == 0 {
		t.Fatalf("incomplete PA response: %+v", pa)
	}
	return pa
}

func filterPCM(p PCM, f biquad) PCM {
	out := p
	out.Samples = f.apply(p.Samples)
	out.Source = "filtered"
	return out
}

func hasFeatureNear(features []ResponseFeature, kind ResponseFeatureKind, hz, oct float64) bool {
	for _, f := range features {
		if f.Kind == kind && math.Abs(math.Log2(f.FrequencyHz/hz)) <= oct {
			return true
		}
	}
	return false
}

func hasDoNotBoostNear(features []ResponseFeature, hz, oct float64) bool {
	for _, f := range features {
		if f.DoNotBoost && math.Abs(math.Log2(f.FrequencyHz/hz)) <= oct {
			return true
		}
	}
	return false
}

func findRec(recs []Recommendation, kind string) *Recommendation {
	for i := range recs {
		if recs[i].Kind == kind {
			return &recs[i]
		}
	}
	return nil
}

func featureKinds(features []ResponseFeature) string {
	parts := make([]string, len(features))
	for i, f := range features {
		parts[i] = fmt.Sprintf("%s@%.0fHz (%+.1fdB do_not_boost=%t)", f.Kind, f.FrequencyHz, f.MagnitudeDB, f.DoNotBoost)
	}
	return strings.Join(parts, ", ")
}
