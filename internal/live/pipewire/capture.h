#ifndef JIVE_PW_CAPTURE_H
#define JIVE_PW_CAPTURE_H

#include "../rt/ring.h"
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct jive_pw_capture jive_pw_capture;

jive_pw_capture *jive_pw_capture_create(jive_rt_ring *ring, uint32_t channels);
int jive_pw_capture_start(jive_pw_capture *c);
void jive_pw_capture_stop(jive_pw_capture *c);
void jive_pw_capture_destroy(jive_pw_capture *c);
uint32_t jive_pw_capture_rate(const jive_pw_capture *c);
uint32_t jive_pw_capture_quantum(const jive_pw_capture *c);
uint32_t jive_pw_capture_channels(const jive_pw_capture *c);
uint64_t jive_pw_capture_xruns(const jive_pw_capture *c);
uint64_t jive_pw_capture_samples(const jive_pw_capture *c);

#ifdef __cplusplus
}
#endif

#endif
