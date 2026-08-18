package room

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
)

const (
	pinkKind            = "band_limited_pink_noise"
	pinkWarmupSamples   = 8192
	pinkFilterQ         = 0.707
	minPinkDurationSec  = 1.0
	maxPinkDurationSec  = 30.0
	minPinkHeadroomDB   = 3.0
	maxPinkHeadroomDB   = 24.0
	minPinkSampleRateHz = 8000
	maxPinkSampleRateHz = 96000
)

func DefaultPinkSpec() TestSignalSpec {
	return TestSignalSpec{
		Kind:            pinkKind,
		SampleRateHz:    48000,
		DurationSeconds: 10,
		HeadroomDB:      12,
		FadeInSeconds:   0.1,
		FadeOutSeconds:  0.1,
		Seed:            1,
		HighPassHz:      40,
		LowPassHz:       16000,
	}
}

func (s TestSignalSpec) Validate() error {
	if s.Kind != "" && s.Kind != pinkKind {
		return fmt.Errorf("unsupported test signal %q", s.Kind)
	}
	if s.SampleRateHz < minPinkSampleRateHz || s.SampleRateHz > maxPinkSampleRateHz {
		return fmt.Errorf("sample rate %d Hz is outside %d–%d Hz", s.SampleRateHz, minPinkSampleRateHz, maxPinkSampleRateHz)
	}
	if s.DurationSeconds < minPinkDurationSec || s.DurationSeconds > maxPinkDurationSec {
		return fmt.Errorf("duration %.3f s is outside %.0f–%.0f s", s.DurationSeconds, minPinkDurationSec, maxPinkDurationSec)
	}
	if s.HeadroomDB < minPinkHeadroomDB || s.HeadroomDB > maxPinkHeadroomDB {
		return fmt.Errorf("headroom %.1f dB is outside %.0f–%.0f dB", s.HeadroomDB, minPinkHeadroomDB, maxPinkHeadroomDB)
	}
	if s.FadeInSeconds < 0 || s.FadeOutSeconds < 0 || s.FadeInSeconds+s.FadeOutSeconds >= s.DurationSeconds {
		return fmt.Errorf("fade in/out must be non-negative and shorter than the duration")
	}
	nyquist := float64(s.SampleRateHz) / 2
	if s.HighPassHz <= 0 || s.LowPassHz <= s.HighPassHz || s.LowPassHz >= nyquist {
		return fmt.Errorf("band limits %.1f–%.1f Hz are invalid for sample rate %d Hz", s.HighPassHz, s.LowPassHz, s.SampleRateHz)
	}
	return nil
}

// GeneratePink synthesises band-limited pink noise. It never plays audio or
// changes a PA gain; callers write a file and the operator sets playback level.
func GeneratePink(spec TestSignalSpec) (PCM, error) {
	if spec.Kind == "" {
		spec.Kind = pinkKind
	}
	if err := spec.Validate(); err != nil {
		return PCM{}, err
	}
	n := int(math.Round(spec.DurationSeconds * float64(spec.SampleRateHz)))
	rng := rand.New(rand.NewPCG(uint64(spec.Seed), uint64(spec.Seed)^0x9e3779b97f4a7c15))
	pink := pinkFilter{}
	hpf := rbjHighPass(float64(spec.SampleRateHz), spec.HighPassHz, pinkFilterQ)
	hpf2 := rbjHighPass(float64(spec.SampleRateHz), spec.HighPassHz, pinkFilterQ)
	lpf := rbjLowPass(float64(spec.SampleRateHz), spec.LowPassHz, pinkFilterQ)
	lpf2 := rbjLowPass(float64(spec.SampleRateHz), spec.LowPassHz, pinkFilterQ)
	for range pinkWarmupSamples {
		x := pink.tick(rng.NormFloat64())
		_ = lpf2.process(lpf.process(hpf2.process(hpf.process(x))))
	}
	samples := make([]float64, n)
	var peak float64
	for i := range samples {
		x := lpf2.process(lpf.process(hpf2.process(hpf.process(pink.tick(rng.NormFloat64())))))
		samples[i] = x
		peak = max(peak, math.Abs(x))
	}
	if peak > 0 {
		target := math.Pow(10, -spec.HeadroomDB/20)
		scale := target / peak
		for i := range samples {
			samples[i] *= scale
		}
	}
	fade(samples, spec.SampleRateHz, spec.FadeInSeconds, spec.FadeOutSeconds)
	return PCM{Samples: samples, SampleRate: spec.SampleRateHz, Channels: 1, Source: spec.Kind}, nil
}

func fade(samples []float64, rate int, fadeInSec, fadeOutSec float64) {
	inN := min(len(samples), int(math.Round(fadeInSec*float64(rate))))
	outN := min(len(samples), int(math.Round(fadeOutSec*float64(rate))))
	for i := range inN {
		w := 0.5 - 0.5*math.Cos(math.Pi*float64(i)/float64(max(inN, 1)))
		samples[i] *= w
	}
	for i := range outN {
		idx := len(samples) - 1 - i
		w := 0.5 - 0.5*math.Cos(math.Pi*float64(i)/float64(max(outN, 1)))
		samples[idx] *= w
	}
}

// Paul Kellet's refined pink-noise filter. Coefficients are a close 44.1/48 kHz
// approximation; explicit HPF/LPF set the measurement band independently.
type pinkFilter struct {
	b0, b1, b2, b3, b4, b5, b6 float64
}

func (p *pinkFilter) tick(white float64) float64 {
	p.b0 = 0.99886*p.b0 + white*0.0555179
	p.b1 = 0.99332*p.b1 + white*0.0750759
	p.b2 = 0.96900*p.b2 + white*0.1538520
	p.b3 = 0.86650*p.b3 + white*0.3104856
	p.b4 = 0.55000*p.b4 + white*0.5329522
	p.b5 = -0.7616*p.b5 - white*0.0168980
	out := p.b0 + p.b1 + p.b2 + p.b3 + p.b4 + p.b5 + p.b6 + white*0.5362
	p.b6 = white * 0.115926
	return out
}

func WriteWAV(path string, pcm PCM) error {
	if pcm.SampleRate <= 0 || pcm.Channels <= 0 {
		return fmt.Errorf("invalid PCM metadata")
	}
	samples := pcm.Samples
	channels := pcm.Channels
	if channels != 1 {
		samples = downmix(samples, channels)
		channels = 1
	}
	pcm16 := make([]int16, len(samples))
	for i, x := range samples {
		x = max(-1, min(1, x))
		pcm16[i] = int16(math.Round(x * 32767))
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dataBytes := uint32(len(pcm16) * 2) //nolint:gosec // generated PCM length is bounded by Validate
	if _, err = f.Write([]byte("RIFF")); err != nil {
		return err
	}
	for _, v := range []any{
		uint32(36) + dataBytes,
		[4]byte{'W', 'A', 'V', 'E'},
		[4]byte{'f', 'm', 't', ' '},
		uint32(16),
		uint16(1),
		uint16(channels),
		uint32(pcm.SampleRate),
		uint32(pcm.SampleRate * 2 * channels),
		uint16(2 * channels),
		uint16(16),
		[4]byte{'d', 'a', 't', 'a'},
		dataBytes,
	} {
		if err = binary.Write(f, binary.LittleEndian, v); err != nil {
			return err
		}
	}
	return binary.Write(f, binary.LittleEndian, pcm16)
}

func SignalSpecPath(wavPath string) string {
	return wavPath[:len(wavPath)-len(filepath.Ext(wavPath))] + ".signal.json"
}

func WritePinkWAV(path string, spec TestSignalSpec) (PCM, error) {
	pcm, err := GeneratePink(spec)
	if err != nil {
		return PCM{}, err
	}
	pcm.Source = path
	if err = WriteWAV(path, pcm); err != nil {
		return PCM{}, err
	}
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return PCM{}, err
	}
	return pcm, os.WriteFile(SignalSpecPath(path), append(b, '\n'), 0o644)
}

func LoadTestSignalSpec(wavPath string) (*TestSignalSpec, error) {
	path := SignalSpecPath(wavPath)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var spec TestSignalSpec
	if err = json.Unmarshal(b, &spec); err != nil {
		return nil, err
	}
	return &spec, nil
}
