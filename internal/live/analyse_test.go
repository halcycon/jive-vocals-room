package live

import (
	"errors"
	"math"
	"testing"

	"github.com/linuxmatters/jive-vocals/internal/live/rt"
	"github.com/linuxmatters/jive-vocals/internal/room"
)

func TestFakeProducerDrivesAnalyser(t *testing.T) {
	const (
		rate     = 48000
		frames   = 8192 * 2
		channels = 1
	)
	r, err := rt.NewRing(16384, channels)
	if err != nil {
		t.Fatal(err)
	}
	tone := make([]float32, frames)
	for i := range tone {
		tone[i] = float32(0.2 * math.Sin(2*math.Pi*1000*float64(i)/rate))
	}
	if n := r.Write(tone); n != frames {
		t.Fatalf("wrote %d frames", n)
	}
	var acc []float64
	overruns := DrainRing(r, &acc)
	if overruns != 0 {
		t.Fatalf("overruns=%d", overruns)
	}
	snap := SnapshotFromPCM(room.PCM{Samples: acc, SampleRate: rate, Channels: channels, Source: "fake-ring"}, Telemetry{
		SampleRateHz: rate, Channels: channels, QuantumFrames: 128, EngineHealthy: true, ProcessedSamples: uint64(len(acc)),
	})
	if !snap.HasSpectrum || snap.RMSDB < -30 {
		t.Fatalf("snapshot=%+v", snap)
	}
	if snap.SpectralFlatness <= 0 {
		t.Fatalf("expected a measured flatness, got %+v", snap)
	}
	loud, ok := LoudestBand(snap.Bands)
	if !ok || loud.FrequencyHz != 1000 {
		t.Fatalf("expected 1 kHz to be the loudest 15-band centre, got %+v", loud)
	}
	if r.Overruns() != 0 {
		t.Fatal("analysis drop counted as ring overrun after a successful drain")
	}
}

func TestOpenInputAnalyserDisabledByDefault(t *testing.T) {
	if PipeWireBuilt() {
		t.Skip("PipeWire capture is linked in this build")
	}
	_, err := OpenInputAnalyser()
	if !errors.Is(err, ErrPipeWireDisabled) {
		t.Fatalf("got %v", err)
	}
}
