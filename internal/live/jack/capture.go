//go:build jack

// Package jack is the tagged native JACK input capture. The client registers
// input ports only, so it cannot play audio; the process callback only copies
// samples into the analysis ring.
package jack

/*
#cgo pkg-config: jack
#cgo CFLAGS: -std=gnu11
#include <stdlib.h>
#include "capture.h"
*/
import "C"

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/linuxmatters/jive-vocals/internal/live/rt"
)

type Capture struct {
	ring *rt.Ring
	cap  *C.jive_jack_capture
}

// NewCapture opens a JACK client with one input port per channel. It does not
// start a JACK server; the server must already be running.
func NewCapture(clientName string, capacityFrames, channels int) (*Capture, error) {
	ring, err := rt.NewRing(capacityFrames, channels)
	if err != nil {
		return nil, err
	}
	name := C.CString(clientName)
	defer C.free(unsafe.Pointer(name))
	var status C.int
	cap := C.jive_jack_capture_create((*C.jive_rt_ring)(ring.CRing()), C.uint32_t(channels), name, &status)
	if cap == nil {
		return nil, fmt.Errorf("jack client open failed (status 0x%x; is jackd running for this user?)", int(status))
	}
	runtime.KeepAlive(ring)
	return &Capture{ring: ring, cap: cap}, nil
}

// PhysicalSources lists physical capture ports, e.g. StudioLive inputs.
func (c *Capture) PhysicalSources() []string {
	return goPorts(C.jive_jack_capture_physical_sources(c.cap))
}

func goPorts(ports **C.char) []string {
	if ports == nil {
		return nil
	}
	defer C.jive_jack_free_ports(ports)
	var out []string
	for p := ports; *p != nil; p = (**C.char)(unsafe.Add(unsafe.Pointer(p), unsafe.Sizeof(*p))) {
		out = append(out, C.GoString(*p))
	}
	return out
}

func (c *Capture) ClientName() string {
	return C.GoString(C.jive_jack_capture_client_name(c.cap))
}

func (c *Capture) Activate() error {
	if rc := C.jive_jack_capture_activate(c.cap); rc != 0 {
		return fmt.Errorf("jack activate failed (%d)", int(rc))
	}
	return nil
}

// Connect wires a physical source port to input port index (0-based).
func (c *Capture) Connect(index int, source string) error {
	src := C.CString(source)
	defer C.free(unsafe.Pointer(src))
	if rc := C.jive_jack_capture_connect(c.cap, C.uint32_t(index), src); rc != 0 {
		return fmt.Errorf("jack connect %q to in_%d failed (%d)", source, index+1, int(rc))
	}
	return nil
}

// PhysicalSinks lists physical playback ports, e.g. StudioLive FireWire returns.
func (c *Capture) PhysicalSinks() []string {
	return goPorts(C.jive_jack_capture_physical_sinks(c.cap))
}

// EnablePlayback registers the single output port. Call before Activate.
func (c *Capture) EnablePlayback() error {
	if C.jive_jack_capture_enable_playback(c.cap) != 0 {
		return fmt.Errorf("jack playback port registration failed")
	}
	return nil
}

// LoadPlayback copies samples into C-owned memory once. They are played only
// after Play and only to the port wired by ConnectOutput.
func (c *Capture) LoadPlayback(samples []float32) error {
	if len(samples) == 0 {
		return fmt.Errorf("empty playback buffer")
	}
	if C.jive_jack_capture_load_playback(c.cap, (*C.float)(unsafe.Pointer(&samples[0])), C.uint32_t(len(samples))) != 0 { //nolint:gosec // bounded by the test-signal duration cap
		return fmt.Errorf("jack playback load failed (already loaded or playback not enabled)")
	}
	return nil
}

func (c *Capture) ConnectOutput(sink string) error {
	s := C.CString(sink)
	defer C.free(unsafe.Pointer(s))
	if rc := C.jive_jack_capture_connect_output(c.cap, s); rc != 0 {
		return fmt.Errorf("jack connect out_1 to %q failed (%d)", sink, int(rc))
	}
	return nil
}

func (c *Capture) Play() error {
	if C.jive_jack_capture_play(c.cap) != 0 {
		return fmt.Errorf("jack playback cannot start (nothing loaded or already started)")
	}
	return nil
}

// StopPlayback fades out over C.JIVE_JACK_STOP_FADE_FRAMES and then outputs silence.
func (c *Capture) StopPlayback() { C.jive_jack_capture_stop_playback(c.cap) }

type PlaybackState int

const (
	PlaybackIdle     PlaybackState = C.JIVE_JACK_PLAY_IDLE
	PlaybackPlaying  PlaybackState = C.JIVE_JACK_PLAY_PLAYING
	PlaybackStopping PlaybackState = C.JIVE_JACK_PLAY_STOPPING
	PlaybackDone     PlaybackState = C.JIVE_JACK_PLAY_DONE
)

func (c *Capture) PlaybackState() PlaybackState {
	return PlaybackState(C.jive_jack_capture_playback_state(c.cap))
}

func (c *Capture) PlaybackPosition() int {
	return int(C.jive_jack_capture_playback_position(c.cap))
}

func (c *Capture) OutputName() string { return C.GoString(C.jive_jack_capture_output_name(c.cap)) }

func (c *Capture) InputName(i int) string {
	return C.GoString(C.jive_jack_capture_input_name(c.cap, C.uint32_t(i))) //nolint:gosec // index checked in C
}

func (c *Capture) Ring() *rt.Ring { return c.ring }

func (c *Capture) Rate() int     { return int(C.jive_jack_capture_rate(c.cap)) }
func (c *Capture) Quantum() int  { return int(C.jive_jack_capture_quantum(c.cap)) }
func (c *Capture) Channels() int { return int(C.jive_jack_capture_channels(c.cap)) }

// XRuns counts server-reported graph xruns plus callbacks larger than the
// preallocated scratch.
func (c *Capture) XRuns() uint64   { return uint64(C.jive_jack_capture_xruns(c.cap)) }
func (c *Capture) Samples() uint64 { return uint64(C.jive_jack_capture_samples(c.cap)) }
func (c *Capture) Healthy() bool   { return C.jive_jack_capture_healthy(c.cap) != 0 }

func (c *Capture) Close() error {
	if c.cap != nil {
		C.jive_jack_capture_destroy(c.cap)
		c.cap = nil
	}
	runtime.KeepAlive(c.ring)
	return nil
}
