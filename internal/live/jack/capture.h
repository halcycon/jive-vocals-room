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
