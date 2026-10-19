//go:build jack

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxmatters/jive-vocals/internal/roomfile"
)

// These run against a dummy JACK server (`just test-jack-dummy`): its
// system:capture_* ports are silent and system:playback_* go nowhere.

func requireDummy(t *testing.T) {
	t.Helper()
	if os.Getenv("JIVE_JACK_DUMMY") != "1" {
		t.Skip("needs a dummy JACK server; set JIVE_JACK_DUMMY=1")
	}
}

func TestStageCaptureOnDummyJACK(t *testing.T) {
	requireDummy(t)
	dir := t.TempDir()
	err := run([]string{"-studiolive-capture", "empty", "-jack-source", "system:capture_1", "-capture-seconds", "5", "-session-dir", dir, "-studiolive-bridge", "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	pcm, err := roomfile.Decode(filepath.Join(dir, "empty.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(pcm.Samples), 5*pcm.SampleRate; got != want {
		t.Fatalf("captured %d samples, want exactly %d", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "empty.capture.json")); err != nil {
		t.Fatal(err)
	}
}

func TestPAMeasureOnDummyJACK(t *testing.T) {
	requireDummy(t)
	dir := t.TempDir()
	err := run([]string{
		"-studiolive-pa-measure", "-jack-source", "system:capture_1", "-play-pink-to", "system:playback_1",
		"-confirm-playback", "-pink-duration", "4", "-session-dir", dir, "-studiolive-bridge", "127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := roomfile.Decode(filepath.Join(dir, "pa-reference.wav"))
	if err != nil {
		t.Fatal(err)
	}
	meas, err := roomfile.Decode(filepath.Join(dir, "pa-measured.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ref.Samples) != 4*ref.SampleRate {
		t.Fatalf("reference %d samples", len(ref.Samples))
	}
	// Arming and stopping follow the 200 ms consumer tick, so the window may
	// start a tick late or stop a tick early, but never extends into the fades.
	steady := len(ref.Samples) - int(0.7*float64(ref.SampleRate))
	if len(meas.Samples) < steady-ref.SampleRate/2 || len(meas.Samples) > steady+4096 {
		t.Fatalf("measured %d samples, want up to about %d (steady part only)", len(meas.Samples), steady)
	}
	for _, f := range []string{"pa-reference.signal.json", "pa-measured.capture.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
}
