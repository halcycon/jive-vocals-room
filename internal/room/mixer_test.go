package room

import "testing"

func TestMixerRepresentations(t *testing.T) {
	tests := []struct {
		want  MixerKind
		mixer MixerCapability
		bands int
	}{
		{MixerFixed3, FixedThreeBandMixer(80, 2500, 12000), 3},
		{MixerSemiParam4, SemiParametricFourBandMixer(), 4},
		{MixerGraphic, GraphicMixer([]float64{125, 250, 500}), 3},
		{MixerParametric, DefaultParametricMixer(), 4},
	}
	for _, test := range tests {
		if test.mixer.Kind != test.want || len(test.mixer.Bands) != test.bands {
			t.Errorf("%s representation: %+v", test.want, test.mixer)
		}
	}
}
