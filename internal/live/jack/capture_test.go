//go:build jack

package jack

import (
	"os"
	"testing"
	"time"
)

// Needs a running JACK server, e.g. `jackd --no-realtime -d dummy -r 44100 -p 1024`
// (see `just test-jack-dummy`). The test wires its own out_1 to its own in_1,
// so nothing physical is touched.
func requireDummyJACK(t *testing.T) {
	t.Helper()
	if os.Getenv("JIVE_JACK_DUMMY") != "1" {
		t.Skip("needs a dummy JACK server; set JIVE_JACK_DUMMY=1")
	}
}

func TestPlaybackLoopbackIsSampleExact(t *testing.T) {
	requireDummyJACK(t)
	c, err := NewCapture("jive-room-loopback", 1<<16, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.EnablePlayback(); err != nil {
		t.Fatal(err)
	}
	const n = 20000
	signal := make([]float32, n)
	for i := range signal {
		signal[i] = float32(i+1) / n
	}
	if err := c.LoadPlayback(signal); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadPlayback(signal); err == nil {
		t.Fatal("second load accepted")
	}
	if err := c.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := c.ConnectOutput(c.InputName(0)); err != nil {
		t.Fatal(err)
	}
	if c.PlaybackState() != PlaybackIdle {
		t.Fatal("playing before Play")
	}
	if err := c.Play(); err != nil {
		t.Fatal(err)
	}
	if err := c.Play(); err == nil {
		t.Fatal("second Play accepted")
	}
	var got []float32
	buf := make([]float32, 4096)
	deadline := time.Now().Add(5 * time.Second)
	for c.PlaybackState() != PlaybackDone && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		for k := c.Ring().Read(buf); k > 0; k = c.Ring().Read(buf) {
			got = append(got, buf[:k]...)
		}
	}
	time.Sleep(100 * time.Millisecond)
	for k := c.Ring().Read(buf); k > 0; k = c.Ring().Read(buf) {
		got = append(got, buf[:k]...)
	}
	if c.PlaybackState() != PlaybackDone || c.PlaybackPosition() != n {
		t.Fatalf("state %d position %d", c.PlaybackState(), c.PlaybackPosition())
	}
	start := -1
	for i, v := range got {
		if v != 0 {
			start = i
			break
		}
	}
	if start < 0 || len(got) < start+n {
		t.Fatalf("captured %d samples, signal starts at %d", len(got), start)
	}
	for i := range n {
		if got[start+i] != signal[i] {
			t.Fatalf("sample %d: got %v want %v", i, got[start+i], signal[i])
		}
	}
	for _, v := range got[start+n:] {
		if v != 0 {
			t.Fatal("output not silent after the buffer ended")
		}
	}
}

func TestStopFadesOutThenSilence(t *testing.T) {
	requireDummyJACK(t)
	c, err := NewCapture("jive-room-stop", 1<<17, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.EnablePlayback(); err != nil {
		t.Fatal(err)
	}
	signal := make([]float32, 44100*5)
	for i := range signal {
		signal[i] = 0.5
	}
	if err := c.LoadPlayback(signal); err != nil {
		t.Fatal(err)
	}
	if err := c.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := c.ConnectOutput(c.InputName(0)); err != nil {
		t.Fatal(err)
	}
	if err := c.Play(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	c.StopPlayback()
	deadline := time.Now().Add(2 * time.Second)
	for c.PlaybackState() != PlaybackDone && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.PlaybackState() != PlaybackDone || c.PlaybackPosition() >= len(signal) {
		t.Fatalf("stop did not end playback early: state %d position %d", c.PlaybackState(), c.PlaybackPosition())
	}
	time.Sleep(100 * time.Millisecond)
	var got []float32
	buf := make([]float32, 8192)
	for k := c.Ring().Read(buf); k > 0; k = c.Ring().Read(buf) {
		got = append(got, buf[:k]...)
	}
	last := -1
	for i, v := range got {
		if v != 0 {
			last = i
		}
	}
	if last < 0 {
		t.Fatal("nothing captured")
	}
	prev := float32(0.5)
	fadeStart := last - 1100
	for i := max(0, fadeStart); i <= last; i++ {
		if got[i] > prev+1e-6 {
			t.Fatalf("level rose during the stop fade at %d", i)
		}
		prev = got[i]
	}
	if got[last] > 0.01 {
		t.Fatalf("hard stop: last non-zero sample %v", got[last])
	}
}
