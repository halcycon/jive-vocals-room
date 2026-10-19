#ifndef JIVE_RT_RING_H
#define JIVE_RT_RING_H

#include <stdatomic.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Fixed-capacity interleaved float32 SPSC ring.
 *
 * The producer (PipeWire RT callback) may only call jive_rt_ring_write,
 * jive_rt_ring_overruns, and read atomic telemetry. It must not allocate,
 * log, block, take mutexes, or call into Go.
 *
 * capacity_frames must be a power of two.
 */
#define JIVE_RT_RING_MAX_CHANNELS 8

typedef struct jive_rt_ring {
	uint32_t capacity_frames;
	uint32_t channels;
	_Atomic uint32_t write_frames;
	_Atomic uint32_t read_frames;
	_Atomic uint64_t overruns;
	float *samples; /* C-owned when created with jive_rt_ring_create */
} jive_rt_ring;

int jive_rt_ring_init(jive_rt_ring *r, float *storage, uint32_t capacity_frames, uint32_t channels);
jive_rt_ring *jive_rt_ring_create(uint32_t capacity_frames, uint32_t channels);
void jive_rt_ring_destroy(jive_rt_ring *r);
uint32_t jive_rt_ring_write(jive_rt_ring *r, const float *interleaved, uint32_t nframes);
uint32_t jive_rt_ring_read(jive_rt_ring *r, float *interleaved, uint32_t max_frames);
uint64_t jive_rt_ring_overruns(const jive_rt_ring *r);
void jive_rt_ring_reset(jive_rt_ring *r);

#ifdef __cplusplus
}
#endif

#endif
