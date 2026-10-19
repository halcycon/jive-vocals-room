#include "../ring.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void fail(const char *msg)
{
	fprintf(stderr, "jive-rt-ring-test: %s\n", msg);
	exit(1);
}

int main(void)
{
	float storage[16];
	float in[6] = {1.f, 2.f, 3.f, 4.f, 5.f, 6.f};
	float out[16];
	jive_rt_ring ring;
	jive_rt_ring *created;
	uint32_t n;

	memset(storage, 0, sizeof(storage));
	if (jive_rt_ring_init(&ring, storage, 8, 2) != 0) {
		fail("init");
	}
	n = jive_rt_ring_write(&ring, in, 3);
	if (n != 3) {
		fail("write");
	}
	n = jive_rt_ring_read(&ring, out, 8);
	if (n != 3 || out[0] != 1.f || out[5] != 6.f) {
		fail("read");
	}
	n = jive_rt_ring_write(&ring, in, 3);
	n += jive_rt_ring_write(&ring, in, 3);
	n += jive_rt_ring_write(&ring, in, 3);
	if (jive_rt_ring_overruns(&ring) == 0) {
		fail("expected overruns when the consumer is idle");
	}
	(void)n;

	created = jive_rt_ring_create(8, 1);
	if (created == NULL) {
		fail("create");
	}
	n = jive_rt_ring_write(created, in, 1);
	if (n != 1) {
		fail("create write");
	}
	jive_rt_ring_destroy(created);
	return 0;
}
