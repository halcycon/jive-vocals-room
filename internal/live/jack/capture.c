#include "capture.h"

#include <errno.h>
#include <jack/jack.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define JIVE_JACK_MAX_FRAMES 8192

struct jive_jack_capture {
	jack_client_t *client;
	jack_port_t *ports[JIVE_RT_RING_MAX_CHANNELS];
	jive_rt_ring *ring;
	uint32_t channels;
	_Atomic uint32_t rate;
	_Atomic uint32_t quantum;
	_Atomic uint64_t xruns;
	_Atomic uint64_t samples;
	_Atomic int healthy;
	/* Playback: play_buf/play_len are written before PLAYING is published with
	 * release ordering and never change afterwards. fade_left is RT-owned. */
	jack_port_t *out_port;
	float *play_buf;
	uint32_t play_len;
	_Atomic uint32_t play_pos;
	_Atomic int play_state;
	uint32_t fade_left;
	int fade_started;
	float scratch[JIVE_JACK_MAX_FRAMES * JIVE_RT_RING_MAX_CHANNELS];
};

/* RT thread: copy the preloaded buffer or write silence. */
static void render_playback(struct jive_jack_capture *c, jack_nframes_t nframes)
{
	float *out = jack_port_get_buffer(c->out_port, nframes);
	int state = atomic_load_explicit(&c->play_state, memory_order_acquire);
	uint32_t pos, i;

	if (out == NULL) {
		return;
	}
	if (state != JIVE_JACK_PLAY_PLAYING && state != JIVE_JACK_PLAY_STOPPING) {
		memset(out, 0, nframes * sizeof(float));
		return;
	}
	if (state == JIVE_JACK_PLAY_STOPPING && !c->fade_started) {
		c->fade_started = 1;
		c->fade_left = JIVE_JACK_STOP_FADE_FRAMES;
	}
	pos = atomic_load_explicit(&c->play_pos, memory_order_relaxed);
	for (i = 0; i < nframes; i++) {
		float g = 1.0f;
		if (pos >= c->play_len || (c->fade_started && c->fade_left == 0)) {
			out[i] = 0.0f;
			continue;
		}
		if (c->fade_started) {
			g = (float)c->fade_left / (float)JIVE_JACK_STOP_FADE_FRAMES;
			c->fade_left--;
		}
		out[i] = c->play_buf[pos++] * g;
	}
	atomic_store_explicit(&c->play_pos, pos, memory_order_relaxed);
	if (pos >= c->play_len || (c->fade_started && c->fade_left == 0)) {
		atomic_store_explicit(&c->play_state, JIVE_JACK_PLAY_DONE, memory_order_release);
	}
}

/* RT thread: copy and interleave only. No allocation, I/O, locks, or Go. */
static int on_process(jack_nframes_t nframes, void *arg)
{
	struct jive_jack_capture *c = arg;
	const float *bufs[JIVE_RT_RING_MAX_CHANNELS];
	uint32_t ch = c->channels, i, k;

	if (nframes == 0) {
		return 0;
	}
	if (c->out_port != NULL) {
		render_playback(c, nframes);
	}
	if (nframes > JIVE_JACK_MAX_FRAMES) {
		nframes = JIVE_JACK_MAX_FRAMES;
		atomic_fetch_add_explicit(&c->xruns, 1, memory_order_relaxed);
	}
	for (k = 0; k < ch; k++) {
		bufs[k] = jack_port_get_buffer(c->ports[k], nframes);
		if (bufs[k] == NULL) {
			return 0;
		}
	}
	if (ch == 1) {
		jive_rt_ring_write(c->ring, bufs[0], nframes);
	} else {
		for (i = 0; i < nframes; i++) {
			for (k = 0; k < ch; k++) {
				c->scratch[i * ch + k] = bufs[k][i];
			}
		}
		jive_rt_ring_write(c->ring, c->scratch, nframes);
	}
	atomic_store_explicit(&c->quantum, nframes, memory_order_relaxed);
	atomic_fetch_add_explicit(&c->samples, nframes, memory_order_relaxed);
	return 0;
}

/* JACK notification thread. Counts graph xruns reported by the server. */
static int on_xrun(void *arg)
{
	struct jive_jack_capture *c = arg;
	atomic_fetch_add_explicit(&c->xruns, 1, memory_order_relaxed);
	return 0;
}

static int on_rate(jack_nframes_t rate, void *arg)
{
	struct jive_jack_capture *c = arg;
	atomic_store_explicit(&c->rate, rate, memory_order_relaxed);
	return 0;
}

static void on_shutdown(void *arg)
{
	struct jive_jack_capture *c = arg;
	atomic_store_explicit(&c->healthy, 0, memory_order_relaxed);
}

jive_jack_capture *jive_jack_capture_create(jive_rt_ring *ring, uint32_t channels, const char *client_name,
					    int *status_out)
{
	jive_jack_capture *c;
	jack_status_t status = 0;
	char name[32];
	uint32_t k;

	if (status_out != NULL) {
		*status_out = 0;
	}
	if (ring == NULL || channels == 0 || channels > JIVE_RT_RING_MAX_CHANNELS || ring->channels != channels ||
	    client_name == NULL) {
		return NULL;
	}
	c = calloc(1, sizeof(*c));
	if (c == NULL) {
		return NULL;
	}
	c->ring = ring;
	c->channels = channels;
	c->client = jack_client_open(client_name, JackNoStartServer, &status);
	if (status_out != NULL) {
		*status_out = (int)status;
	}
	if (c->client == NULL) {
		free(c);
		return NULL;
	}
	for (k = 0; k < channels; k++) {
		snprintf(name, sizeof(name), "in_%u", k + 1);
		c->ports[k] = jack_port_register(c->client, name, JACK_DEFAULT_AUDIO_TYPE, JackPortIsInput, 0);
		if (c->ports[k] == NULL) {
			jack_client_close(c->client);
			free(c);
			return NULL;
		}
	}
	atomic_store(&c->rate, jack_get_sample_rate(c->client));
	atomic_store(&c->quantum, jack_get_buffer_size(c->client));
	atomic_store(&c->healthy, 1);
	if (jack_set_process_callback(c->client, on_process, c) != 0 ||
	    jack_set_xrun_callback(c->client, on_xrun, c) != 0 ||
	    jack_set_sample_rate_callback(c->client, on_rate, c) != 0) {
		jack_client_close(c->client);
		free(c);
		return NULL;
	}
	jack_on_shutdown(c->client, on_shutdown, c);
	return c;
}

int jive_jack_capture_activate(jive_jack_capture *c)
{
	if (c == NULL || c->client == NULL) {
		return -1;
	}
	return jack_activate(c->client);
}

int jive_jack_capture_connect(jive_jack_capture *c, uint32_t index, const char *source_port)
{
	int rc;

	if (c == NULL || c->client == NULL || index >= c->channels || source_port == NULL) {
		return -1;
	}
	rc = jack_connect(c->client, source_port, jack_port_name(c->ports[index]));
	return rc == EEXIST ? 0 : rc;
}

void jive_jack_capture_destroy(jive_jack_capture *c)
{
	if (c == NULL) {
		return;
	}
	if (c->client != NULL) {
		jack_client_close(c->client);
		c->client = NULL;
	}
	free(c->play_buf);
	free(c);
}

int jive_jack_capture_enable_playback(jive_jack_capture *c)
{
	if (c == NULL || c->client == NULL || c->out_port != NULL) {
		return -1;
	}
	c->out_port = jack_port_register(c->client, "out_1", JACK_DEFAULT_AUDIO_TYPE, JackPortIsOutput, 0);
	return c->out_port == NULL ? -1 : 0;
}

int jive_jack_capture_load_playback(jive_jack_capture *c, const float *samples, uint32_t nframes)
{
	if (c == NULL || c->out_port == NULL || c->play_buf != NULL || samples == NULL || nframes == 0 ||
	    atomic_load(&c->play_state) != JIVE_JACK_PLAY_IDLE) {
		return -1;
	}
	c->play_buf = malloc((size_t)nframes * sizeof(float));
	if (c->play_buf == NULL) {
		return -1;
	}
	memcpy(c->play_buf, samples, (size_t)nframes * sizeof(float));
	c->play_len = nframes;
	atomic_store(&c->play_pos, 0);
	return 0;
}

int jive_jack_capture_connect_output(jive_jack_capture *c, const char *sink_port)
{
	int rc;

	if (c == NULL || c->client == NULL || c->out_port == NULL || sink_port == NULL) {
		return -1;
	}
	rc = jack_connect(c->client, jack_port_name(c->out_port), sink_port);
	return rc == EEXIST ? 0 : rc;
}

int jive_jack_capture_play(jive_jack_capture *c)
{
	int idle = JIVE_JACK_PLAY_IDLE;

	if (c == NULL || c->play_buf == NULL) {
		return -1;
	}
	return atomic_compare_exchange_strong_explicit(&c->play_state, &idle, JIVE_JACK_PLAY_PLAYING,
						       memory_order_release, memory_order_relaxed)
		       ? 0
		       : -1;
}

void jive_jack_capture_stop_playback(jive_jack_capture *c)
{
	int playing = JIVE_JACK_PLAY_PLAYING;
	int idle = JIVE_JACK_PLAY_IDLE;

	if (c == NULL) {
		return;
	}
	if (!atomic_compare_exchange_strong(&c->play_state, &playing, JIVE_JACK_PLAY_STOPPING)) {
		atomic_compare_exchange_strong(&c->play_state, &idle, JIVE_JACK_PLAY_DONE);
	}
}

int jive_jack_capture_playback_state(const jive_jack_capture *c)
{
	return c == NULL ? JIVE_JACK_PLAY_IDLE : atomic_load_explicit(&c->play_state, memory_order_acquire);
}

uint32_t jive_jack_capture_playback_position(const jive_jack_capture *c)
{
	return c == NULL ? 0 : atomic_load_explicit(&c->play_pos, memory_order_relaxed);
}

const char *jive_jack_capture_output_name(jive_jack_capture *c)
{
	return (c == NULL || c->out_port == NULL) ? NULL : jack_port_name(c->out_port);
}

const char *jive_jack_capture_input_name(jive_jack_capture *c, uint32_t index)
{
	return (c == NULL || index >= c->channels) ? NULL : jack_port_name(c->ports[index]);
}

const char **jive_jack_capture_physical_sinks(jive_jack_capture *c)
{
	if (c == NULL || c->client == NULL) {
		return NULL;
	}
	return jack_get_ports(c->client, NULL, JACK_DEFAULT_AUDIO_TYPE, JackPortIsPhysical | JackPortIsInput);
}

const char **jive_jack_capture_physical_sources(jive_jack_capture *c)
{
	if (c == NULL || c->client == NULL) {
		return NULL;
	}
	return jack_get_ports(c->client, NULL, JACK_DEFAULT_AUDIO_TYPE, JackPortIsPhysical | JackPortIsOutput);
}

void jive_jack_free_ports(const char **ports)
{
	if (ports != NULL) {
		jack_free((void *)ports);
	}
}

const char *jive_jack_capture_client_name(jive_jack_capture *c)
{
	return (c == NULL || c->client == NULL) ? NULL : jack_get_client_name(c->client);
}

uint32_t jive_jack_capture_rate(const jive_jack_capture *c)
{
	return c == NULL ? 0 : atomic_load_explicit(&c->rate, memory_order_relaxed);
}

uint32_t jive_jack_capture_quantum(const jive_jack_capture *c)
{
	return c == NULL ? 0 : atomic_load_explicit(&c->quantum, memory_order_relaxed);
}

uint32_t jive_jack_capture_channels(const jive_jack_capture *c)
{
	return c == NULL ? 0 : c->channels;
}

uint64_t jive_jack_capture_xruns(const jive_jack_capture *c)
{
	return c == NULL ? 0 : atomic_load_explicit(&c->xruns, memory_order_relaxed);
}

uint64_t jive_jack_capture_samples(const jive_jack_capture *c)
{
	return c == NULL ? 0 : atomic_load_explicit(&c->samples, memory_order_relaxed);
}

int jive_jack_capture_healthy(const jive_jack_capture *c)
{
	return c == NULL ? 0 : atomic_load_explicit(&c->healthy, memory_order_relaxed);
}
