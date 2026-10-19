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
	ports := C.jive_jack_capture_physical_sources(c.cap)
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
