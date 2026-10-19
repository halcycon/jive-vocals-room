#include "ring.h"

#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>

static int is_power_of_two(uint32_t n)
{
	return n != 0 && (n & (n - 1)) == 0;
}

int jive_rt_ring_init(jive_rt_ring *r, float *storage, uint32_t capacity_frames, uint32_t channels)
{
	if (r == NULL || storage == NULL || !is_power_of_two(capacity_frames) || channels == 0 ||
	    channels > JIVE_RT_RING_MAX_CHANNELS) {
		return -1;
	}
	r->capacity_frames = capacity_frames;
	r->channels = channels;
	r->samples = storage;
	jive_rt_ring_reset(r);
	return 0;
}

jive_rt_ring *jive_rt_ring_create(uint32_t capacity_frames, uint32_t channels)
{
	jive_rt_ring *r;
	float *storage;
	size_t nsamples;

	if (!is_power_of_two(capacity_frames) || channels == 0 || channels > JIVE_RT_RING_MAX_CHANNELS) {
		return NULL;
	}
	nsamples = (size_t)capacity_frames * (size_t)channels;
	r = calloc(1, sizeof(*r));
	storage = calloc(nsamples, sizeof(float));
	if (r == NULL || storage == NULL) {
		free(r);
		free(storage);
		return NULL;
	}
	if (jive_rt_ring_init(r, storage, capacity_frames, channels) != 0) {
		free(storage);
		free(r);
		return NULL;
	}
	return r;
}

void jive_rt_ring_destroy(jive_rt_ring *r)
{
	if (r == NULL) {
		return;
	}
	free(r->samples);
	r->samples = NULL;
	free(r);
}

void jive_rt_ring_reset(jive_rt_ring *r)
{
	atomic_store_explicit(&r->write_frames, 0, memory_order_relaxed);
	atomic_store_explicit(&r->read_frames, 0, memory_order_relaxed);
	atomic_store_explicit(&r->overruns, 0, memory_order_relaxed);
	memset(r->samples, 0, (size_t)r->capacity_frames * r->channels * sizeof(float));
}

uint64_t jive_rt_ring_overruns(const jive_rt_ring *r)
{
	return atomic_load_explicit(&r->overruns, memory_order_relaxed);
}

uint32_t jive_rt_ring_write(jive_rt_ring *r, const float *interleaved, uint32_t nframes)
{
	uint32_t cap, w, rd, used, free_frames, to_write, mask, ch, i;
	const float *src;
	float *dst;

	if (r == NULL || interleaved == NULL || nframes == 0) {
		return 0;
	}
	cap = r->capacity_frames;
	mask = cap - 1;
	ch = r->channels;
	w = atomic_load_explicit(&r->write_frames, memory_order_relaxed);
	rd = atomic_load_explicit(&r->read_frames, memory_order_acquire);
	used = w - rd;
	free_frames = cap - used;
	to_write = nframes;
	if (to_write > free_frames) {
		atomic_fetch_add_explicit(&r->overruns, (uint64_t)(to_write - free_frames), memory_order_relaxed);
		to_write = free_frames;
	}
	if (to_write == 0) {
		return 0;
	}
	src = interleaved;
	for (i = 0; i < to_write; i++) {
		dst = r->samples + ((w + i) & mask) * ch;
		memcpy(dst, src, (size_t)ch * sizeof(float));
		src += ch;
	}
	atomic_store_explicit(&r->write_frames, w + to_write, memory_order_release);
	return to_write;
}

uint32_t jive_rt_ring_read(jive_rt_ring *r, float *interleaved, uint32_t max_frames)
{
	uint32_t cap, w, rd, available, to_read, mask, ch, i;
	float *dst;
	const float *src;

	if (r == NULL || interleaved == NULL || max_frames == 0) {
		return 0;
	}
	cap = r->capacity_frames;
	mask = cap - 1;
	ch = r->channels;
	rd = atomic_load_explicit(&r->read_frames, memory_order_relaxed);
	w = atomic_load_explicit(&r->write_frames, memory_order_acquire);
	available = w - rd;
	to_read = available;
	if (to_read > max_frames) {
		to_read = max_frames;
	}
	if (to_read == 0) {
		return 0;
	}
	dst = interleaved;
	for (i = 0; i < to_read; i++) {
		src = r->samples + ((rd + i) & mask) * ch;
		memcpy(dst, src, (size_t)ch * sizeof(float));
		dst += ch;
	}
	atomic_store_explicit(&r->read_frames, rd + to_read, memory_order_release);
	return to_read;
}
