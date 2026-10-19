package room

import (
	"math"
	"sort"
)

const (
	// MinPASignalToNoiseDB is the margin the PA test signal must keep over the
	// room noise (captured through the same microphone and chain) before a
	// transfer feature is trusted. Below it the "response" is mostly noise.
	MinPASignalToNoiseDB = 10.0
	// featureSNRHalfOctaves is the neighbourhood checked around each feature.
	featureSNRHalfOctaves = 1.0 / 6

	paStatusInsufficientSignal = "insufficient_signal"
)

// PASignalToNoise records how far the PA capture sits above the room noise on
// the shared fine-spectrum axis.
type PASignalToNoise struct {
	NoiseReference   string  `json:"noise_reference"`
	MinRequiredDB    float64 `json:"min_required_db"`
	MedianDB         float64 `json:"median_db"`
	UsableFraction   float64 `json:"usable_fraction"`
	ExcludedFeatures int     `json:"excluded_features"`
}

// GatePAResponse compares the PA capture with a noise capture taken through
// the same microphone (normally the empty room) and removes transfer features
// that are not at least MinPASignalToNoiseDB above it. When the median margin
// across the analysis band is below that, the response is marked
// insufficient_signal and carries no features, so no PA EQ is recommended.
func GatePAResponse(pa *PAResponse, measured, noise Measurement, noiseLabel string) {
	if pa == nil || pa.Status != paStatusMeasured || len(measured.FineSpectrum) == 0 || len(noise.FineSpectrum) == 0 {
		return
	}
	type point struct{ f, snr float64 }
	var pts []point
	for _, p := range measured.FineSpectrum {
		if p.FrequencyHz < pa.AnalysisLowHz || p.FrequencyHz > pa.AnalysisHighHz {
			continue
		}
		pts = append(pts, point{p.FrequencyHz, p.LevelDB - levelAt(noise.FineSpectrum, p.FrequencyHz)})
	}
	if len(pts) == 0 {
		return
	}
	snrs := make([]float64, len(pts))
	usable := 0
	for i, p := range pts {
		snrs[i] = p.snr
		if p.snr >= MinPASignalToNoiseDB {
			usable++
		}
	}
	sort.Float64s(snrs)
	g := &PASignalToNoise{
		NoiseReference: noiseLabel,
		MinRequiredDB:  MinPASignalToNoiseDB,
		MedianDB:       finiteDB(snrs[len(snrs)/2]),
		UsableFraction: float64(usable) / float64(len(pts)),
	}
	pa.SignalToNoise = g
	if g.MedianDB < MinPASignalToNoiseDB {
		g.ExcludedFeatures = len(pa.Features)
		pa.Features = nil
		pa.Status = paStatusInsufficientSignal
		return
	}
	kept := pa.Features[:0]
	for _, f := range pa.Features {
		local := math.Inf(1)
		for _, p := range pts {
			if math.Abs(math.Log2(p.f/f.FrequencyHz)) <= featureSNRHalfOctaves {
				local = math.Min(local, p.snr)
			}
		}
		if local >= MinPASignalToNoiseDB {
			kept = append(kept, f)
		} else {
			g.ExcludedFeatures++
		}
	}
	pa.Features = kept
}

// levelAt returns the level of the nearest point on a log-frequency axis.
func levelAt(spec []SpectralPoint, f float64) float64 {
	best, bestD := floorDB, math.Inf(1)
	for _, p := range spec {
		if d := math.Abs(math.Log2(p.FrequencyHz / f)); d < bestD {
			best, bestD = p.LevelDB, d
		}
	}
	return best
}
