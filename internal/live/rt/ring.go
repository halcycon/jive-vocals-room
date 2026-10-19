package rt

/*
#cgo CFLAGS: -std=gnu11
#include "ring.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"
)

const MaxChannels = int(C.JIVE_RT_RING_MAX_CHANNELS)

var ErrRingInit = errors.New("analysis ring capacity must be a power of two with 1–8 channels")

// Ring is a preallocated interleaved float32 SPSC buffer.
// The header and sample storage are C-owned so a PipeWire callback can retain
// the pointer without pinning Go memory. Write is safe for a single RT producer;
// Read is for one non-RT consumer.
type Ring struct {
	ptr *C.jive_rt_ring
}

func NewRing(capacityFrames, channels int) (*Ring, error) {
	ptr := C.jive_rt_ring_create(C.uint32_t(capacityFrames), C.uint32_t(channels))
	if ptr == nil {
		return nil, ErrRingInit
	}
	r := &Ring{ptr: ptr}
	runtime.SetFinalizer(r, (*Ring).destroy)
	return r, nil
}

func (r *Ring) destroy() {
	if r.ptr != nil {
		C.jive_rt_ring_destroy(r.ptr)
		r.ptr = nil
	}
}

func (r *Ring) Channels() int {
	if r.ptr == nil {
		return 0
	}
	return int(r.ptr.channels)
}

func (r *Ring) CapacityFrames() int {
	if r.ptr == nil {
		return 0
	}
	return int(r.ptr.capacity_frames)
}

func (r *Ring) Write(interleaved []float32) int {
	if r.ptr == nil || len(interleaved) == 0 {
		return 0
	}
	ch := r.Channels()
	nframes := len(interleaved) / ch
	n := int(C.jive_rt_ring_write(r.ptr, (*C.float)(unsafe.Pointer(&interleaved[0])), C.uint32_t(nframes)))
	runtime.KeepAlive(r)
	return n
}

func (r *Ring) Read(dst []float32) int {
	if r.ptr == nil || len(dst) == 0 {
		return 0
	}
	ch := r.Channels()
	maxFrames := len(dst) / ch
	n := int(C.jive_rt_ring_read(r.ptr, (*C.float)(unsafe.Pointer(&dst[0])), C.uint32_t(maxFrames)))
	runtime.KeepAlive(r)
	return n
}

func (r *Ring) Overruns() uint64 {
	if r.ptr == nil {
		return 0
	}
	n := uint64(C.jive_rt_ring_overruns(r.ptr))
	runtime.KeepAlive(r)
	return n
}

func (r *Ring) Reset() {
	if r.ptr == nil {
		return
	}
	C.jive_rt_ring_reset(r.ptr)
	runtime.KeepAlive(r)
}

// CRing returns the C-owned ring pointer for the PipeWire callback.
// The Go Ring must stay alive for as long as C holds this pointer.
func (r *Ring) CRing() unsafe.Pointer {
	return unsafe.Pointer(r.ptr)
}
