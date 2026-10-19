//go:build pipewire

package live

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestOpenInputAnalyserLinks(t *testing.T) {
	a, err := OpenInputAnalyser()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPipeWireHardwareCaptureOptional(t *testing.T) {
	if os.Getenv("JIVE_LIVE_PW") != "1" {
		t.Skip("optional PipeWire hardware test; set JIVE_LIVE_PW=1")
	}
	a, err := OpenInputAnalyser()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := make(chan Snapshot, 1)
	if err := a.Start(ctx, func(s Snapshot) {
		if s.SampleRateHz > 0 {
			select {
			case got <- s:
			default:
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s.QuantumFrames < 0 {
			t.Fatalf("negative quantum: %+v", s.Telemetry)
		}
	case <-ctx.Done():
		t.Fatal("no live snapshot with a negotiated sample rate")
	}
}
