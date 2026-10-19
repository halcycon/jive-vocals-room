//go:build jack

package live

import (
	"context"
	"os"
	"testing"
	"time"
)

// Set JIVE_LIVE_JACK to a physical capture port name or suffix, e.g. "_Ch 1_in",
// with jackd running for this user. The client only registers input ports.
func TestJACKHardwareCaptureOptional(t *testing.T) {
	source := os.Getenv("JIVE_LIVE_JACK")
	if source == "" {
		t.Skip("optional JACK hardware test; set JIVE_LIVE_JACK to a capture port suffix")
	}
	a, err := OpenInputAnalyser(InputOptions{Backend: BackendJACK, JACKSources: []string{source}, JACKClientName: "jive-room-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan Snapshot, 1)
	if err := a.Start(ctx, func(s Snapshot) {
		if s.HasSpectrum {
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
		if s.SampleRateHz <= 0 || s.QuantumFrames <= 0 || !s.EngineHealthy {
			t.Fatalf("telemetry %+v", s.Telemetry)
		}
	case <-ctx.Done():
		t.Fatal("no JACK spectrum snapshot")
	}
}
