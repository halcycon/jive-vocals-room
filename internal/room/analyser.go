package room

import (
	"errors"
	"math"
	"sort"
)

var legacyCentresHz = []float64{80, 125, 195, 290, 440, 660, 1000, 1500, 2250, 3350, 5000, 7500, 11200, 16000, 24000}

const (
	fftSize             = 8192
	frameHop            = fftSize / 2
	floorDB             = -140.0
	minToneProminenceDB = 8.0
)

type PCM struct {
	Samples              []float64
	SampleRate, Channels int
	Source               string
}

func Analyse(p PCM) (Measurement, error) {
	if p.SampleRate <= 0 || p.Channels <= 0 || len(p.Samples) < fftSize {
		return Measurement{}, errors.New("room analysis needs at least 8192 mono samples")
	}
	mono := downmix(p.Samples, p.Channels)
	frames := spectra(mono, p.SampleRate)
	avg := averagePower(frames)
	fine := fractionalOctave(avg, p.SampleRate, 24)
	legacy := aggregateAtCentres(avg, p.SampleRate, legacyCentresHz)
	tones := tonalPeaks(frames, p.SampleRate)
	return Measurement{Source: p.Source, DurationSeconds: float64(len(mono)) / float64(p.SampleRate), BroadbandRMSDB: rmsDB(mono), PeakDB: peakDB(mono), SpectralFlatness: flatness(avg), FineSpectrum: fine, LegacyBands: legacy, TonalCandidates: tones, Hum: detectHum(tones)}, nil
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
	for i := range margins {
		margins[i] = SpectralPoint{FrequencyHz: speech.LegacyBands[i].FrequencyHz, LevelDB: speech.LegacyBands[i].LevelDB - noise.LegacyBands[i].LevelDB}
		if margins[i].FrequencyHz >= 1500 && margins[i].FrequencyHz <= 4000 {
			presenceSum += margins[i].LevelDB
			presenceCount++
		}
	}
	presence := 0.0
	if presenceCount > 0 {
		presence = presenceSum / float64(presenceCount)
	}
	return PresenterTest{OverallMarginDB: speech.BroadbandRMSDB - noise.BroadbandRMSDB, BandMargins: margins, PresenceMarginDB: presence}
}

func Recommend(empty Measurement, occupied *Measurement, mixer MixerCapability) []Recommendation {
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
			result = append(result, Recommendation{Kind: "eq_cut", FrequencyHz: freq, GainDB: gainDB, Q: 1, Reason: "Broad low/low-mid room energy is elevated; try a small cut and verify by listening and re-measuring.", Confidence: .65, StartingPoint: true})
		}
	}
	return result
}

func downmix(in []float64, channels int) []float64 {
	n := len(in) / channels
	out := make([]float64, n)
	for i := range n {
		for c := range channels {
			out[i] += in[i*channels+c]
		}
		out[i] /= float64(channels)
	}
	return out
}
func rmsDB(s []float64) float64 {
	var v float64
	for _, x := range s {
		v += x * x
	}
	return db(math.Sqrt(v / float64(len(s))))
}
func peakDB(s []float64) float64 {
	var p float64
	for _, x := range s {
		p = max(p, math.Abs(x))
	}
	return db(p)
}
func db(v float64) float64 {
	if v <= 0 {
		return floorDB
	}
	return max(floorDB, 20*math.Log10(v))
}
func spectra(s []float64, rate int) [][]float64 {
	var out [][]float64
	for start := 0; start+fftSize <= len(s); start += frameHop {
		x := make([]complex128, fftSize)
		for i := range fftSize {
			w := .5 - .5*math.Cos(2*math.Pi*float64(i)/float64(fftSize-1))
			x[i] = complex(s[start+i]*w, 0)
		}
		fft(x)
		p := make([]float64, fftSize/2+1)
		for i := range p {
			p[i] = real(x[i])*real(x[i]) + imag(x[i])*imag(x[i])
		}
		out = append(out, p)
	}
	return out
}
func fft(x []complex128) {
	for i, j := 1, 0; i < len(x); i++ {
		bit := len(x) >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for n := 2; n <= len(x); n <<= 1 {
		wlen := complex(math.Cos(-2*math.Pi/float64(n)), math.Sin(-2*math.Pi/float64(n)))
		for i := 0; i < len(x); i += n {
			w := complex(1, 0)
			for j := 0; j < n/2; j++ {
				u, v := x[i+j], x[i+j+n/2]*w
				x[i+j], x[i+j+n/2] = u+v, u-v
				w *= wlen
			}
		}
	}
}
func averagePower(frames [][]float64) []float64 {
	out := make([]float64, len(frames[0]))
	for _, f := range frames {
		for i, v := range f {
			out[i] += v
		}
	}
	for i := range out {
		out[i] /= float64(len(frames))
	}
	return out
}
func level(power float64) float64 {
	if power <= 0 {
		return floorDB
	}
	return max(floorDB, 10*math.Log10(power))
}
func fractionalOctave(p []float64, rate, divisions int) []SpectralPoint {
	centres := []float64{}
	for f := 25.0; f < float64(rate)/2; f *= math.Pow(2, 1/float64(divisions)) {
		centres = append(centres, f)
	}
	return aggregateAtCentres(p, rate, centres)
}
func aggregateAtCentres(p []float64, rate int, centres []float64) []SpectralPoint {
	out := make([]SpectralPoint, 0, len(centres))
	for i, c := range centres {
		// Keep a centre exactly at Nyquist for the legacy afftdn-compatible view.
		// Its lower half-band is measurable even though the centre itself is the
		// boundary. Centres above Nyquist remain omitted.
		if c > float64(rate)/2 {
			continue
		}
		lo, hi := c/math.Pow(2, 1.0/48), c*math.Pow(2, 1.0/48)
		if len(centres) == len(legacyCentresHz) {
			if i == 0 {
				lo = c / math.Sqrt(centres[1]/c)
			} else {
				lo = math.Sqrt(centres[i-1] * c)
			}
			if i == len(centres)-1 {
				hi = c * math.Sqrt(c/centres[i-1])
			} else {
				hi = math.Sqrt(c * centres[i+1])
			}
		}
		a, b := int(lo*fftSize/float64(rate)), int(math.Ceil(hi*fftSize/float64(rate)))
		a = max(1, a)
		b = min(len(p), max(a+1, b))
		var sum float64
		for _, v := range p[a:b] {
			sum += v
		}
		out = append(out, SpectralPoint{c, level(sum / float64(b-a))})
	}
	return out
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
			candidates = append(candidates, TonalPeak{float64(i) * float64(rate) / fftSize, prom, persistence})
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
			return &HumDiagnostic{base, hs, min(1, score/3)}
		}
	}
	return nil
}
func subtractBands(a, b []SpectralPoint) []SpectralPoint {
	n := min(len(a), len(b))
	out := make([]SpectralPoint, n)
	for i := range n {
		out[i] = SpectralPoint{b[i].FrequencyHz, b[i].LevelDB - a[i].LevelDB}
	}
	return out
}
func medianLevels(v []SpectralPoint) float64 {
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
