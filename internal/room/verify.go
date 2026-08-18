package room

import (
	"fmt"
	"math"
)

func VerifyRecommendations(before Measurement, after Measurement, recs []Recommendation) []string {
	notes := []string{
		"Verification compares two separate captures on the same FFT/dBFS axis. It does not prove the room is corrected.",
	}
	checked := 0
	improved := 0
	for _, rec := range recs {
		if rec.Kind != "eq_cut" || rec.FrequencyHz <= 0 {
			continue
		}
		b := nearestBandLevel(before.LegacyBands, rec.FrequencyHz)
		a := nearestBandLevel(after.LegacyBands, rec.FrequencyHz)
		checked++
		if a <= b-1 {
			improved++
			notes = append(notes, fmt.Sprintf("The %.0f Hz region is %.1f dB lower in the verification capture than before (%+.1f vs %+.1f dBFS). The cut direction matches; still listen.", rec.FrequencyHz, b-a, a, b))
			continue
		}
		notes = append(notes, fmt.Sprintf("The %.0f Hz region did not drop in the verification capture (%+.1f vs %+.1f dBFS). Re-listen; do not add more cut automatically.", rec.FrequencyHz, a, b))
	}
	if checked == 0 {
		notes = append(notes, "No cut recommendations were available to compare against the verification capture.")
	} else if improved == checked {
		notes = append(notes, "Every compared cut region was lower after the operator change. Treat this as supporting evidence, not a finished tune.")
	}
	return notes
}

func nearestBandLevel(bands []SpectralPoint, freq float64) float64 {
	best, d := 0.0, math.Inf(1)
	for _, p := range bands {
		if x := math.Abs(math.Log(p.FrequencyHz / freq)); x < d {
			best, d = p.LevelDB, x
		}
	}
	return best
}
