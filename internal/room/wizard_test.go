package room

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureDurationNote(t *testing.T) {
	if CaptureDurationNote("empty_room", 12) != "" {
		t.Fatal("12 s should be inside the recommended window")
	}
	if CaptureDurationNote("empty_room", 2) == "" {
		t.Fatal("expected short-capture note")
	}
	if CaptureDurationNote("occupied_room", 40) == "" {
		t.Fatal("expected long-capture note")
	}
}

func TestLoadAndApplyCalibrationDoesNotClaimSPL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mic.json")
	if err := os.WriteFile(path, []byte(`{"points":[{"frequency_hz":100,"level_db":6},{"frequency_hz":1000,"level_db":0}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cal, err := LoadCalibrationCurve(path)
	if err != nil {
		t.Fatal(err)
	}
	if cal.SPLCalibrated || !cal.Applied {
		t.Fatalf("calibration flags: %+v", cal)
	}
	m := Measurement{FineSpectrum: []SpectralPoint{{FrequencyHz: 100, LevelDB: 0}, {FrequencyHz: 1000, LevelDB: 0}}}
	got := ApplyCalibration(m, cal)
	if got.FineSpectrum[0].LevelDB > -5 {
		t.Fatalf("100 Hz should be reduced by the +6 dB mic bump: %+v", got.FineSpectrum)
	}
}

func TestVerifyRecommendationsReportsDrop(t *testing.T) {
	before := Measurement{LegacyBands: []SpectralPoint{{FrequencyHz: 250, LevelDB: -20}}}
	after := Measurement{LegacyBands: []SpectralPoint{{FrequencyHz: 250, LevelDB: -26}}}
	notes := VerifyRecommendations(before, after, []Recommendation{{Kind: "eq_cut", FrequencyHz: 250, GainDB: -3}})
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "250") || !strings.Contains(joined, "lower") {
		t.Fatalf("expected improvement note: %v", notes)
	}
}

func TestRecommendPresenterHPFAndGainRisk(t *testing.T) {
	speech := Measurement{
		BroadbandRMSDB: -18,
		LegacyBands: []SpectralPoint{
			{FrequencyHz: 80, LevelDB: -10},
			{FrequencyHz: 125, LevelDB: -20},
			{FrequencyHz: 1000, LevelDB: -25},
			{FrequencyHz: 2250, LevelDB: -24},
		},
	}
	noise := Measurement{
		BroadbandRMSDB: -28,
		LegacyBands: []SpectralPoint{
			{FrequencyHz: 80, LevelDB: -30},
			{FrequencyHz: 125, LevelDB: -30},
			{FrequencyHz: 1000, LevelDB: -32},
			{FrequencyHz: 2250, LevelDB: -30},
		},
	}
	p := AnalysePresenter(speech, noise)
	if p.GainAdvice == "" {
		t.Fatal("expected gain advice")
	}
	s := Session{EmptyRoom: noise, Presenter: &p, Mixer: DefaultParametricMixer(), PARoom: &PAResponse{Status: paStatusMeasured, Features: []ResponseFeature{{Kind: FeatureNarrowResonance, FrequencyHz: 1000, MagnitudeDB: 8}}}}
	recs := RecommendSession(s)
	foundHPF, foundRisk := false, false
	for _, r := range recs {
		if r.Kind == "hpf" {
			foundHPF = true
		}
		if r.Evidence == GainAdviceRisky {
			foundRisk = true
		}
	}
	if !foundHPF || !foundRisk {
		t.Fatalf("expected HPF and risky-gain warning: %+v", recs)
	}
}
