# ADR 0003: StudioLive target uses JACK input and the mixer as the actuator

Status: accepted

## Context

The first deployment target is a 2011 Mac mini (Xubuntu 24.04, glibc 2.39, i5-2415M) bridging a PreSonus StudioLive 16.0.2 over FireWire. FFADO/JACK owns the audio stream (originally 44.1 kHz, 1024 frames, 3 periods; 48 kHz, 128 frames, 4 periods since 2026-10-20, the only rate at which FireWire return 1 plays to the desk); `snd_dice` is blacklisted because ALSA capture fails with I/O errors. osclive owns mixer control over raw1394 and serves OSC/TCP on 4301. A Python bridge caches mixer state and serves Open Stage Control over UDP 9000/9001. See [`../StudioLive-Integration.md`](../StudioLive-Integration.md).

## Decision

- On this target, `jive-room` reads audio as a native JACK client (`//go:build jack`), using the same C SPSC analysis ring as the PipeWire backend. The JACK process callback only copies into the ring. Port connections, selection, and analysis run off the RT thread. PipeWire stays the default live backend elsewhere; ADR 0001 is unchanged for that path.
- The StudioLive's own channel HPF/EQ and main 31-band graphic EQ are the actuators. `jive-room` does not insert DSP into the live signal on this target: a FireWire round trip at 3 × 1024 frames is roughly 70 ms each way, unusable for an inline vocal path.
- `jive-room` never writes mixer addresses directly. It publishes proposals to a `/dsp/...` namespace on the bridge. The bridge is the single mixer-state broker: it validates, clamps, snapshots, writes, verifies read-back, and owns undo (handover Option A).
- Operator-started pink-noise playback through a JACK FireWire return is permitted at a fixed conservative level with fades and a duration cap. It never changes mixer gain, faders, or routing.

## Consequences

- A JACK backend and an Ubuntu 24.04 build image are needed. Binaries built on the Fedora 44 toolchain image are expected to require a newer glibc than the mini provides.
- The bridge gains state-subscription and `/dsp` proposal/apply/revert handling. Policy lives in the bridge; `jive-room` also enforces the same bounds before publishing.
- Recommendations must map to the desk's quantised 8-bit control values through measured calibration tables, not assumed linear dB or Hz scales.
- The in-path C DSP core (M3) remains valid for the PipeWire target but is not on the StudioLive critical path.
