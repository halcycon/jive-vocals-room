#ifndef JIVE_JACK_CAPTURE_H
#define JIVE_JACK_CAPTURE_H

#include "../rt/ring.h"
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct jive_jack_capture jive_jack_capture;

/* Opens a JACK client with `channels` input ports (in_1..in_N). Never starts a
 * server and never registers output ports. Returns NULL on failure; *status_out
 * receives the jack_status_t bits when non-NULL. */
jive_jack_capture *jive_jack_capture_create(jive_rt_ring *ring, uint32_t channels, const char *client_name,
					    int *status_out);
int jive_jack_capture_activate(jive_jack_capture *c);
int jive_jack_capture_connect(jive_jack_capture *c, uint32_t index, const char *source_port);
void jive_jack_capture_destroy(jive_jack_capture *c);

/* NULL-terminated list of physical capture (source) ports; free with jive_jack_free_ports. */
const char **jive_jack_capture_physical_sources(jive_jack_capture *c);
void jive_jack_free_ports(const char **ports);
const char *jive_jack_capture_client_name(jive_jack_capture *c);

/* Optional single-port playback of one preloaded buffer (operator-started test
 * signals only). enable must precede activate; load copies the samples once.
 * The RT callback outputs silence unless playing, and stop fades out over
 * JIVE_JACK_STOP_FADE_FRAMES before going silent. */
#define JIVE_JACK_STOP_FADE_FRAMES 1024
enum { JIVE_JACK_PLAY_IDLE = 0, JIVE_JACK_PLAY_PLAYING = 1, JIVE_JACK_PLAY_STOPPING = 2, JIVE_JACK_PLAY_DONE = 3 };
int jive_jack_capture_enable_playback(jive_jack_capture *c);
int jive_jack_capture_load_playback(jive_jack_capture *c, const float *samples, uint32_t nframes);
int jive_jack_capture_connect_output(jive_jack_capture *c, const char *sink_port);
int jive_jack_capture_play(jive_jack_capture *c);
void jive_jack_capture_stop_playback(jive_jack_capture *c);
int jive_jack_capture_playback_state(const jive_jack_capture *c);
uint32_t jive_jack_capture_playback_position(const jive_jack_capture *c);
const char *jive_jack_capture_output_name(jive_jack_capture *c);
const char *jive_jack_capture_input_name(jive_jack_capture *c, uint32_t index);
/* NULL-terminated list of physical playback (sink) ports; free with jive_jack_free_ports. */
const char **jive_jack_capture_physical_sinks(jive_jack_capture *c);

uint32_t jive_jack_capture_rate(const jive_jack_capture *c);
uint32_t jive_jack_capture_quantum(const jive_jack_capture *c);
uint32_t jive_jack_capture_channels(const jive_jack_capture *c);
uint64_t jive_jack_capture_xruns(const jive_jack_capture *c);
uint64_t jive_jack_capture_samples(const jive_jack_capture *c);
int jive_jack_capture_healthy(const jive_jack_capture *c);

#ifdef __cplusplus
}
#endif

#endif
