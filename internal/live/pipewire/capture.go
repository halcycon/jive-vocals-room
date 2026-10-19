//go:build pipewire

// Package pipewire is the tagged native PipeWire input capture.
// The real-time callback only copies samples into the analysis ring.
package pipewire

/*
#cgo pkg-config: libpipewire-0.3
#cgo CFLAGS: -std=gnu11
#include "capture.h"
*/
import "C"

import (
	"fmt"
	"runtime"

	"github.com/linuxmatters/jive-vocals/internal/live/rt"
)

// Capture is a read-only PipeWire input tap. It never plays audio or changes gain.
type Capture struct {
	ring *rt.Ring
	cap  *C.jive_pw_capture
}

func NewCapture(capacityFrames, channels int) (*Capture, error) {
	ring, err := rt.NewRing(capacityFrames, channels)
	if err != nil {
		return nil, err
	}
	cap := C.jive_pw_capture_create((*C.jive_rt_ring)(ring.CRing()), C.uint32_t(channels))
	if cap == nil {
		return nil, fmt.Errorf("pipewire capture create failed")
	}
	runtime.KeepAlive(ring)
	return &Capture{ring: ring, cap: cap}, nil
}

func (c *Capture) Start() error {
	if C.jive_pw_capture_start(c.cap) != 0 {
		return fmt.Errorf("pipewire capture start failed (is the session running?)")
	}
	runtime.KeepAlive(c)
	return nil
}

func (c *Capture) Ring() *rt.Ring { return c.ring }

func (c *Capture) Rate() int {
	n := int(C.jive_pw_capture_rate(c.cap))
	runtime.KeepAlive(c)
	return n
}

func (c *Capture) Quantum() int {
	n := int(C.jive_pw_capture_quantum(c.cap))
	runtime.KeepAlive(c)
	return n
}

func (c *Capture) Channels() int {
	n := int(C.jive_pw_capture_channels(c.cap))
	runtime.KeepAlive(c)
	return n
}

func (c *Capture) XRuns() uint64 {
	n := uint64(C.jive_pw_capture_xruns(c.cap))
	runtime.KeepAlive(c)
	return n
}

func (c *Capture) Samples() uint64 {
	n := uint64(C.jive_pw_capture_samples(c.cap))
	runtime.KeepAlive(c)
	return n
}

func (c *Capture) Close() error {
	if c.cap != nil {
		C.jive_pw_capture_destroy(c.cap)
		c.cap = nil
	}
	runtime.KeepAlive(c.ring)
	return nil
}
