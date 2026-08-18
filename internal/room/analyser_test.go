package room

import (
	"math"
	"testing"
)

func TestAnalyseDetectsPersistentFiftyHertzHum(t *testing.T) {
	p := synthetic(48000, 3, func(i int, rate float64) float64 {
		return .08*math.Sin(2*math.Pi*50*float64(i)/rate) + .04*math.Sin(2*math.Pi*100*float64(i)/rate)
	})
	m, err := Analyse(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Hum == nil {
		t.Fatal("expected hum diagnostic")
	}
	if m.Hum.FundamentalHz != 50 {
		t.Fatalf("fundamental = %v", m.Hum.FundamentalHz)
	}
	if len(m.LegacyBands) != 15 {
		t.Fatalf("legacy bands = %d", len(m.LegacyBands))
	}
	if len(m.FineSpectrum) < 100 {
		t.Fatalf("fine bands = %d", len(m.FineSpectrum))
	}
}

func TestCompareReportsOccupiedBandIncrease(t *testing.T) {
	empty, _ := Analyse(synthetic(48000, 2, func(i int, r float64) float64 { return .001 * math.Sin(2*math.Pi*1000*float64(i)/r) }))
	occupied, _ := Analyse(synthetic(48000, 2, func(i int, r float64) float64 { return .01 * math.Sin(2*math.Pi*1000*float64(i)/r) }))
	c := Compare(empty, occupied)
	if c.BroadbandDeltaDB < 19.9 || c.BroadbandDeltaDB > 20.1 {
		t.Fatalf("delta=%v", c.BroadbandDeltaDB)
	}
	if len(c.BandDeltas) != 15 {
		t.Fatalf("deltas=%d", len(c.BandDeltas))
	}
}

func TestRecommendMapsCutToMixerAndDoesNotBoost(t *testing.T) {
	m, _ := Analyse(synthetic(48000, 2, func(i int, r float64) float64 {
		return .1*math.Sin(2*math.Pi*250*float64(i)/r) + .005*math.Sin(2*math.Pi*2000*float64(i)/r)
	}))
	mixer := MixerCapability{Kind: MixerFixed3, Bands: []MixerBand{{Name: "low", FixedFrequencyHz: 200, MinGainDB: -12, MaxGainDB: 6}, {Name: "mid", FixedFrequencyHz: 2500, MinGainDB: -12, MaxGainDB: 6}, {Name: "high", FixedFrequencyHz: 10000, MinGainDB: -12, MaxGainDB: 6}}}
	r := Recommend(m, nil, nil, mixer)
	found := false
	for _, x := range r {
		if x.GainDB > 0 {
			t.Fatalf("automatic boost: %+v", x)
		}
		if x.Kind == "eq_cut" {
			found = true
			if x.FrequencyHz != 200 {
				t.Fatalf("mapped frequency=%v", x.FrequencyHz)
			}
			if x.GainDB < -3 {
				t.Fatalf("cut exceeds bound: %v", x.GainDB)
			}
		}
	}
	if !found {
		t.Fatal("expected conservative low-mid cut")
	}
}

func TestAnalysePresenterUsesOccupiedNoiseOnSameAxis(t *testing.T) {
	noise := Measurement{BroadbandRMSDB: -40, LegacyBands: []SpectralPoint{{1000, -45}, {2250, -40}, {3350, -42}}}
	speech := Measurement{BroadbandRMSDB: -20, LegacyBands: []SpectralPoint{{1000, -25}, {2250, -20}, {3350, -22}}}
	got := AnalysePresenter(speech, noise)
	if got.OverallMarginDB != 20 || got.PresenceMarginDB != 20 {
		t.Fatalf("presenter margin: %+v", got)
	}
}

func synthetic(rate, seconds int, fn func(int, float64) float64) PCM {
	s := make([]float64, rate*seconds)
	for i := range s {
		s[i] = fn(i, float64(rate))
	}
	return PCM{Samples: s, SampleRate: rate, Channels: 1, Source: "synthetic"}
}
