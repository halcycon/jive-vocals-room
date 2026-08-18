package room

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

var legacyCentresHz = []float64{80, 125, 195, 290, 440, 660, 1000, 1500, 2250, 3350, 5000, 7500, 11200, 16000, 24000}

const minToneProminenceDB = 8.0

func Analyse(p PCM) (Measurement, error) {
	mono, err := analysisMono(p)
	if err != nil {
		return Measurement{}, err
	}
	frames := spectra(mono, p.SampleRate)
	avg := averagePower(frames)
	fine := fractionalOctave(avg, p.SampleRate, 24)
	legacy := aggregateAtCentres(avg, p.SampleRate, legacyCentresHz)
	tones := tonalPeaks(frames, p.SampleRate)
	return Measurement{
		Source:           p.Source,
		DurationSeconds:  float64(len(mono)) / float64(p.SampleRate),
		BroadbandRMSDB:   finiteDB(rmsDB(mono)),
		PeakDB:           finiteDB(peakDB(mono)),
		SpectralFlatness: finiteNonNeg(flatness(avg)),
		FineSpectrum:     fine,
		LegacyBands:      legacy,
		TonalCandidates:  tones,
		Hum:              detectHum(tones),
	}, nil
}

func analysisMono(p PCM) ([]float64, error) {
	if p.SampleRate <= 0 || p.Channels <= 0 || len(p.Samples) < fftSize*p.Channels {
		return nil, errors.New("room analysis needs at least 8192 mono samples")
	}
	return downmix(p.Samples, p.Channels), nil
}

func Compare(empty, occupied Measurement) Comparison {
	return Comparison{BroadbandDeltaDB: occupied.BroadbandRMSDB - empty.BroadbandRMSDB, BandDeltas: subtractBands(empty.LegacyBands, occupied.LegacyBands)}
}

// AnalysePresenter expresses speech margin on one consistent FFT/dBFS axis.
// It is an estimate from separate captures, not a simultaneous SNR measurement.
func AnalysePresenter(speech, noise Measurement) PresenterTest {
	margins := make([]SpectralPoint, min(len(speech.LegacyBands), len(noise.LegacyBands)))
	var presenceSum float64
	var presenceCount int
	masked := []SpectralPoint{}
	for i := range margins {
		margins[i] = SpectralPoint{FrequencyHz: speech.LegacyBands[i].FrequencyHz, LevelDB: speech.LegacyBands[i].LevelDB - noise.LegacyBands[i].LevelDB}
		if margins[i].FrequencyHz >= 1500 && margins[i].FrequencyHz <= 4000 {
			presenceSum += margins[i].LevelDB
			presenceCount++
		}
		if margins[i].LevelDB < maskingMarginDB {
			masked = append(masked, margins[i])
		}
	}
	presence := 0.0
	if presenceCount > 0 {
		presence = presenceSum / float64(presenceCount)
	}
	overall := speech.BroadbandRMSDB - noise.BroadbandRMSDB
	lowMid := strongestExcess(speech.LegacyBands, 125, 440)
	excess := 0.0
	if len(speech.LegacyBands) > 0 {
		excess = lowMid.LevelDB - medianLevels(speech.LegacyBands)
	}
	return PresenterTest{
		OverallMarginDB:  overall,
		BandMargins:      margins,
		PresenceMarginDB: presence,
		LowMidExcessDB:   excess,
		MaskedBands:      masked,
		GainAdvice:       gainAdvice(overall, presence, nil),
		Speech:           speech,
	}
}

const (
	maskingMarginDB    = 6.0
	presenceAdequateDB = 12.0
	overallAdequateDB  = 10.0
	GainAdviceMayHelp  = "more_gain_may_help"
	GainAdviceRisky    = "more_gain_likely_risky"
	GainAdviceAdequate = "gain_adequate"
)

func gainAdvice(overall, presence float64, pa *PAResponse) string {
	if paHasFeedbackRisk(pa) {
		return GainAdviceRisky
	}
	if overall >= overallAdequateDB && presence >= presenceAdequateDB {
		return GainAdviceAdequate
	}
	if overall < overallAdequateDB || presence < presenceAdequateDB {
		return GainAdviceMayHelp
	}
	return GainAdviceAdequate
}

func paHasFeedbackRisk(pa *PAResponse) bool {
	if pa == nil {
		return false
	}
	for _, f := range pa.Features {
		if f.Kind == FeatureNarrowResonance || f.Kind == FeatureCombNotch {
			return true
		}
	}
	return false
}

func RecommendSession(s Session) []Recommendation {
	recs := Recommend(s.EmptyRoom, s.OccupiedRoom, s.PARoom, s.Mixer)
	if s.Presenter != nil {
		s.Presenter.GainAdvice = gainAdvice(s.Presenter.OverallMarginDB, s.Presenter.PresenceMarginDB, s.PARoom)
		recs = append(recs, recommendPresenter(*s.Presenter, s.Mixer)...)
	}
	return recs
}

func recommendPresenter(p PresenterTest, mixer MixerCapability) []Recommendation {
	recs := []Recommendation{}
	if mixer.HasHighPass && presenterNeedsHPF(p.Speech) {
		hz := 80.0
		if len(mixer.HighPassChoicesHz) > 0 {
			hz = mixer.HighPassChoicesHz[0]
		}
		recs = append(recs, Recommendation{Kind: "hpf", FrequencyHz: hz, Evidence: "presenter low-band energy", Reason: fmt.Sprintf("Enable a high-pass around %.0f Hz if the mixer has one. It is a starting point to reduce rumble without pretending to flatten the room.", hz), Confidence: 0.7, StartingPoint: true})
	}
	if p.LowMidExcessDB > 4 && len(p.Speech.LegacyBands) > 0 {
		low := strongestExcess(p.Speech.LegacyBands, 125, 440)
		freq, minGainDB, maxGainDB := nearestMixerControl(low.FrequencyHz, mixer)
		if freq > 0 {
			gain := -min(3, p.LowMidExcessDB)
			gain = min(max(gain, minGainDB), min(0, maxGainDB))
			if gain < 0 {
				recs = append(recs, Recommendation{Kind: "eq_cut", FrequencyHz: freq, GainDB: gain, Q: 1, Evidence: "presenter low/low-mid excess", Reason: "Presenter low/low-mid energy is elevated relative to the speech-band median. Try a small cut and verify by listening.", Confidence: 0.6, StartingPoint: true})
			}
		}
	}
	if len(p.MaskedBands) > 0 {
		recs = append(recs, Recommendation{Kind: "warning", Evidence: "band-wise speech/noise margin", Reason: "One or more bands have less than 6 dB estimated speech margin. Static EQ cannot remove the masking noise; check microphone placement and occupied-room level.", Confidence: 0.75, StartingPoint: true})
	}
	switch p.GainAdvice {
	case GainAdviceRisky:
		recs = append(recs, Recommendation{Kind: "warning", Evidence: p.GainAdvice, Reason: "Raising master gain is likely risky: a PA resonance or comb feature is already present, or the closed loop would move closer to feedback. Do not add automatic makeup gain.", Confidence: 0.7, StartingPoint: true})
	case GainAdviceMayHelp:
		recs = append(recs, Recommendation{Kind: "warning", Evidence: p.GainAdvice, Reason: "Speech margin is modest. A little more acoustic level might help intelligibility, but jive-room will not raise master gain. Increase level only by ear and stop at the first hint of ring.", Confidence: 0.55, StartingPoint: true})
	}
	return recs
}

func presenterNeedsHPF(speech Measurement) bool {
	if len(speech.LegacyBands) == 0 {
		return false
	}
	low := strongestExcess(speech.LegacyBands, 80, 125)
	return low.LevelDB > medianLevels(speech.LegacyBands)+3
}

func Recommend(empty Measurement, occupied *Measurement, pa *PAResponse, mixer MixerCapability) []Recommendation {
	result := []Recommendation{}
	if empty.Hum != nil {
		result = append(result, Recommendation{Kind: "investigate", FrequencyHz: empty.Hum.FundamentalHz, Reason: "Persistent mains-frequency harmonics suggest an electrical or mechanical hum; find the source rather than trying to EQ it away.", Confidence: empty.Hum.Confidence, StartingPoint: true})
	}
	if occupied != nil && occupied.BroadbandRMSDB-empty.BroadbandRMSDB > 3 {
		result = append(result, Recommendation{Kind: "warning", Reason: "Occupied-room noise rose materially. Static EQ cannot remove audience chatter; check speech margin and microphone placement.", Confidence: .9, StartingPoint: true})
	}
	low := strongestExcess(empty.LegacyBands, 125, 660)
	if low.LevelDB > medianLevels(empty.LegacyBands)+4 {
		freq, minGainDB, maxGainDB := nearestMixerControl(low.FrequencyHz, mixer)
		if freq > 0 {
			gainDB := -min(3, low.LevelDB-medianLevels(empty.LegacyBands))
			gainDB = min(max(gainDB, minGainDB), min(0, maxGainDB))
			result = append(result, Recommendation{Kind: "eq_cut", FrequencyHz: freq, GainDB: gainDB, Q: 1, Evidence: "empty-room low/low-mid band energy", Reason: "Broad low/low-mid room energy is elevated; try a small cut and verify by listening and re-measuring.", Confidence: .65, StartingPoint: true})
		}
	}
	if pa != nil && pa.Status == paStatusMeasured {
		result = append(result, recommendFromPA(*pa, mixer)...)
	}
	return result
}

func finiteNonNeg(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

func flatness(p []float64) float64 {
	var logs, sum float64
	var n int
	for _, v := range p[1:] {
		if v > 0 {
			logs += math.Log(v)
			sum += v
			n++
		}
	}
	if n == 0 || sum == 0 {
		return 0
	}
	return math.Exp(logs/float64(n)) / (sum / float64(n))
}

func tonalPeaks(frames [][]float64, rate int) []TonalPeak {
	avg := averagePower(frames)
	candidates := []TonalPeak{}
	for i := 3; i < len(avg)-3; i++ {
		base := []float64{}
		for j := i - 3; j <= i+3; j++ {
			if j != i {
				base = append(base, level(avg[j]))
			}
		}
		sort.Float64s(base)
		prom := level(avg[i]) - base[len(base)/2]
		if prom < minToneProminenceDB || avg[i] <= avg[i-1] || avg[i] < avg[i+1] {
			continue
		}
		present := 0
		for _, f := range frames {
			if level(f[i])-level((f[i-2]+f[i+2])/2) >= minToneProminenceDB {
				present++
			}
		}
		persistence := float64(present) / float64(len(frames))
		if persistence >= .5 {
			candidates = append(candidates, TonalPeak{FrequencyHz: binFrequency(i, rate), ProminenceDB: prom, Persistence: persistence})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ProminenceDB > candidates[j].ProminenceDB })
	if len(candidates) > 12 {
		return candidates[:12]
	}
	return candidates
}

func detectHum(peaks []TonalPeak) *HumDiagnostic {
	for _, base := range []float64{50, 60} {
		hs := []float64{}
		score := 0.0
		for harmonic := 1; harmonic <= 4; harmonic++ {
			target := base * float64(harmonic)
			for _, p := range peaks {
				if math.Abs(p.FrequencyHz-target) <= 4 {
					hs = append(hs, p.FrequencyHz)
					score += p.Persistence
					break
				}
			}
		}
		if len(hs) >= 2 {
			return &HumDiagnostic{FundamentalHz: base, HarmonicsHz: hs, Confidence: min(1, score/3)}
		}
	}
	return nil
}

func subtractBands(a, b []SpectralPoint) []SpectralPoint {
	n := min(len(a), len(b))
	out := make([]SpectralPoint, n)
	for i := range n {
		out[i] = SpectralPoint{FrequencyHz: b[i].FrequencyHz, LevelDB: b[i].LevelDB - a[i].LevelDB}
	}
	return out
}

func medianLevels(v []SpectralPoint) float64 {
	if len(v) == 0 {
		return 0
	}
	x := make([]float64, len(v))
	for i, p := range v {
		x[i] = p.LevelDB
	}
	sort.Float64s(x)
	return x[len(x)/2]
}

func strongestExcess(v []SpectralPoint, lo, hi float64) SpectralPoint {
	best := SpectralPoint{LevelDB: floorDB}
	for _, p := range v {
		if p.FrequencyHz >= lo && p.FrequencyHz <= hi && p.LevelDB > best.LevelDB {
			best = p
		}
	}
	return best
}

func nearestMixerControl(target float64, m MixerCapability) (frequencyHz, minGainDB, maxGainDB float64) {
	best, d := 0.0, math.Inf(1)
	for _, b := range m.Bands {
		f := b.FixedFrequencyHz
		if f == 0 {
			f = min(max(target, b.MinFrequencyHz), b.MaxFrequencyHz)
		}
		if x := math.Abs(math.Log(f / target)); x < d {
			best, minGainDB, maxGainDB, d = f, b.MinGainDB, b.MaxGainDB, x
		}
	}
	return best, minGainDB, maxGainDB
}
