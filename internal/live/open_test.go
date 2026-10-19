package live

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/live/rt"
)

var studioLivePorts = func() []string {
	var p []string
	for i := 1; i <= 16; i++ {
		p = append(p, "firewire_pcm:000a920404e1443c_Ch "+strconv.Itoa(i)+"_in")
	}
	return p
}()

func TestResolveJACKSourcesSuffixDoesNotMatchLongerChannel(t *testing.T) {
	got, err := ResolveJACKSources(studioLivePorts, []string{"_Ch 1_in", "_Ch 11_in"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"firewire_pcm:000a920404e1443c_Ch 1_in", "firewire_pcm:000a920404e1443c_Ch 11_in"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveJACKSourcesExactAndErrors(t *testing.T) {
	exact := "firewire_pcm:000a920404e1443c_Ch 3_in"
	if got, err := ResolveJACKSources(studioLivePorts, []string{exact}); err != nil || got[0] != exact {
		t.Fatalf("exact: %v %v", got, err)
	}
	if _, err := ResolveJACKSources(studioLivePorts, []string{"_Ch 17_in"}); err == nil {
		t.Fatal("missing port accepted")
	}
	if _, err := ResolveJACKSources(studioLivePorts, []string{"_in"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous suffix: %v", err)
	}
	if _, err := ResolveJACKSources(studioLivePorts, []string{""}); err == nil {
		t.Fatal("empty source accepted")
	}
}

func TestOpenInputAnalyserBackendSelection(t *testing.T) {
	if _, err := OpenInputAnalyser(InputOptions{Backend: "alsa"}); err == nil {
		t.Fatal("unknown backend accepted")
	}
	if _, err := OpenInputAnalyser(InputOptions{Backend: BackendJACK}); err == nil {
		t.Fatal("jack without sources accepted")
	}
	if JACKBuilt() {
		return
	}
	_, err := OpenInputAnalyser(InputOptions{Backend: BackendJACK, JACKSources: []string{"_Ch 1_in"}})
	if !errors.Is(err, ErrJACKDisabled) {
		t.Fatalf("got %v", err)
	}
}

type fakeSource struct {
	ring    *rt.Ring
	started atomic.Bool
	closed  atomic.Bool
}

func (f *fakeSource) Start() error    { f.started.Store(true); return nil }
func (f *fakeSource) Ring() *rt.Ring  { return f.ring }
func (f *fakeSource) Rate() int       { return 44100 }
func (f *fakeSource) Quantum() int    { return 1024 }
func (f *fakeSource) Channels() int   { return f.ring.Channels() }
func (f *fakeSource) XRuns() uint64   { return 0 }
func (f *fakeSource) Samples() uint64 { return 0 }
func (f *fakeSource) Healthy() bool   { return true }
func (f *fakeSource) Close() error    { f.closed.Store(true); return nil }

// feedSnapshots writes frames in quantum-sized chunks, taking a snapshot after
// each, as the 200 ms consumer would. It returns the last snapshot.
func feedSnapshots(t *testing.T, channels, frames int, sample func(i int) float64) Snapshot {
	t.Helper()
	ring, err := rt.NewRing(DefaultRingFrames, channels)
	if err != nil {
		t.Fatal(err)
	}
	a := newRingAnalyser(&fakeSource{ring: ring}, "fake")
	const chunk = 4096
	var acc []float64
	var last Snapshot
	buf := make([]float32, chunk*channels)
	for start := 0; start < frames; start += chunk {
		for i := range chunk {
			v := float32(sample(start + i))
			for c := range channels {
				buf[i*channels+c] = v
			}
		}
		ring.Write(buf)
		last = a.snapshot(&acc)
	}
	return last
}

func TestRingAnalyserStereoAt44100(t *testing.T) {
	const rate = 44100
	s := feedSnapshots(t, 2, 2*liveWindowFrames, func(i int) float64 {
		return 0.25 * math.Sin(2*math.Pi*1000*float64(i)/rate)
	})
	if !s.HasSpectrum || s.SampleRateHz != rate || s.Channels != 2 || !s.EngineHealthy || s.QuantumFrames != 1024 {
		t.Fatalf("snapshot %+v", s.Telemetry)
	}
	if b, ok := LoudestBand(s.Bands); !ok || b.FrequencyHz != 1000 {
		t.Fatalf("loudest band %+v", b)
	}
	if math.Abs(s.TonalHz-1000) > 10 {
		t.Fatalf("1 kHz tone not reported: %.1f Hz", s.TonalHz)
	}
}

func TestRingAnalyserNoiseFloorIsNotTonal(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test noise
	s := feedSnapshots(t, 1, 2*liveWindowFrames, func(int) float64 {
		return 2e-6 * rng.NormFloat64()
	})
	if !s.HasSpectrum {
		t.Fatal("no spectrum")
	}
	if s.TonalHz != 0 {
		t.Fatalf("noise floor reported as a %.0f Hz tone (+%.1f dB)", s.TonalHz, s.TonalProminenceDB)
	}
}

func TestRingAnalyserLifecycle(t *testing.T) {
	ring, err := rt.NewRing(DefaultRingFrames, 1)
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{ring: ring}
	a := newRingAnalyser(src, "fake")
	got := make(chan Snapshot, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.Start(ctx, func(s Snapshot) {
		select {
		case got <- s:
		default:
		}
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s.SampleRateHz != 44100 || !s.EngineHealthy {
			t.Fatalf("telemetry %+v", s.Telemetry)
		}
	case <-ctx.Done():
		t.Fatal("no snapshot")
	}
	if err := a.Close(); err != nil || !src.started.Load() || !src.closed.Load() {
		t.Fatalf("lifecycle: err=%v started=%v closed=%v", err, src.started.Load(), src.closed.Load())
	}
}
