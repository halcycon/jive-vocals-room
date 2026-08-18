package room

type MixerKind string

const (
	MixerFixed3     MixerKind = "fixed_3_band"
	MixerSemiParam4 MixerKind = "semi_parametric_4_band"
	MixerGraphic    MixerKind = "graphic_eq"
	MixerParametric MixerKind = "parametric_eq"
)

type MixerBand struct {
	Name             string  `json:"name"`
	FixedFrequencyHz float64 `json:"fixed_frequency_hz,omitempty"`
	MinFrequencyHz   float64 `json:"min_frequency_hz,omitempty"`
	MaxFrequencyHz   float64 `json:"max_frequency_hz,omitempty"`
	MinGainDB        float64 `json:"min_gain_db"`
	MaxGainDB        float64 `json:"max_gain_db"`
	Q                float64 `json:"q,omitempty"`
}

type MixerCapability struct {
	Kind              MixerKind   `json:"kind"`
	Bands             []MixerBand `json:"bands"`
	HasHighPass       bool        `json:"has_high_pass"`
	HighPassChoicesHz []float64   `json:"high_pass_choices_hz,omitempty"`
}

func FixedThreeBandMixer(lowHz, midHz, highHz float64) MixerCapability {
	return MixerCapability{Kind: MixerFixed3, Bands: []MixerBand{
		{Name: "low", FixedFrequencyHz: lowHz, MinGainDB: -12, MaxGainDB: 6},
		{Name: "mid", FixedFrequencyHz: midHz, MinGainDB: -12, MaxGainDB: 6},
		{Name: "high", FixedFrequencyHz: highHz, MinGainDB: -12, MaxGainDB: 6},
	}}
}

func SemiParametricFourBandMixer() MixerCapability {
	return MixerCapability{Kind: MixerSemiParam4, Bands: []MixerBand{
		{Name: "low", FixedFrequencyHz: 100, MinGainDB: -12, MaxGainDB: 6},
		{Name: "low_mid", MinFrequencyHz: 150, MaxFrequencyHz: 2000, MinGainDB: -12, MaxGainDB: 6, Q: 1},
		{Name: "high_mid", MinFrequencyHz: 500, MaxFrequencyHz: 8000, MinGainDB: -12, MaxGainDB: 6, Q: 1},
		{Name: "high", FixedFrequencyHz: 10000, MinGainDB: -12, MaxGainDB: 6},
	}}
}

func GraphicMixer(centresHz []float64) MixerCapability {
	bands := make([]MixerBand, len(centresHz))
	for i, frequencyHz := range centresHz {
		bands[i] = MixerBand{Name: "graphic", FixedFrequencyHz: frequencyHz, MinGainDB: -12, MaxGainDB: 6}
	}
	return MixerCapability{Kind: MixerGraphic, Bands: bands}
}

func DefaultParametricMixer() MixerCapability {
	return MixerCapability{Kind: MixerParametric, HasHighPass: true, HighPassChoicesHz: []float64{80, 100}, Bands: []MixerBand{
		{Name: "low", MinFrequencyHz: 80, MaxFrequencyHz: 500, MinGainDB: -12, MaxGainDB: 6, Q: 1},
		{Name: "low_mid", MinFrequencyHz: 150, MaxFrequencyHz: 2000, MinGainDB: -12, MaxGainDB: 6, Q: 1},
		{Name: "high_mid", MinFrequencyHz: 500, MaxFrequencyHz: 8000, MinGainDB: -12, MaxGainDB: 6, Q: 1},
		{Name: "high", MinFrequencyHz: 2000, MaxFrequencyHz: 16000, MinGainDB: -12, MaxGainDB: 6, Q: 1},
	}}
}
