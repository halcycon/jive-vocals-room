// Package liverec produces slow, display-only live recommendations for one
// voice channel. It never writes to a mixer. It is pure, non-real-time Go.
//
// The input is the channel's FireWire send, which on the StudioLive is pre-DSP:
// the desk's own HPF/EQ is not in the signal, so desk settings are only
// consulted (through the caller's suppression hooks) to avoid repeating advice
// the desk already follows.
//
// Evidence comes from a long-term average spectrum (LTAS) of active frames
// only. Each band is compared with the channel's own neighbourhood rather than
// an external reference curve. Thresholds are justified by synthetic signals
// only and must be validated against recorded voices before being trusted.
package liverec

import (
	"fmt"
	"math"
	"sort"

	"github.com/linuxmatters/jive-vocals/internal/room"
)

const (
	frameSize = 8192
	hopSize   = 4096

	// Activity gate: a frame counts when it is this far above the channel's
	// own recent noise floor and above an absolute level.
	activeAboveFloorDB = 10.0
	activeMinDBFS      = -70.0
	floorPercentile    = 0.10
	historySeconds     = 30.0

	// MinActiveSeconds of active signal before any advice.
	MinActiveSeconds = 5.0

	// HPF: energy 40–90 Hz within this many dB of the 150–400 Hz voice body.
	lowEnergyWithinDB = 10.0
	// Resonance: a 1/3-octave band this far above the mean of the lower and
	// upper pairs of bands 2/3–1 octave away (direct neighbours excluded), and
	// above each pair on its own. The search
	// starts above voice fundamentals and second harmonics (up to ~400 Hz),
	// where the band holding f0 always towers over the empty octave below it.
	resonanceAboveDB = 5.0
	resonanceLowHz   = 400.0
	resonanceHighHz  = 5000.0
	// Sibilance: 6–9 kHz relative to 1–3 kHz in the LTAS. Jive Vocals' de-esser
	// starts engaging at -6 dB and caps at 0 dB on the same band split.
	sibilanceNoticeDB = -6.0
	sibilanceStrongDB = 0.0
	clipPeakDBFS      = -1.0
	lowLevelDBFS      = -45.0

	// HoldSeconds a condition must hold before it is shown, and clear before it
	// is removed, so advice does not chase short-term fluctuations.
	HoldSeconds = 10.0
)

// Kind identifies a recommendation family.
type Kind string

const (
	KindHPF       Kind = "hpf"
	KindResonance Kind = "resonance"
	KindSibilance Kind = "sibilance"
	KindClipping  Kind = "clipping"
	KindLowLevel  Kind = "low_level"
)

// Recommendation is display-only advice with its evidence.
type Recommendation struct {
	Kind        Kind
	FrequencyHz float64
	Summary     string
	Evidence    string
}

// Desk lets the caller suppress advice the desk already follows. Any field
// may be nil.
type Desk struct {
	// HPFHz returns the HPF corner when the HPF is on.
	HPFHz func() (float64, bool)
	// CutNear reports an existing EQ cut of at least 1.5 dB within 1/3 octave.
	CutNear func(hz float64) bool
}

type frame struct {
	bands  []float64 // power per 1/3-octave band
	level  float64   // 100 Hz–8 kHz level, dBFS (sine-calibrated)
	peakDB float64
}

type held struct {
	rec      Recommendation
	trueFor  float64
	falseFor float64
	shown    bool
}

// Recommender accumulates a channel's signal and produces recommendations.
type Recommender struct {
	rate     int
	window   []float64
	winNorm  float64
	buf      []complex128
	pending  []float64
	frames   []frame
	edges    [][2]int
	centres  []float64
	hold     map[string]*held
	elapsed  float64
	lastEval float64
}

// Centres are ISO 1/3-octave band centres from 31.5 Hz to 16 kHz.
var Centres = []float64{
	31.5, 40, 50, 63, 80, 100, 125, 160, 200, 250, 315, 400, 500, 630, 800,
	1000, 1250, 1600, 2000, 2500, 3150, 4000, 5000, 6300, 8000, 10000, 12500, 16000,
}

func New(rateHz int) (*Recommender, error) {
	if rateHz < 32000 {
		return nil, fmt.Errorf("live recommendations need at least 32 kHz, got %d", rateHz)
	}
	r := &Recommender{rate: rateHz, window: make([]float64, frameSize), buf: make([]complex128, frameSize), hold: map[string]*held{}}
	var sum float64
	for i := range r.window {
		r.window[i] = .5 - .5*math.Cos(2*math.Pi*float64(i)/float64(frameSize-1))
		sum += r.window[i]
	}
	r.winNorm = 2 / sum
	bin := float64(rateHz) / frameSize
	for _, c := range Centres {
		lo, hi := c/math.Pow(2, 1.0/6), c*math.Pow(2, 1.0/6)
		a, b := max(1, int(math.Round(lo/bin))), min(frameSize/2, int(math.Round(hi/bin)))
		r.edges = append(r.edges, [2]int{a, max(a+1, b)})
		r.centres = append(r.centres, c)
	}
	return r, nil
}

func (r *Recommender) SampleRate() int { return r.rate }

func (r *Recommender) hopSeconds() float64 { return float64(hopSize) / float64(r.rate) }

// Push consumes mono samples.
func (r *Recommender) Push(samples []float64) {
	r.pending = append(r.pending, samples...)
	for len(r.pending) >= frameSize {
		r.analyse(r.pending[:frameSize])
		r.pending = r.pending[hopSize:]
		r.elapsed += r.hopSeconds()
	}
	if cap(r.pending) > 4*frameSize {
		r.pending = append([]float64(nil), r.pending...)
	}
}

func (r *Recommender) analyse(x []float64) {
	var peak float64
	for i := range frameSize {
		r.buf[i] = complex(x[i]*r.window[i], 0)
		peak = math.Max(peak, math.Abs(x[i]))
	}
	room.FFT(r.buf)
	f := frame{bands: make([]float64, len(r.edges)), peakDB: db(peak)}
	bin := float64(r.rate) / frameSize
	var body float64
	for i := 1; i <= frameSize/2; i++ {
		a := math.Hypot(real(r.buf[i]), imag(r.buf[i])) * r.winNorm
		p := a * a / 2
		if hz := float64(i) * bin; hz >= 100 && hz <= 8000 {
			body += p
		}
	}
	for k, e := range r.edges {
		var s float64
		for i := e[0]; i < e[1]; i++ {
			a := math.Hypot(real(r.buf[i]), imag(r.buf[i])) * r.winNorm
			s += a * a / 2
		}
		f.bands[k] = s
	}
	f.level = 10*math.Log10(math.Max(body, 1e-14)) + 3.0103 // sine-calibrated: RMS power to peak dBFS
	r.frames = append(r.frames, f)
	if keep := int(historySeconds / r.hopSeconds()); len(r.frames) > keep {
		r.frames = append(r.frames[:0], r.frames[len(r.frames)-keep:]...)
	}
}

func db(v float64) float64  { return 20 * math.Log10(math.Max(v, 1e-7)) }
func pdb(p float64) float64 { return 10 * math.Log10(math.Max(p, 1e-14)) }

// Status summarises how much signal has been heard.
type Status struct {
	ActiveSeconds float64
	FloorDBFS     float64
	Ready         bool
}

func (r *Recommender) active() ([]frame, float64) {
	if len(r.frames) == 0 {
		return nil, math.Inf(-1)
	}
	levels := make([]float64, len(r.frames))
	for i, f := range r.frames {
		levels[i] = f.level
	}
	sort.Float64s(levels)
	floor := levels[int(floorPercentile*float64(len(levels)-1))]
	var out []frame
	for _, f := range r.frames {
		if f.level >= floor+activeAboveFloorDB && f.level >= activeMinDBFS {
			out = append(out, f)
		}
	}
	return out, floor
}

func (r *Recommender) Status() Status {
	act, floor := r.active()
	s := Status{ActiveSeconds: float64(len(act)) * r.hopSeconds(), FloorDBFS: floor}
	s.Ready = s.ActiveSeconds >= MinActiveSeconds
	return s
}

func (r *Recommender) band(ltas []float64, lo, hi float64) float64 {
	var s float64
	n := 0
	for k, c := range r.centres {
		if c >= lo && c <= hi {
			s += ltas[k]
			n++
		}
	}
	if n == 0 {
		return math.Inf(-1)
	}
	return pdb(s)
}

// candidates evaluates the current LTAS without hold or desk suppression.
func (r *Recommender) candidates(act []frame) []Recommendation {
	var out []Recommendation
	if len(act) == 0 {
		return out
	}
	ltas := make([]float64, len(r.centres))
	peak := math.Inf(-1)
	levels := make([]float64, 0, len(act))
	for _, f := range act {
		for k, p := range f.bands {
			ltas[k] += p / float64(len(act))
		}
		peak = math.Max(peak, f.peakDB)
		levels = append(levels, f.level)
	}
	for _, f := range r.frames {
		peak = math.Max(peak, f.peakDB)
	}
	sort.Float64s(levels)
	median := levels[len(levels)/2]

	if peak >= clipPeakDBFS {
		out = append(out, Recommendation{
			Kind: KindClipping, Summary: "input is at or near clipping: lower the channel's input gain on the desk",
			Evidence: fmt.Sprintf("peak %.1f dBFS in the last %.0f s", peak, historySeconds),
		})
	} else if median < lowLevelDBFS {
		out = append(out, Recommendation{
			Kind: KindLowLevel, Summary: "active signal is low: consider more input gain for headroom-friendly gain staging",
			Evidence: fmt.Sprintf("median active level %.1f dBFS", median),
		})
	}

	low, body := r.band(ltas, 40, 90), r.band(ltas, 150, 400)
	if low > body-lowEnergyWithinDB {
		out = append(out, Recommendation{
			Kind: KindHPF, FrequencyHz: 100,
			Summary:  "low-frequency energy below the voice: try the channel HPF around 100 Hz",
			Evidence: fmt.Sprintf("40–90 Hz is %.1f dB relative to the 150–400 Hz voice body", low-body),
		})
	}

	best, bestK := 0.0, -1
	for k, c := range r.centres {
		if c < resonanceLowHz || c > resonanceHighHz {
			continue
		}
		if k-3 < 0 || k+3 >= len(ltas) {
			continue
		}
		// The mean of the two sides is the log-frequency interpolation, so a
		// spectral slope cancels. The band must also beat each side on its own:
		// otherwise any band beside a roll-off or band edge looks like a peak.
		lower := (pdb(ltas[k-3]) + pdb(ltas[k-2])) / 2
		upper := (pdb(ltas[k+2]) + pdb(ltas[k+3])) / 2
		level := pdb(ltas[k])
		if level <= lower || level <= upper {
			continue
		}
		if ex := level - (lower+upper)/2; ex >= resonanceAboveDB && ex > best {
			best, bestK = ex, k
		}
	}
	if bestK >= 0 {
		c := r.centres[bestK]
		out = append(out, Recommendation{
			Kind: KindResonance, FrequencyHz: c,
			Summary:  fmt.Sprintf("resonance around %s: try a 2–3 dB EQ cut there and listen", hz(c)),
			Evidence: fmt.Sprintf("%.1f dB above the trend of the bands 2/3–1 octave either side", best),
		})
	}

	if sib := r.band(ltas, 6000, 9000) - r.band(ltas, 1000, 3000); sib >= sibilanceNoticeDB {
		strength := "noticeable"
		if sib >= sibilanceStrongDB {
			strength = "strong"
		}
		out = append(out, Recommendation{
			Kind: KindSibilance, FrequencyHz: 7000,
			Summary:  fmt.Sprintf("%s sibilance: the desk has no de-esser; try a gentle 1–2 dB high-band cut near 7 kHz, or move the mic off-axis", strength),
			Evidence: fmt.Sprintf("6–9 kHz is %.1f dB relative to 1–3 kHz", sib),
		})
	}
	return out
}

func hz(f float64) string {
	if f >= 1000 {
		return fmt.Sprintf("%.2g kHz", f/1000)
	}
	return fmt.Sprintf("%.0f Hz", f)
}

func key(r Recommendation) string {
	if r.Kind == KindResonance {
		return fmt.Sprintf("%s@%.0f", r.Kind, r.FrequencyHz)
	}
	return string(r.Kind)
}

// Recommendations returns the advice that has held for HoldSeconds, minus
// what the desk already follows. Call it about once a second; hold timers use
// the signal time pushed so far.
func (r *Recommender) Recommendations(desk Desk) []Recommendation {
	dt := r.elapsed - r.lastEval
	r.lastEval = r.elapsed
	act, _ := r.active()
	if float64(len(act))*r.hopSeconds() < MinActiveSeconds {
		dt = 0
	}
	now := map[string]Recommendation{}
	if dt > 0 {
		for _, c := range r.candidates(act) {
			if !suppressed(c, desk) {
				now[key(c)] = c
			}
		}
	}
	for k, c := range now {
		h := r.hold[k]
		if h == nil {
			h = &held{}
			r.hold[k] = h
		}
		h.rec, h.trueFor, h.falseFor = c, h.trueFor+dt, 0
		if h.trueFor >= HoldSeconds {
			h.shown = true
		}
	}
	for k, h := range r.hold {
		if _, ok := now[k]; ok {
			continue
		}
		h.trueFor = 0
		if !h.shown {
			delete(r.hold, k)
			continue
		}
		if h.falseFor += dt; h.falseFor >= HoldSeconds {
			delete(r.hold, k)
		}
	}
	var out []Recommendation
	for _, h := range r.hold {
		if h.shown {
			out = append(out, h.rec)
		}
	}
	order := map[Kind]int{KindClipping: 0, KindLowLevel: 1, KindHPF: 2, KindResonance: 3, KindSibilance: 4}
	sort.Slice(out, func(i, j int) bool {
		if order[out[i].Kind] != order[out[j].Kind] {
			return order[out[i].Kind] < order[out[j].Kind]
		}
		return out[i].FrequencyHz < out[j].FrequencyHz
	})
	return out
}

func suppressed(c Recommendation, d Desk) bool {
	switch c.Kind {
	case KindHPF:
		if d.HPFHz != nil {
			if f, on := d.HPFHz(); on && f >= 80 {
				return true
			}
		}
	case KindResonance, KindSibilance:
		if d.CutNear != nil && d.CutNear(c.FrequencyHz) {
			return true
		}
	}
	return false
}
