# StudioLive 16.0.2 integration

Target: `mixer@studiolive` (2011 Mac mini, Xubuntu 24.04.5, kernel 7.0, glibc 2.39, i5-2415M, 8 GB). Decision record: [ADR 0003](adr/0003-studiolive-jack-mixer-actuator.md). Deployed baseline snapshot: [`integrations/studiolive/`](../integrations/studiolive/README.md). The original architecture handover from the mixer project is [`StudioLive_Mixer_Project_Architecture_Handover.md`](../StudioLive_Mixer_Project_Architecture_Handover.md).

## Existing stack (treat as infrastructure)

```text
StudioLive ── FireWire ──┬── raw1394 ── osclive (OSC/TCP 4301, length-prefixed)
                         │                 │
                         │              bridge (osc_tcp1_bridge_jack.py)
                         │                 │  UDP 9000 in / 9001 out, state cache, RTA + recorder lifecycle
                         │              Open Stage Control (node, :8080)
                         └── FFADO ── jackd (44.1 kHz, 1024 × 3)
                                         ├── studiolive_rta (Python, 31 bands)
                                         ├── recorder (16 ch RF64)
                                         └── jive-room (new, read-only client)
```

systemd order: `studiolive-jack` → `studiolive-osclive` → `studiolive-bridge` → `studiolive-osc`. The new service must use `Wants=`/`After=` so a `jive-room` failure never stops the existing stack, and the bridge must never depend on it.

## Facts verified from deployed code (osclive 0.3.0, commit `1da2ada`)

These came from the osclive source on the mini, not from documentation. Each one constrains apply logic.

- **Value encoding.** Continuous controls are an 8-bit nibble pair: `raw = int(value * 255)` on write, `value = raw / 255` on read. Writing `0.5` stores raw 127, and the desk later reports `0.498`. Compare targets in raw 8-bit units, never as floats, or a "converge to target" loop never terminates.
- **Writes rewrite the whole block.** `set_control` patches osclive's cached raw block and writes all of it: 120 bytes for an input channel, 64 for the GEQ. osclive refreshes a block only when the desk's status poll (about every 100 ms) flags it modified. A write issued just after a physical knob move can revert that move. The bridge must refuse to write a channel that changed on the desk within a settle window, and must verify read-back.
- **Echo to every client.** `SLRemote.set_control` invokes every client's update callback immediately, including the writer's. When the desk confirms, a second update arrives only if the quantised value differs. Incoming state is never a trigger to write.
- **Debug traces.** With `-d`, writes print `StudioLive: Set …` and desk-originated changes print `StudioLive: Upd …`. Use this to audit that `jive-room` produces no unexpected writes.
- **GEQ addresses.** osclive maps the graphic EQ as `/channel/geq0/1` … `/channel/geq0/31` (20 Hz … 20 kHz, ISO 1/3-octave), plus `/channel/geq0/enable`. The handover's `/channel/geq0/20Hz` form is not what osclive dispatches. Flat is `0.5`; the dB range per raw step is not yet calibrated.
- **Dangerous command.** `/channel/geq0/reset` sets all 31 bands to `0.5`. The bridge must never forward it from the DSP path.
- **Fader is `gain`.** `/channel/chN/gain` is the channel fader (`_update_faders`). It is never automated.
- **Channel layout.** Control channels 1–8 are mono; 9–12 are stereo (two level meters each). This implies FireWire streams 1–8 = ch1–8, 9/10 = ch9, 11/12 = ch10, 13/14 = ch11, 15/16 = ch12. **Unverified on hardware.** The deployed RTA maps control channel N to `Ch N_in`, which would be wrong for channels 10–12.
- **Main mix on FireWire 15/16.** osclive has `route_source_1516(main_mix=True)`, but nothing calls it. What streams 15/16 carry now is unknown. A post-GEQ main-mix tap would allow electrical verification of GEQ changes. Changing the routing is a routing change, so it is never automated.
- **Pre/post FireWire send.** `/channel/chN/post` selects whether a channel's FireWire send is post-DSP. Verifying an applied channel EQ change from that channel's capture requires post-DSP sends. Toggling `post` is routing and is never automated; it is reported in the UI.
- **Multiple TCP clients.** osclive creates a handler per TCP connection, so a second client is technically possible. We still route through the bridge (Option A) so one process owns state, snapshots, and undo.

## Mixer model for recommendations

| Control | Address | Recommendation use |
| ------- | ------- | ------------------ |
| HPF on/freq | `/channel/chN/hpf`, `hpffreq` | Presenter low-cut starting point |
| Low band | `eqlow`, `eqlowfreq`, `eqlowgain`, `eqlowshelf` | Broad low/low-mid cuts |
| Mid band | `eqmid`, `eqmidfreq`, `eqmidgain`, `eqmidhiq` | Single-mic ring-out cut (Hi-Q) or resonance cut |
| High band | `eqhigh`, `eqhighfreq`, `eqhighgain`, `eqhighshelf` | Gentle HF reduction only |
| Main GEQ | `/channel/geq0/1..31`, `enable` | PA/room ring-out and transfer-response cuts |

Normalised-to-engineering mappings (Hz, dB, Q) are device-specific and nonlinear, like the fader calibration in the handover. Each needs a measured table. Record it with a calibration version, and store both engineering and raw values in every proposal and profile.

## Never automated

Phantom power, channel and FireWire routing (`firewire`, `post`, FireWire 15/16 source), mute, solo, `toMain`, faders (`gain`), aux and FX sends, FX parameters, master/monitor levels, and `/channel/geq0/reset`. Profiles cannot contain these controls.

## Modes

- **Advance (sound check).**
  - Empty-room, PA pink-noise, presenter, and ring-out captures run from JACK instead of files, producing a `jive-room-session/v1` record plus a mixer profile.
  - During ring-out the operator raises gain on the desk. `jive-room` never raises gain. It flags ringing candidates using prominence, persistence, growth, and stability, and proposes main-GEQ cuts for PA/room feedback or a Hi-Q mid cut for a single offending microphone.
  - The operator confirms each cut.
- **Live (show).** Slow, hysteretic, display-only recommendations per selected channel, with a ringing alert that offers a one-tap, operator-confirmed GEQ cut.
- **Profiles.** Save, load, and push a profile before the show. A push snapshots the current state, writes through the bridge, verifies read-back to within one raw step, and can be undone.
- **Guarded automatic (later).** Off by default and cuts only.
  - At most 3 dB per step, with existing session bounds (6 dB automatic cut per control, 12 dB total).
  - A minimum interval between changes, hysteresis, and a confidence threshold.
  - Operator lockout, an emergency disable, and undo.
  - If `jive-room` disconnects, the mixer is left untouched.

## Bridge `/dsp` namespace (to implement)

- Mixer state is forwarded from the bridge to `jive-room` on a localhost-only UDP port, and `jive-room` publishes `/dsp/...` UI values to the bridge.
- `jive-room` never sends `/channel/...` addresses. The bridge drops any it receives from the DSP port.
- A proposal is a versioned JSON object carrying engineering values, raw values, evidence, and confidence. Operator `/dsp/apply` and `/dsp/revert` come from the UI. The bridge clamps, snapshots, writes, verifies, and publishes the outcome.
- `/bridge/resync` replays the cache to the UI only, as it does today.

## Phases

1. **Read-only JACK analyser.** JACK backend for `jive-room -live`, an Ubuntu 24.04 build image, and channel-to-stream mapping verified on hardware.
2. **Read-only mixer state.** Go OSC client of the bridge state stream, the StudioLive mixer model, and calibration tables.
3. **Advance mode, read-only.** Captures from JACK, the ring-out detector, operator-started pink playback, and a read-only ROOM tab in Open Stage Control.
4. **Assisted apply and profiles.** Bridge `/dsp` apply/revert, snapshot, read-back verification, and profile save/load/push.
5. **Live recommendations,** display only.
6. **Guarded automatic cuts,** off by default.

## Open questions to resolve on hardware

- Actual FireWire stream mapping for stereo channels 9–12, and what streams 15/16 carry.
- Current `post` state of each channel's FireWire send.
- GEQ raw-step-to-dB mapping; channel EQ frequency, gain, and HPF mappings.
- Whether the desk acknowledges every write (osclive notes `FIXME: Check for ACK?`), and the write latency.
