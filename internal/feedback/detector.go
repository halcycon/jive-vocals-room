// Package feedback detects ringing (acoustic feedback) candidates in a mono
// signal for operator-controlled ring-out. It is pure, non-real-time Go: it
// allocates per detector, never per sample, and never touches audio or mixers.
//
// A candidate needs several kinds of evidence at once: local prominence, an
// absolute level, persistence, frequency stability, and a lack of harmonic
// context. Growth raises confidence. Thresholds are justified by synthetic
// signals only and must be validated against recorded ring-outs, speech, and
// music before confidence is treated as calibrated.
package feedback

import (
	"fmt"
	"math"
	"sort"

	"github.com/linuxmatters/jive-vocals/internal/room"
)

const (
	frameSize = 8192
	hopSize   = 2048

	minFrequencyHz  = 40.0
	maxFrequencyHz  = 16000.0
	maxPeaksPerHop  = 16
	excludeHalfBins = 3

	// MinLevelDBFS gates idle converter tones (about -104 dBFS on the StudioLive)
	// and room noise; a ring worth cutting is far louder. Sine-calibrated: a
	// full-scale sine reads 0 dBFS.
	MinLevelDBFS = -70.0
	// MinProminenceDB is the peak's height above the median of its ±1/6-octave
	// neighbourhood (excluding the peak's own main lobe).
	MinProminenceDB = 15.0

	minDurationSeconds  = 0.5
	minPersistence      = 0.8
	maxMissedHops       = 4
	historySeconds      = 2.0
	growthWindowSeconds = 1.0
	// GrowingDBPerSecond marks a ring that is building, not merely sustained.
	GrowingDBPerSecond = 3.0
	// maxFrequencyStdRatio rejects gliding sources: speech harmonics drift by
	// percent within half a second; feedback holds to a fraction of that.
	maxFrequencyStdRatio = 0.002
	harmonicToleranceRat = 0.01
	maxHarmonicRatio     = 8
	harmonicFraction     = 0.5
	matchToleranceRatio  = 0.005
)

// Candidate is a current ringing candidate with its evidence.
type Candidate struct {
	ID                int
	FrequencyHz       float64
	LevelDBFS         float64
	ProminenceDB      float64
	BandwidthHz       float64
	DurationSeconds   float64
	Persistence       float64
	FrequencyStdHz    float64
	GrowthDBPerSecond float64
	Growing           bool
	Harmonic          bool
	Confidence        float64
	Evidence          []string
}

type peak struct {
	freq, level, prom, bw float64
	harmonic              bool
}

type obs struct {
	hop             int
	freq, level     float64
	prom, bw        float64
	harmonicContext bool
}

type track struct {
	id          int
	first, last int
	hits        int
	history     []obs
}

type Detector struct {
	rate    int
	window  []float64
	winNorm float64
	pending []float64
	frame   []complex128
	levels  []float64
	scratch []float64
	hop     int
	nextID  int
	tracks  []*track
}

// New returns a detector for mono input at rateHz.
func New(rateHz int) (*Detector, error) {
	if rateHz < 8000 {
		return nil, fmt.Errorf("feedback detector needs a sample rate of at least 8 kHz, got %d", rateHz)
	}
	d := &Detector{
		rate:    rateHz,
		window:  make([]float64, frameSize),
		frame:   make([]complex128, frameSize),
		levels:  make([]float64, frameSize/2+1),
		scratch: make([]float64, 0, 512),
	}
	var sum float64
	for i := range d.window {
		d.window[i] = .5 - .5*math.Cos(2*math.Pi*float64(i)/float64(frameSize-1))
		sum += d.window[i]
	}
	d.winNorm = 2 / sum
	return d, nil
}

func (d *Detector) SampleRate() int          { return d.rate }
func (d *Detector) HopSeconds() float64      { return float64(hopSize) / float64(d.rate) }
func (d *Detector) binHz() float64           { return float64(d.rate) / frameSize }
func (d *Detector) hopsFor(s float64) int    { return max(1, int(math.Ceil(s/d.HopSeconds()))) }
func (d *Detector) seconds(hops int) float64 { return float64(hops) * d.HopSeconds() }

// Push consumes mono samples and analyses every complete hop.
func (d *Detector) Push(samples []float64) {
	d.pending = append(d.pending, samples...)
	for len(d.pending) >= frameSize {
		d.analyse(d.pending[:frameSize])
		d.pending = d.pending[hopSize:]
	}
	if cap(d.pending) > 4*frameSize {
		d.pending = append([]float64(nil), d.pending...)
	}
}

func (d *Detector) analyse(x []float64) {
	for i := range frameSize {
		d.frame[i] = complex(x[i]*d.window[i], 0)
	}
	room.FFT(d.frame)
	for i := range d.levels {
		a := math.Hypot(real(d.frame[i]), imag(d.frame[i])) * d.winNorm
		d.levels[i] = 20 * math.Log10(max(a, 1e-12))
	}
	d.associate(d.findPeaks())
	d.hop++
}

func (d *Detector) findPeaks() []peak {
	bin := d.binHz()
	lo := max(excludeHalfBins+1, int(minFrequencyHz/bin))
	hi := min(len(d.levels)-excludeHalfBins-2, int(math.Min(maxFrequencyHz, 0.45*float64(d.rate))/bin))
	var peaks []peak
	for k := lo; k <= hi; k++ {
		l := d.levels
		if l[k] < MinLevelDBFS || l[k] <= l[k-1] || l[k] < l[k+1] {
			continue
		}
		prom := l[k] - d.neighbourhoodMedian(k)
		if prom < MinProminenceDB {
			continue
		}
		a, b, c := l[k-1], l[k], l[k+1]
		delta := 0.0
		if den := a - 2*b + c; den != 0 {
			delta = math.Max(-0.5, math.Min(0.5, 0.5*(a-c)/den))
		}
		peaks = append(peaks, peak{
			freq:  (float64(k) + delta) * bin,
			level: b - 0.25*(a-c)*delta,
			prom:  prom,
			bw:    d.bandwidth(k),
		})
	}
	sort.Slice(peaks, func(i, j int) bool { return peaks[i].level > peaks[j].level })
	if len(peaks) > maxPeaksPerHop {
		peaks = peaks[:maxPeaksPerHop]
	}
	markHarmonics(peaks)
	return peaks
}

func (d *Detector) neighbourhoodMedian(k int) float64 {
	half := max(8, int(float64(k)*(math.Pow(2, 1.0/6)-1)))
	d.scratch = d.scratch[:0]
	for j := max(1, k-half); j <= min(len(d.levels)-1, k+half); j++ {
		if j < k-excludeHalfBins || j > k+excludeHalfBins {
			d.scratch = append(d.scratch, d.levels[j])
		}
	}
	sort.Float64s(d.scratch)
	return d.scratch[len(d.scratch)/2]
}

func (d *Detector) bandwidth(k int) float64 {
	target := d.levels[k] - 3
	edge := func(step int) float64 {
		for j := k; j > 0 && j < len(d.levels)-1; j += step {
			next := j + step
			if d.levels[next] < target {
				frac := (d.levels[j] - target) / (d.levels[j] - d.levels[next])
				return math.Abs(float64(j-k) + frac*float64(step))
			}
		}
		return float64(frameSize)
	}
	return (edge(-1) + edge(1)) * d.binHz()
}

// markHarmonics flags peaks at an integer multiple (2–8×) of another peak, or
// with another peak at such a multiple of themselves: voiced speech and
// instruments, not a lone ring. Two unrelated rings at an exact integer ratio
// are downgraded too; they remain visible, just with lower confidence.
func markHarmonics(peaks []peak) {
	for i := range peaks {
		for j := range peaks {
			if i == j {
				continue
			}
			for n := 2.0; n <= maxHarmonicRatio; n++ {
				if near(peaks[j].freq, n*peaks[i].freq) || near(peaks[i].freq, n*peaks[j].freq) {
					peaks[i].harmonic = true
				}
			}
		}
	}
}

func near(a, b float64) bool { return math.Abs(a-b) <= harmonicToleranceRat*b }

func (d *Detector) associate(peaks []peak) {
	used := make([]bool, len(d.tracks))
	for _, p := range peaks {
		best, bestDist := -1, math.Inf(1)
		tol := math.Max(2*d.binHz(), matchToleranceRatio*p.freq)
		for i, t := range d.tracks {
			if used[i] {
				continue
			}
			dist := math.Abs(t.history[len(t.history)-1].freq - p.freq)
			if dist <= tol && dist < bestDist {
				best, bestDist = i, dist
			}
		}
		o := obs{hop: d.hop, freq: p.freq, level: p.level, prom: p.prom, bw: p.bw, harmonicContext: p.harmonic}
		if best < 0 {
			d.nextID++
			d.tracks = append(d.tracks, &track{id: d.nextID, first: d.hop, last: d.hop, hits: 1, history: []obs{o}})
			used = append(used, true)
			continue
		}
		t := d.tracks[best]
		used[best] = true
		t.last, t.hits = d.hop, t.hits+1
		t.history = append(t.history, o)
		if keep := d.hopsFor(historySeconds); len(t.history) > keep {
			t.history = append(t.history[:0], t.history[len(t.history)-keep:]...)
		}
	}
	live := d.tracks[:0]
	for _, t := range d.tracks {
		if d.hop-t.last <= maxMissedHops {
			live = append(live, t)
		}
	}
	d.tracks = live
}

// Candidates returns current ringing candidates, highest confidence first.
func (d *Detector) Candidates() []Candidate {
	var out []Candidate
	current := d.hop - 1
	for _, t := range d.tracks {
		if current-t.last > 1 {
			continue
		}
		if c, ok := d.evaluate(t); ok {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].LevelDBFS > out[j].LevelDBFS
	})
	return out
}

func (d *Detector) evaluate(t *track) (Candidate, bool) {
	span := t.last - t.first + 1
	duration := d.seconds(span)
	persistence := float64(t.hits) / float64(span)
	if duration < minDurationSeconds || persistence < minPersistence {
		return Candidate{}, false
	}
	var meanF float64
	for _, o := range t.history {
		meanF += o.freq
	}
	meanF /= float64(len(t.history))
	var varF float64
	harmonicHops := 0
	for _, o := range t.history {
		varF += (o.freq - meanF) * (o.freq - meanF)
		if o.harmonicContext {
			harmonicHops++
		}
	}
	stdF := math.Sqrt(varF / float64(len(t.history)))
	if stdF > math.Max(maxFrequencyStdRatio*meanF, 0.5*d.binHz()) {
		return Candidate{}, false
	}
	last := t.history[len(t.history)-1]
	growth := d.growth(t)
	harmonic := float64(harmonicHops)/float64(len(t.history)) >= harmonicFraction
	c := Candidate{
		ID:                t.id,
		FrequencyHz:       meanF,
		LevelDBFS:         last.level,
		ProminenceDB:      last.prom,
		BandwidthHz:       last.bw,
		DurationSeconds:   duration,
		Persistence:       persistence,
		FrequencyStdHz:    stdF,
		GrowthDBPerSecond: growth,
		Growing:           growth >= GrowingDBPerSecond,
		Harmonic:          harmonic,
	}
	conf := 0.6 + 0.2*math.Max(0, math.Min(1, (last.prom-MinProminenceDB)/20))
	c.Evidence = append(c.Evidence,
		fmt.Sprintf("stable to %.1f Hz for %.1f s", stdF, duration),
		fmt.Sprintf("%.0f dB above its neighbourhood at %.0f dBFS", last.prom, last.level))
	if c.Growing {
		conf += 0.2
		c.Evidence = append(c.Evidence, fmt.Sprintf("growing %.1f dB/s", growth))
	}
	if harmonic {
		conf *= 0.4
		c.Evidence = append(c.Evidence, "has harmonic partners (likely voice or instrument)")
	}
	c.Confidence = math.Min(1, conf)
	return c, true
}

// growth is the least-squares level slope over the recent window, in dB/s.
func (d *Detector) growth(t *track) float64 {
	from := d.hop - 1 - d.hopsFor(growthWindowSeconds)
	var n, sx, sy, sxx, sxy float64
	for _, o := range t.history {
		if o.hop < from {
			continue
		}
		x := d.seconds(o.hop)
		n++
		sx += x
		sy += o.level
		sxx += x * x
		sxy += x * o.level
	}
	den := n*sxx - sx*sx
	if n < 3 || den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}
