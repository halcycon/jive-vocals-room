#include "capture.h"

#include <pipewire/pipewire.h>
#include <spa/param/audio/format-utils.h>
#include <spa/pod/builder.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#define JIVE_PW_MAX_QUANTUM 8192

struct jive_pw_capture {
	struct pw_thread_loop *loop;
	struct pw_context *context;
	struct pw_core *core;
	struct pw_stream *stream;
	struct spa_hook stream_listener;
	jive_rt_ring *ring;
	uint32_t wanted_channels;
	_Atomic uint32_t rate;
	_Atomic uint32_t quantum;
	_Atomic uint32_t channels;
	_Atomic uint64_t xruns;
	_Atomic uint64_t samples;
	float scratch[JIVE_PW_MAX_QUANTUM * JIVE_RT_RING_MAX_CHANNELS];
};

static void on_process(void *data)
{
	struct jive_pw_capture *c = data;
	struct pw_buffer *b;
	struct spa_buffer *buf;
	uint8_t *src;
	uint32_t n_frames, ch, i;
	int32_t stride;

	b = pw_stream_dequeue_buffer(c->stream);
	if (b == NULL) {
		atomic_fetch_add_explicit(&c->xruns, 1, memory_order_relaxed);
		return;
	}
	buf = b->buffer;
	src = buf->datas[0].data;
	if (src == NULL) {
		pw_stream_queue_buffer(c->stream, b);
		return;
	}
	ch = atomic_load_explicit(&c->channels, memory_order_relaxed);
	if (ch == 0) {
		ch = c->wanted_channels;
	}
	if (ch == 0) {
		ch = 1;
	}
	stride = buf->datas[0].chunk->stride;
	if (stride <= 0) {
		stride = (int32_t)(ch * sizeof(float));
	}
	n_frames = buf->datas[0].chunk->size / (uint32_t)stride;
	if (n_frames == 0) {
		pw_stream_queue_buffer(c->stream, b);
		return;
	}
	if (n_frames > JIVE_PW_MAX_QUANTUM) {
		n_frames = JIVE_PW_MAX_QUANTUM;
		atomic_fetch_add_explicit(&c->xruns, 1, memory_order_relaxed);
	}
	atomic_store_explicit(&c->quantum, n_frames, memory_order_relaxed);
	if ((uint32_t)stride == ch * sizeof(float)) {
		jive_rt_ring_write(c->ring, (const float *)src, n_frames);
	} else {
		for (i = 0; i < n_frames; i++) {
			memcpy(c->scratch + i * ch, src + (size_t)i * (size_t)stride, (size_t)ch * sizeof(float));
		}
		jive_rt_ring_write(c->ring, c->scratch, n_frames);
	}
	atomic_fetch_add_explicit(&c->samples, n_frames, memory_order_relaxed);
	pw_stream_queue_buffer(c->stream, b);
}

static void on_param_changed(void *data, uint32_t id, const struct spa_pod *param)
{
	struct jive_pw_capture *c = data;
	struct spa_audio_info_raw info;

	if (param == NULL || id != SPA_PARAM_Format) {
		return;
	}
	spa_zero(info);
	if (spa_format_audio_raw_parse(param, &info) < 0) {
		return;
	}
	atomic_store_explicit(&c->rate, info.rate, memory_order_relaxed);
	if (info.channels > 0 && info.channels <= JIVE_RT_RING_MAX_CHANNELS) {
		atomic_store_explicit(&c->channels, info.channels, memory_order_relaxed);
	}
}

static const struct pw_stream_events stream_events = {
	PW_VERSION_STREAM_EVENTS,
	.param_changed = on_param_changed,
	.process = on_process,
};

jive_pw_capture *jive_pw_capture_create(jive_rt_ring *ring, uint32_t channels)
{
	static int inited;
	jive_pw_capture *c;

	if (ring == NULL || channels == 0 || channels > JIVE_RT_RING_MAX_CHANNELS) {
		return NULL;
	}
	if (!inited) {
		pw_init(NULL, NULL);
		inited = 1;
	}
	c = calloc(1, sizeof(*c));
	if (c == NULL) {
		return NULL;
	}
	c->ring = ring;
	c->wanted_channels = channels;
	atomic_store(&c->channels, channels);
	atomic_store(&c->rate, 48000);
	return c;
}

int jive_pw_capture_start(jive_pw_capture *c)
{
	uint8_t buffer[1024];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buffer, sizeof(buffer));
	const struct spa_pod *params[1];
	struct pw_properties *props;

	if (c == NULL || c->loop != NULL) {
		return -1;
	}
	c->loop = pw_thread_loop_new("jive-room-analyse", NULL);
	if (c->loop == NULL) {
		return -1;
	}
	c->context = pw_context_new(pw_thread_loop_get_loop(c->loop), NULL, 0);
	if (c->context == NULL) {
		return -1;
	}
	if (pw_thread_loop_start(c->loop) < 0) {
		return -1;
	}
	pw_thread_loop_lock(c->loop);
	c->core = pw_context_connect(c->context, NULL, 0);
	if (c->core == NULL) {
		pw_thread_loop_unlock(c->loop);
		return -1;
	}
	props = pw_properties_new(PW_KEY_MEDIA_TYPE, "Audio", PW_KEY_MEDIA_CATEGORY, "Capture",
				  PW_KEY_MEDIA_ROLE, "DSP", PW_KEY_NODE_NAME, "jive-room-analyse", NULL);
	c->stream = pw_stream_new(c->core, "jive-room-analyse", props);
	if (c->stream == NULL) {
		pw_thread_loop_unlock(c->loop);
		return -1;
	}
	pw_stream_add_listener(c->stream, &c->stream_listener, &stream_events, c);
	params[0] = spa_format_audio_raw_build(&b, SPA_PARAM_EnumFormat,
					       &SPA_AUDIO_INFO_RAW_INIT(.format = SPA_AUDIO_FORMAT_F32,
									.channels = c->wanted_channels, .rate = 48000));
	if (pw_stream_connect(c->stream, PW_DIRECTION_INPUT, PW_ID_ANY,
			      PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS | PW_STREAM_FLAG_RT_PROCESS, params,
			      1) < 0) {
		pw_thread_loop_unlock(c->loop);
		return -1;
	}
	pw_thread_loop_unlock(c->loop);
	return 0;
}

void jive_pw_capture_stop(jive_pw_capture *c)
{
	if (c == NULL || c->loop == NULL) {
		return;
	}
	pw_thread_loop_stop(c->loop);
}

void jive_pw_capture_destroy(jive_pw_capture *c)
{
	if (c == NULL) {
		return;
	}
	jive_pw_capture_stop(c);
	if (c->loop != NULL) {
		pw_thread_loop_lock(c->loop);
	}
	if (c->stream != NULL) {
		pw_stream_destroy(c->stream);
		c->stream = NULL;
	}
	if (c->core != NULL) {
		pw_core_disconnect(c->core);
		c->core = NULL;
	}
	if (c->loop != NULL) {
		pw_thread_loop_unlock(c->loop);
	}
	if (c->context != NULL) {
		pw_context_destroy(c->context);
		c->context = NULL;
	}
	if (c->loop != NULL) {
		pw_thread_loop_destroy(c->loop);
		c->loop = NULL;
	}
	free(c);
}

uint32_t jive_pw_capture_rate(const jive_pw_capture *c)
{
	return c == NULL ? 0 : atomic_load_explicit(&c->rate, memory_order_relaxed);
}

uint32_t jive_pw_capture_quantum(const jive_pw_capture *c)
{
	return c == NULL ? 0 : atomic_load_explicit(&c->quantum, memory_order_relaxed);
}

uint32_t jive_pw_capture_channels(const jive_pw_capture *c)
{
	return c == NULL ? 0 : atomic_load_explicit(&c->channels, memory_order_relaxed);
}

uint64_t jive_pw_capture_xruns(const jive_pw_capture *c)
{
	return c == NULL ? 0 : atomic_load_explicit(&c->xruns, memory_order_relaxed);
}

uint64_t jive_pw_capture_samples(const jive_pw_capture *c)
{
	return c == NULL ? 0 : atomic_load_explicit(&c->samples, memory_order_relaxed);
}
