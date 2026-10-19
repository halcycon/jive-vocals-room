package main

import (
	"bytes"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxmatters/jive-vocals/internal/room"
)

func TestRingOutFileFindsSyntheticRing(t *testing.T) {
	const rate = 44100
	samples := make([]float64, 3*rate)
	for i := range samples {
		tt := float64(i) / rate
		samples[i] = math.Pow(10, (-40+12*tt)/20)*math.Sin(2*math.Pi*1250*tt) + 1e-3*math.Sin(2*math.Pi*97*tt*tt)
	}
	path := filepath.Join(t.TempDir(), "ring.wav")
	if err := room.WriteWAV(path, room.PCM{Samples: samples, SampleRate: rate, Channels: 1}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runRingOutFile(&out, path); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"1.25 kHz", "growing", "[ring]", "propose: GEQ 1.30 kHz band -3 dB (raw mapping not calibrated)", "never changes gain"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestRingOutLiveNeedsChannel(t *testing.T) {
	if err := run([]string{"-studiolive-ringout", "-jack-source", "_Ch 1_in"}); !isUsage(err) {
		t.Fatalf("got %v", err)
	}
}
