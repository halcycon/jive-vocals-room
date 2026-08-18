package room

import (
	"fmt"
	"math"
	"sort"
)

const (
	maxAutoBoostDB          = 3.0
	maxAutoCutDB            = 6.0
	maxTotalPositiveGainDB  = 3.0
	maxTotalAbsCorrectionDB = 12.0
	broadFeatureThresholdDB = 3.0
	narrowProminenceDB      = 6.0
	deepNullThresholdDB     = -12.0
	combMinNotches          = 3
	combSpacingTolerance    = 0.2
	minCombSpacingHz        = 40.0
	narrowMaxBandwidthOct   = 1.0 / 6.0
	paRecommendLowQ         = 0.7
	paRecommendNarrowQ      = 4.0
)

type ResponseAnalysisConfig struct {
	SmoothOctaveDivisions int
	FineOctaveDivisions   int
	PowerRatioEpsilon     float64
	AnalysisLowHz         float64
	AnalysisHighHz        float64
	MidbandLowHz          float64
	MidbandHighHz         float64
}

func DefaultResponseAnalysisConfig() ResponseAnalysisConfig {
	return ResponseAnalysisConfig{
		SmoothOctaveDivisions: 6,
		FineOctaveDivisions:   24,
		PowerRatioEpsilon:     1e-12,
		AnalysisLowHz:         80,
		AnalysisHighHz:        10000,
		MidbandLowHz:          500,
		MidbandHighHz:         2000,
	}
}

func (c ResponseAnalysisConfig) validate() error {
	if c.SmoothOctaveDivisions < 1 || c.FineOctaveDivisions < 1 {
		return fmt.Errorf("octave divisions must be positive")
	}
	if c.PowerRatioEpsilon <= 0 {
		return fmt.Errorf("power ratio epsilon must be positive")
	}
	if c.AnalysisLowHz <= 0 || c.AnalysisHighHz <= c.AnalysisLowHz {
		return fmt.Errorf("analysis band %.1f–%.1f Hz is invalid", c.AnalysisLowHz, c.AnalysisHighHz)
	}
	if c.MidbandLowHz <= 0 || c.MidbandHighHz <= c.MidbandLowHz {
		return fmt.Errorf("midband %.1f–%.1f Hz is invalid", c.MidbandLowHz, c.MidbandHighHz)
	}
	return nil
}

func AnalysePAResponse(reference, measured PCM, cfg ResponseAnalysisConfig) (PAResponse, error) {
	if err := cfg.validate(); err != nil {
		return PAResponse{}, err
	}
	if reference.SampleRate != measured.SampleRate {
		return PAResponse{}, fmt.Errorf("pa response sample rates differ: reference %d Hz, measured %d Hz", reference.SampleRate, measured.SampleRate)
	}
	refMono, err := analysisMono(reference)
	if err != nil {
		return PAResponse{}, fmt.Errorf("reference: %w", err)
	}
	measMono, err := analysisMono(measured)
	if err != nil {
		return PAResponse{}, fmt.Errorf("measured: %w", err)
	}
	refFrames := spectra(refMono, reference.SampleRate)
	measFrames := spectra(measMono, measured.SampleRate)
	if len(refFrames) == 0 || len(measFrames) == 0 {
		return PAResponse{}, fmt.Errorf("pa response needs enough samples for an 8192-point FFT")
	}
	refPower := averagePower(refFrames)
	measPower := averagePower(measFrames)
	n := min(len(refPower), len(measPower))
	ratio := make([]float64, n)
	for i := range ratio {
		ratio[i] = measPower[i] / max(refPower[i], cfg.PowerRatioEpsilon)
	}
	rawFine := fractionalOctave(ratio, reference.SampleRate, cfg.FineOctaveDivisions)
	rawSmooth := fractionalOctave(ratio, reference.SampleRate, cfg.SmoothOctaveDivisions)
	offset := midbandMedian(rawSmooth, cfg.MidbandLowHz, cfg.MidbandHighHz)
	fine := shiftSpectrum(rawFine, -offset)
	smooth := shiftSpectrum(rawSmooth, -offset)
	binDB := make([]float64, n)
	for i := range binDB {
		binDB[i] = finiteDB(level(ratio[i]) - offset)
	}
	features := classifyTransfer(smooth, fine, binDB, reference.SampleRate, cfg)
	return PAResponse{
		Method:             PAMethodPinkAveraging,
		Status:             paStatusMeasured,
		Reference:          captureMetadata(reference, refMono),
		MeasuredCapture:    captureMetadata(measured, measMono),
		MidbandOffsetDB:    finiteDB(offset),
		PowerRatioEpsilon:  cfg.PowerRatioEpsilon,
		AnalysisLowHz:      cfg.AnalysisLowHz,
		AnalysisHighHz:     cfg.AnalysisHighHz,
		TransferFineDB:     fine,
		TransferSmoothedDB: smooth,
		Features:           features,
	}, nil
}

func captureMetadata(p PCM, mono []float64) *CaptureMetadata {
	rate := p.SampleRate
	if rate <= 0 {
		rate = 1
	}
	return &CaptureMetadata{
		Source:          p.Source,
		SampleRateHz:    p.SampleRate,
		Channels:        p.Channels,
		DurationSeconds: float64(len(mono)) / float64(rate),
	}
}

func midbandMedian(bands []SpectralPoint, lo, hi float64) float64 {
	vals := []float64{}
	for _, p := range bands {
		if p.FrequencyHz >= lo && p.FrequencyHz <= hi {
			vals = append(vals, p.LevelDB)
		}
	}
	if len(vals) == 0 {
		return 0
	}
	sort.Float64s(vals)
	return vals[len(vals)/2]
}

func shiftSpectrum(bands []SpectralPoint, deltaDB float64) []SpectralPoint {
	out := make([]SpectralPoint, len(bands))
	for i, p := range bands {
		out[i] = SpectralPoint{FrequencyHz: p.FrequencyHz, LevelDB: finiteDB(p.LevelDB + deltaDB)}
	}
	return out
}

func UnmeasuredPAResponse() *PAResponse {
	return &PAResponse{Method: PAMethodPinkAveraging, Status: paStatusNotMeasured}
}

func classifyTransfer(smooth, fine []SpectralPoint, binDB []float64, rate int, cfg ResponseAnalysisConfig) []ResponseFeature {
	broad := groupSmoothed(smooth, cfg)
	narrow := dedupeFeatures(append(narrowFeatures(fine, cfg), binDeepNulls(binDB, rate, cfg)...))
	features := make([]ResponseFeature, 0, len(broad)+len(narrow))
	features = append(features, broad...)
	for _, n := range narrow {
		if n.Kind != FeatureDeepNull && coveredByBroad(n, broad) {
			continue
		}
		features = append(features, n)
	}
	return markCombNotches(features, cfg)
}

func binDeepNulls(binDB []float64, rate int, cfg ResponseAnalysisConfig) []ResponseFeature {
	out := []ResponseFeature{}
	for i := 4; i < len(binDB)-4; i++ {
		freq := binFrequency(i, rate)
		if freq < cfg.AnalysisLowHz || freq > cfg.AnalysisHighHz || binDB[i] > deepNullThresholdDB {
			continue
		}
		if binDB[i] > binDB[i-1] || binDB[i] > binDB[i+1] {
			continue
		}
		base := []float64{binDB[i-4], binDB[i-3], binDB[i-2], binDB[i+2], binDB[i+3], binDB[i+4]}
		sort.Float64s(base)
		if base[len(base)/2]-binDB[i] < narrowProminenceDB {
			continue
		}
		out = append(out, ResponseFeature{
			Kind:        FeatureDeepNull,
			FrequencyHz: freq,
			MagnitudeDB: binDB[i],
			DoNotBoost:  true,
			Reason:      "A deep or narrow dip in the PA/room transfer is likely a room null or comb notch. Do not invert it with a boost.",
		})
	}
	return out
}

func dedupeFeatures(in []ResponseFeature) []ResponseFeature {
	if len(in) == 0 {
		return in
	}
	sort.Slice(in, func(i, j int) bool { return in[i].FrequencyHz < in[j].FrequencyHz })
	out := []ResponseFeature{in[0]}
	for _, f := range in[1:] {
		prev := &out[len(out)-1]
		if f.Kind == prev.Kind && math.Abs(math.Log2(f.FrequencyHz/prev.FrequencyHz)) < 1.0/24 {
			if math.Abs(f.MagnitudeDB) > math.Abs(prev.MagnitudeDB) {
				*prev = f
			}
			continue
		}
		out = append(out, f)
	}
	return out
}

func groupSmoothed(smooth []SpectralPoint, cfg ResponseAnalysisConfig) []ResponseFeature {
	type run struct {
		first, last int
		sign        int
		peak        SpectralPoint
	}
	var current *run
	var out []ResponseFeature
	flush := func() {
		if current == nil {
			return
		}
		first := smooth[current.first]
		last := smooth[current.last]
		widthOct := math.Log2(last.FrequencyHz / first.FrequencyHz)
		if current.first == current.last {
			widthOct = 1 / float64(max(cfg.SmoothOctaveDivisions, 1))
		}
		kind := FeatureBroadExcess
		reason := "Smoothed PA/room transfer shows a broad excess relative to the 500–2000 Hz median. Prefer a cut, then listen and re-measure."
		if current.sign < 0 {
			kind = FeatureBroadDeficit
			reason = "Smoothed PA/room transfer shows a broad deficit. A small boost may be a starting point only if this is not a room null."
		}
		doNotBoost := current.sign < 0 && current.peak.LevelDB <= deepNullThresholdDB
		if doNotBoost {
			reason = "Broad deficit reaches a depth where inversion is unsafe. Do not boost this region automatically."
		}
		out = append(out, ResponseFeature{
			Kind:         kind,
			FrequencyHz:  current.peak.FrequencyHz,
			MagnitudeDB:  current.peak.LevelDB,
			BandwidthOct: widthOct,
			DoNotBoost:   doNotBoost,
			Reason:       reason,
		})
		current = nil
	}
	for i, p := range smooth {
		if p.FrequencyHz < cfg.AnalysisLowHz || p.FrequencyHz > cfg.AnalysisHighHz {
			flush()
			continue
		}
		sign := 0
		if p.LevelDB >= broadFeatureThresholdDB {
			sign = 1
		} else if p.LevelDB <= -broadFeatureThresholdDB {
			sign = -1
		}
		if sign == 0 {
			flush()
			continue
		}
		if current != nil && current.sign != sign {
			flush()
		}
		if current == nil {
			current = &run{first: i, last: i, sign: sign, peak: p}
			continue
		}
		current.last = i
		if math.Abs(p.LevelDB) > math.Abs(current.peak.LevelDB) {
			current.peak = p
		}
	}
	flush()
	return out
}

func narrowFeatures(fine []SpectralPoint, cfg ResponseAnalysisConfig) []ResponseFeature {
	out := []ResponseFeature{}
	for i := 3; i < len(fine)-3; i++ {
		p := fine[i]
		if p.FrequencyHz < cfg.AnalysisLowHz || p.FrequencyHz > cfg.AnalysisHighHz {
			continue
		}
		base := make([]float64, 0, 6)
		for j := i - 3; j <= i+3; j++ {
			if j != i {
				base = append(base, fine[j].LevelDB)
			}
		}
		sort.Float64s(base)
		prom := p.LevelDB - base[len(base)/2]
		localMax := p.LevelDB >= fine[i-1].LevelDB && p.LevelDB >= fine[i+1].LevelDB
		localMin := p.LevelDB <= fine[i-1].LevelDB && p.LevelDB <= fine[i+1].LevelDB
		widthOct := 1 / float64(max(cfg.FineOctaveDivisions, 1))
		if localMax && prom >= narrowProminenceDB && widthOct <= narrowMaxBandwidthOct {
			out = append(out, ResponseFeature{
				Kind:         FeatureNarrowResonance,
				FrequencyHz:  p.FrequencyHz,
				MagnitudeDB:  p.LevelDB,
				BandwidthOct: widthOct,
				Reason:       "A narrow resonance sits above the local transfer baseline. A small cut is safer than chasing a peak with a boost elsewhere.",
			})
		}
		if localMin && prom <= -narrowProminenceDB && p.LevelDB <= deepNullThresholdDB {
			out = append(out, ResponseFeature{
				Kind:         FeatureDeepNull,
				FrequencyHz:  p.FrequencyHz,
				MagnitudeDB:  p.LevelDB,
				BandwidthOct: widthOct,
				DoNotBoost:   true,
				Reason:       "A deep or narrow dip in the PA/room transfer is likely a room null or comb notch. Do not invert it with a boost.",
			})
		}
	}
	return out
}

func coveredByBroad(n ResponseFeature, broad []ResponseFeature) bool {
	for _, b := range broad {
		sameSign := (n.MagnitudeDB >= 0) == (b.MagnitudeDB >= 0)
		if !sameSign {
			continue
		}
		half := math.Pow(2, max(b.BandwidthOct, 1.0/6.0)/2)
		if n.FrequencyHz >= b.FrequencyHz/half && n.FrequencyHz <= b.FrequencyHz*half {
			return true
		}
	}
	return false
}

func markCombNotches(features []ResponseFeature, cfg ResponseAnalysisConfig) []ResponseFeature {
	idx := []int{}
	for i, f := range features {
		if f.Kind == FeatureDeepNull && f.FrequencyHz >= cfg.AnalysisLowHz && f.FrequencyHz <= cfg.AnalysisHighHz {
			idx = append(idx, i)
		}
	}
	if len(idx) < combMinNotches {
		return features
	}
	sort.Slice(idx, func(a, b int) bool { return features[idx[a]].FrequencyHz < features[idx[b]].FrequencyHz })
	deltas := make([]float64, 0, len(idx)-1)
	for i := 1; i < len(idx); i++ {
		deltas = append(deltas, features[idx[i]].FrequencyHz-features[idx[i-1]].FrequencyHz)
	}
	sort.Float64s(deltas)
	median := deltas[len(deltas)/2]
	if median < minCombSpacingHz {
		return features
	}
	matched := 0
	for _, d := range deltas {
		if math.Abs(d-median)/median <= combSpacingTolerance {
			matched++
		}
	}
	if matched < combMinNotches-1 {
		return features
	}
	for _, i := range idx {
		features[i].Kind = FeatureCombNotch
		features[i].DoNotBoost = true
		features[i].Reason = "Periodic narrow dips are consistent with comb filtering from a reflection. Do not boost the notches."
	}
	return features
}

func recommendFromPA(pa PAResponse, mixer MixerCapability) []Recommendation {
	used := map[int]bool{}
	var totalAbs, totalPos float64
	out := []Recommendation{}
	add := func(rec Recommendation, band int) {
		if rec.GainDB > 0 {
			if rec.GainDB > maxAutoBoostDB || totalPos+rec.GainDB > maxTotalPositiveGainDB {
				return
			}
			totalPos += rec.GainDB
		}
		if totalAbs+math.Abs(rec.GainDB) > maxTotalAbsCorrectionDB {
			return
		}
		totalAbs += math.Abs(rec.GainDB)
		if band >= 0 {
			used[band] = true
		}
		rec.StartingPoint = true
		out = append(out, rec)
	}
	for _, f := range pa.Features {
		if f.Kind == FeatureDeepNull || f.Kind == FeatureCombNotch || f.DoNotBoost {
			out = append(out, Recommendation{
				Kind:          "do_not_boost",
				FrequencyHz:   f.FrequencyHz,
				Evidence:      string(f.Kind),
				Reason:        f.Reason,
				Confidence:    0.8,
				StartingPoint: true,
			})
		}
	}
	for _, f := range pa.Features {
		if f.Kind != FeatureBroadExcess {
			continue
		}
		rec, band, ok := mapPACut(f, mixer, used, paRecommendLowQ, "1/6-octave smoothed transfer excess")
		if ok {
			add(rec, band)
		}
	}
	for _, f := range pa.Features {
		if f.Kind != FeatureNarrowResonance {
			continue
		}
		rec, band, ok := mapPACut(f, mixer, used, paRecommendNarrowQ, "narrow transfer resonance")
		if ok {
			add(rec, band)
		}
	}
	for _, f := range pa.Features {
		if f.Kind != FeatureBroadDeficit || f.DoNotBoost {
			continue
		}
		if hasCombFeatures(pa.Features) || nearDoNotBoost(f.FrequencyHz, pa.Features, 0.5) {
			continue
		}
		rec, band, ok := mapPABoost(f, mixer, used)
		if ok {
			add(rec, band)
		}
	}
	return out
}

func mapPACut(f ResponseFeature, mixer MixerCapability, used map[int]bool, q float64, evidence string) (Recommendation, int, bool) {
	band, freq, minGainDB, maxGainDB := unusedMixerControl(f.FrequencyHz, mixer, used)
	if freq == 0 {
		return Recommendation{}, -1, false
	}
	gain := -min(maxAutoCutDB, max(f.MagnitudeDB, 0))
	gain = min(max(gain, minGainDB), min(0, maxGainDB))
	if gain >= 0 {
		return Recommendation{}, -1, false
	}
	return Recommendation{
		Kind:        "eq_cut",
		FrequencyHz: freq,
		GainDB:      gain,
		Q:           q,
		Evidence:    evidence,
		Reason:      f.Reason,
		Confidence:  0.7,
	}, band, true
}

func mapPABoost(f ResponseFeature, mixer MixerCapability, used map[int]bool) (Recommendation, int, bool) {
	band, freq, minGainDB, maxGainDB := unusedMixerControl(f.FrequencyHz, mixer, used)
	if freq == 0 {
		return Recommendation{}, -1, false
	}
	gain := min(maxAutoBoostDB, max(-f.MagnitudeDB, 0))
	gain = min(max(gain, minGainDB), min(gain, maxGainDB))
	if gain <= 0 {
		return Recommendation{}, -1, false
	}
	return Recommendation{
		Kind:        "eq_boost",
		FrequencyHz: freq,
		GainDB:      gain,
		Q:           paRecommendLowQ,
		Evidence:    "1/6-octave smoothed transfer deficit",
		Reason:      "A broad deficit remains after cuts. Any boost is capped at +3 dB, is a starting point only, and must not target a deep null.",
		Confidence:  0.45,
	}, band, true
}

func unusedMixerControl(target float64, m MixerCapability, used map[int]bool) (index int, frequencyHz, minGainDB, maxGainDB float64) {
	best, bestI, d := 0.0, -1, math.Inf(1)
	for i, b := range m.Bands {
		if used[i] {
			continue
		}
		f := b.FixedFrequencyHz
		if f == 0 {
			f = min(max(target, b.MinFrequencyHz), b.MaxFrequencyHz)
		}
		if x := math.Abs(math.Log(f / target)); x < d {
			best, bestI, minGainDB, maxGainDB, d = f, i, b.MinGainDB, b.MaxGainDB, x
		}
	}
	if bestI < 0 {
		return -1, 0, 0, 0
	}
	return bestI, best, minGainDB, maxGainDB
}

func hasCombFeatures(features []ResponseFeature) bool {
	for _, f := range features {
		if f.Kind == FeatureCombNotch {
			return true
		}
	}
	return false
}

func nearDoNotBoost(frequencyHz float64, features []ResponseFeature, oct float64) bool {
	for _, f := range features {
		if f.DoNotBoost && f.FrequencyHz > 0 && math.Abs(math.Log2(frequencyHz/f.FrequencyHz)) <= oct {
			return true
		}
	}
	return false
}
