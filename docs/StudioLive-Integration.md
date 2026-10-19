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

- **Value encoding.** Continuous controls are an 8-bit nibble pair: `raw = int(value * 255)` on write (truncating), `value = raw / 255` on read. Writing `0.5` stores raw 127, and the desk later reports `0.498`. Exact `r/255` values (float32 or float64) round-trip without loss. Compare targets in raw 8-bit units, never as floats, or a "converge to target" loop never terminates. `studiolive.ValueForRaw` sends mid-step values for margin.
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

## Bridge `/dsp` namespace

Implemented in [`integrations/studiolive/bridge/osc_tcp1_bridge_jack.py`](../integrations/studiolive/bridge/osc_tcp1_bridge_jack.py) (108 added lines against the deployed baseline) and installed on the mini with `just deploy-studiolive-bridge`.

- The DSP state port is `127.0.0.1:9002` (`STUDIOLIVE_DSP_PORT`), bound to loopback only.
- `/dsp/subscribe <port>` registers `127.0.0.1:<port>`, replays the state cache, sends `/dsp/replay_done <count>`, then forwards every osclive packet (bundles included). Subscribers lapse after 15 s without renewal, and at most 4 are accepted. `jive-room` renews every 5 s and sends `/dsp/unsubscribe` on exit.
- The bridge replies from its shared output socket, not from port 9002. Clients must accept packets from the bridge's IP, not only from port 9002.
- `/dsp/ui/...` received on the DSP port is forwarded to Open Stage Control only. Anything else received there is logged as `DSP port dropped … (read-only path)` and discarded. Nothing from the DSP port ever reaches osclive.
- `/dsp/...` arriving from Open Stage Control on UDP 9000 is swallowed rather than forwarded to osclive. Phase 4 will handle `/dsp/apply` and `/dsp/revert` there.
- `/bridge/resync` replays the cache to the UI only, as before.
- **Passwordless restarts.** [`integrations/studiolive/sudoers/studiolive-dsp`](../integrations/studiolive/sudoers/studiolive-dsp) allows `mixer` to run exactly `systemctl restart studiolive-bridge.service` and `systemctl restart studiolive-osc.service`, and nothing else. Install it with `sudo install -m 0440 -o root -g root studiolive-dsp /etc/sudoers.d/` and check it with `sudo visudo -cf`. Both deploy scripts try it first.
- **Restart side effect.** Without passwordless sudo, the deploy script restarts the bridge by SIGKILL plus `Restart=on-failure`. systemd 255 propagates that restart to `studiolive-osc` through `Requires=`, so Open Stage Control restarts too and browsers reconnect. Never deploy during a show.

### Apply engine (phase 4)

The bridge is the only component that writes to the desk on jive-room's behalf.

- **Submit.** `/dsp/proposal/set <json>` (DSP port) carries a `jive-room-proposal/v1` object: `id`, `source` (`ringout` or `profile`), `summary`, and `changes` as `{address, raw}`. The bridge checks structure and allow-list, then holds a single proposal pending, which the ROOM tab shows.
  - Allowed: channel HPF and EQ switches, frequencies and gains on ch1–12, and GEQ bands 1–31.
  - Rejected: faders, phantom, routing, mute, solo, sends, GEQ enable or reset, main/aux/FX channels, and ring-out proposals touching anything but the mid band or GEQ bands.
- **Apply.** `/dsp/apply <id>` (DSP port) or the ROOM tab's double-tap APPLY (`/dsp/apply 1`). The bridge refuses unless all of these hold:
  - osclive is connected and no other apply is running;
  - at least 1 s has passed since the last apply;
  - every target channel has been quiet on the desk for 1 s (`APPLY_SETTLE_SECONDS`).

  For ring-out proposals it also requires that gains only go down, by at most 26 raw steps (about 3 dB on the channel EQ model) per step, and that no boosting band is retuned.

  It then snapshots current raw values, writes only the differences at mid-step values 50 ms apart, and waits up to 3 s for read-back to match each raw target exactly. The snapshot is pushed onto an undo stack (20 deep, saved as `~/studiolive/dsp-undo.json`), and a line is appended to the audit log `~/studiolive/dsp-apply.log` (rotated at 1 MB).
- **Undo.** `/dsp/revert`, or the double-tap UNDO, restores the last snapshot through the same checks. The stack survives bridge restarts.
- **Results.** `jive-room-apply-result/v1` reports go to DSP subscribers as `/dsp/result <json>`, and to the UI as `/dsp/ui/apply/status`. The status is one of `pending`, `rejected`, `refused`, `verified`, `unverified` or `failed`.
- **Never.** The bridge does not write in response to incoming state, nor when a client disconnects. Raw `/channel/...` messages on the DSP port are dropped.
- **Ring-out apply** (`jive-room -studiolive-ringout -ringout-submit`) is refused in jive-room while the EQ calibration is unverified. The GEQ is unmappable until its dB-per-step is measured. `-allow-unverified-calibration` overrides only the channel-mid check, and the operator takes responsibility for it.
- **Profiles** need no calibration because they store the desk's own raw values:
  - `-studiolive-profile-save` captures 199 controls (all channels plus GEQ), or one channel's HPF and EQ.
  - `-studiolive-profile-push` prints a dry-run diff, and writes only with `-confirm-apply`.
  - `-studiolive-undo -confirm-apply` reverts the bridge's last apply.
- **Tests.**
  - `integrations/studiolive/bridge/test_bridge_dsp.py` runs the real bridge against `fake_osclive.py` on spare ports (`just test-bridge`, or the mini's osclive venv). It covers apply, verify and undo; the allow-list; ring-out policy; the settle window; no writes from state, disconnects or direct sends; the UI buttons; rate limiting; undo across a restart; and, with `JIVE_ROOM_BIN`, the CLI profile save, push and undo.
  - The fake mirrors osclive's echo-then-desk-confirmation behaviour.
  - All nine tests passed on the mini.

### First supervised write on the real desk

Not done yet; it needs the operator present. Suggested procedure:
1. `jive-room -studiolive-profile-save ~/studiolive/profiles/before.json`. A bench baseline already exists at `~/studiolive/profiles/bench-baseline-2026-10-19.json`.
2. Make a test profile that changes one harmless control on an unused channel, e.g. ch8 `eqhighgain` raw 128 → 102 with the band switched off.
3. `jive-room -studiolive-profile-push test.json`, check the diff, then add `-confirm-apply`. Watch the desk and the Open Stage Control EQ view, and run `journalctl -fu studiolive-osclive` to confirm only that control was set.
4. Run `jive-room -studiolive-undo -confirm-apply`, or double-tap UNDO, and confirm the value returns.
5. Repeat with the UI's APPLY button, using a pending proposal from `-studiolive-profile-push` … or from ring-out with `-ringout-submit`.

## Observed desk state (2026-10-19)

Read with `jive-room -studiolive-state` through the new bridge port. The bridge replayed 799 cached states, of which 500 are known controls.

- **Every channel's FireWire send is pre-DSP** (`post` off on ch1–12). A JACK capture of a channel therefore cannot show a channel EQ change. Verifying applied channel EQ needs post-DSP sends, which is an operator routing change, or an acoustic measurement.
- **Untouched EQ gains sit at raw 128,** while osclive's GEQ reset leaves bands at raw 127. With the linear ±15 dB model, raw 128 reads as +0.06 dB. Which raw value is the desk's true 0 dB is a calibration question for each control family.
- The GEQ is disabled and flat (all 31 bands at raw 127). Channels 6–12 have the HPF frequency at raw 96 (≈98 Hz on the model) with the HPF switched off.

## Phases

1. **Read-only JACK analyser.** Mostly done. The `-tags jack` backend, `-studiolive-channel` / `-jack-source`, the Ubuntu 24.04 image (`just build-studiolive`) and `just deploy-studiolive` are in place. On the mini the analyser reports 44.1 kHz, 1024-frame periods, zero xruns and zero analysis drops over a 60-second soak, and leaves no ports behind on exit. Still open: verify the stereo channel-to-stream mapping with signal present (all 16 streams were idle at about −114 dBFS during testing).
2. **Read-only mixer state.** Mostly done.
   - `internal/osc` is the codec. `internal/studiolive` holds the control model, proposable allow-list, 8-bit raw handling, state mirror, read-only `BridgeClient`, and the session-label calibration model.
   - `jive-room -studiolive-state [-studiolive-channel N]` prints the state, and `-studiolive-watch` streams changes.
   - Still open: verify calibration against the desk (set `Verified` only after a check), find each family's true 0 dB raw value, and measure the GEQ dB range.
3. **Advance mode, read-only.** In progress.
   - Done:
     - `internal/feedback` ring-out detector (synthetic-signal thresholds).
     - `studiolive.RingOutProposals` (GEQ band −3 dB without a raw target; channel Hi-Q mid −3 dB, capped at −6 dB, model raw values, conflict and pre-DSP notes; proposable controls only).
     - `jive-room -studiolive-ringout` (live, JACK plus bridge state plus `/dsp/ui/ringout/*` publishing) and `-ringout-file` (offline).
     - ROOM tab generated by `integrations/studiolive/osc/add_room_tab.py`, installed with `just deploy-studiolive-osc`.
     - Stage captures (`-studiolive-capture`), operator-started PA measurement (`-studiolive-pa-measure`, one preallocated JACK output port, −20 dBFS peak cap, stop fade), and `-session-dir` analysis.
     - A PA signal-to-noise gate against the empty room, and a −90 dBFS converter-floor gate on empty-room EQ advice.
   - Verified:
     - On the dummy JACK server in the Ubuntu image: sample-exact playback loopback, stop fade, exact-length stage capture, and the PA window kept clear of the fades.
     - On the mini: no ring-out candidates over 20 s on idle inputs; Open Stage Control serving v27; a 5 s empty-room capture with zero xruns and drops; 4 s of pink at −24 dBFS to `Ch 16_out`, sent only after every channel's `firewire` input switch read off, with no speakers connected.
     - The bench session analyses to PA `insufficient_signal` (median 0.2 dB margin) plus a converter-floor warning, with no EQ.
   - Not yet seen: a real ring, an acoustic PA measurement, or the ROOM tab rendered in a browser. These all need speakers.
   - osclive does not publish the masters block over OSC, so `monFirewire` cannot be checked before playback.
4. **Assisted apply and profiles.** Built and tested against the fake osclive; see "Apply engine".
   - Deployed on the mini.
   - Real-desk checks so far are non-writing only: saving a profile, and pushing it back as "already matches".
   - Still open: the first supervised real write, and verifying calibration so ring-out proposals can be applied.
5. **Live recommendations,** display only.
6. **Guarded automatic cuts,** off by default.

## Open questions to resolve on hardware

- Actual FireWire stream mapping for stereo channels 9–12, and what streams 15/16 carry.
- Current `post` state of each channel's FireWire send.
- GEQ raw-step-to-dB mapping; channel EQ frequency, gain, and HPF mappings.
- Whether the desk acknowledges every write (osclive notes `FIXME: Check for ACK?`), and the write latency.
