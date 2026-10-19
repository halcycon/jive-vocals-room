# StudioLive Mixer Project — Architecture & Cursor Handover

## Project goal

This project turns a **PreSonus StudioLive 16.0.2 FireWire mixer** into a remotely controllable, headless Linux-based mixer platform with:

- bidirectional mixer control from Open Stage Control,
- live metering,
- a per-channel real-time spectrum/RTA view,
- 16-channel multitrack recording,
- automatic startup on boot,
- remote access over LAN/Tailscale,
- and a clean path for future DSP/analysis software to read mixer audio and apply or recommend mixer changes in real time.

The permanent host is now a **2011 Mac mini** running **Xubuntu 24.04 LTS**.

The system is intended to run **without a monitor or graphical login** most of the time.

---

# 1. Hardware

## Mixer

**PreSonus StudioLive 16.0.2 FireWire**

Known mixer details:

- firmware: **0.98f build 219**
- 12 mono input channels exposed for control in the current UI
- 16 FireWire capture channels
- 16 FireWire playback channels
- 4 auxes
- FX A / FX B returns
- main bus
- graphic EQ
- FireWire/DICE interface
- mixer GUID:

```text
000a920404e1443c
```

## Host

**2011 Mac mini**

OS:

```text
Xubuntu 24.04 LTS
```

User:

```text
mixer
```

Hostname:

```text
studiolive
```

FireWire controller:

```text
Texas Instruments XIO2213A/B/XIO2221 IEEE-1394b OHCI
PCI ID: 104c:823f
Driver: firewire_ohci
```

This controller works successfully with FFADO/JACK and the StudioLive.

---

# 2. Important architectural decision

The Linux ALSA `snd_dice` PCM path proved unreliable for actual audio streaming with this mixer.

Direct ALSA capture using:

```bash
arecord -D hw:StudioLive ...
```

opened the interface correctly but immediately failed with:

```text
read error: Input/output error
```

This happened on both the Ubuntu 6.8 and 7.0 kernels and with PipeWire completely stopped, so the final design does **not** use ALSA/snd_dice for audio streaming.

Instead:

- **FFADO/JACK owns the FireWire audio stream**
- **osclive talks directly to the mixer over raw1394 for control**
- both coexist successfully at the same time

This is the core design to preserve.

---

# 3. Final high-level architecture

```text
                       ┌───────────────────────────┐
                       │ PreSonus StudioLive 16.0.2│
                       └─────────────┬─────────────┘
                                     │ FireWire
                  ┌──────────────────┴──────────────────┐
                  │                                     │
                  │                                     │
          raw1394 control                         FFADO audio
                  │                                     │
             ┌────▼─────┐                         ┌─────▼─────┐
             │ osclive  │                         │   JACK    │
             │ TCP 4301 │                         │ 44.1 kHz  │
             └────┬─────┘                         └─────┬─────┘
                  │ OSC/TCP                             │
                  │                                     │
         ┌────────▼────────┐                    ┌────────┴─────────┐
         │ custom OSC bridge│                    │                  │
         │ UDP <-> OSC/TCP │                    │                  │
         │ state cache     │                    │                  │
         │ RTA control     │                    │                  │
         │ recorder control│                    │                  │
         └────────┬────────┘                    │                  │
                  │                             │                  │
          OSC UDP │                             │                  │
          9000/9001                             │                  │
                  │                             │                  │
        ┌─────────▼───────────┐       ┌────────▼────────┐ ┌───────▼─────────┐
        │ Open Stage Control │       │ JACK RTA client │ │ JACK recorder   │
        │ headless under Node│       │ selected input  │ │ 16ch RF64/PCM24 │
        │ web UI :8080       │       │ 31 bands       │ │                 │
        └─────────────────────┘       └─────────────────┘ └─────────────────┘
```

---

# 4. Audio layer: FFADO + JACK

## Why FFADO

FFADO successfully detects the StudioLive:

```text
PreSonus - STUDIOLIVE_1602
GUID 000a920404e1443c
```

JACK is launched with:

```bash
jackd -d firewire -r 44100 -p 1024 -n 3
```

Current JACK configuration:

```text
Sample rate: 44100 Hz
Block size: 1024 frames
Periods: 3
Backend: firewire / FFADO
```

The mixer exposes:

```text
16 physical capture ports
16 physical playback ports
1 MIDI input
1 MIDI output
```

Example capture ports:

```text
firewire_pcm:000a920404e1443c_Ch 1_in
firewire_pcm:000a920404e1443c_Ch 2_in
...
firewire_pcm:000a920404e1443c_Ch 16_in
```

Example playback ports:

```text
firewire_pcm:000a920404e1443c_Ch 1_out
...
firewire_pcm:000a920404e1443c_Ch 16_out
```

Important JACK semantics:

- StudioLive inputs appear as JACK `output,physical,terminal` ports because they are signal sources feeding clients.
- StudioLive return channels appear as JACK `input,physical,terminal` ports.

## snd_dice

`snd_dice` is intentionally prevented from claiming the interface.

Configuration:

```text
/etc/modprobe.d/studiolive-blacklist.conf
```

Contents:

```text
blacklist snd_dice
```

The JACK service also runs:

```bash
modprobe -r snd_dice
```

before starting FFADO.

Do not redesign the system around `hw:StudioLive` ALSA capture unless there is a specific reason to revisit the kernel/driver issue.

---

# 5. Control layer: osclive

`osclive` is installed in:

```text
/home/mixer/.venvs/osclive/
```

Python:

```text
/home/mixer/.venvs/osclive/bin/python
```

Main executable:

```text
/home/mixer/.venvs/osclive/bin/osclive
```

It requires the separate Python `raw1394` extension from:

```text
martinspinler/pyraw1394
```

The Python module imported by osclive is:

```python
import raw1394
```

The direct FireWire control path works simultaneously with FFADO/JACK.

`osclive` successfully reads the full mixer state, including:

- channel gain
- pan
- linked pan
- HPF
- gate
- compressor
- EQ
- auxes
- FX returns
- main
- graphic EQ
- master settings

It ultimately reports:

```text
StudioLive: connected
```

## osclive network protocol

The control server speaks:

```text
OSC/TCP 1.0
length-prefixed frames
TCP port 4301
```

Open Stage Control does not directly speak this framing cleanly, which is why the custom bridge exists.

---

# 6. Custom OSC bridge

Current bridge:

```text
/home/mixer/studiolive/osc_tcp1_bridge_jack.py
```

Source lineage:

```text
osc_tcp1_bridge_jack_v2.py
```

The bridge performs several jobs.

## Protocol conversion

Open Stage Control:

```text
OSC/UDP
send to 127.0.0.1:9000
receive on UDP 9001
```

Bridge:

```text
UDP 9000 <-> OSC/TCP length-prefix <-> osclive TCP 4301
```

## State cache

The bridge caches the latest mixer state received from osclive.

Open Stage Control can request:

```text
/bridge/resync
```

The bridge then replays cached values to the UI only.

This is important because the selected-channel UI must **never write stale/default values to the desk merely because the selected channel changed**.

Previous versions of the UI accidentally risked writing pan/HPF/etc. on channel selection.

The current architecture avoids that by:

- keeping actual mixer state mirrored/cached,
- replaying state to the UI,
- only sending values to the mixer when the operator actually manipulates a control.

## RTA lifecycle commands

Handled locally by the bridge:

```text
/analyser/start
/analyser/stop
/analyser/toggle
/analyser/status
/analyser/select <1..12>
```

These commands are **not forwarded to osclive**.

## Recording lifecycle commands

Handled locally by the bridge:

```text
/recording/start
/recording/stop
/recording/status
```

## Recording status sent back to O-S-C

```text
/recording/running
/recording/elapsed
/recording/disk_free_gb
/recording/file
/recording/error
```

## RTA status sent back to O-S-C

```text
/analyser/running
/analyser/error
```

---

# 7. Open Stage Control

Version:

```text
Open Stage Control 1.30.3
```

The application bundle is installed at:

```text
/home/mixer/.local/opt/open-stage-control/open-stage-control_1.30.3_linux-x64/
```

The UI itself is **not** launched using Electron in production.

It runs headlessly using Node:

```bash
node \
  /home/mixer/.local/opt/open-stage-control/open-stage-control_1.30.3_linux-x64/resources/app/ \
  --no-gui \
  --load /home/mixer/studiolive/StudioLive1602_OpenStageControl_v26.json \
  --port 8080 \
  --osc-port 9001 \
  --send 127.0.0.1:9000
```

Current session:

```text
/home/mixer/studiolive/StudioLive1602_OpenStageControl_v26.json
```

Web interface:

```text
http://<host-ip>:8080
```

The machine also has Tailscale, so the UI can be reached remotely via the Tailscale IP when appropriate.

---

# 8. Current O-S-C UI structure

Current session:

```text
StudioLive1602_OpenStageControl_v26.json
```

Main tabs:

```text
MIX
EQ
DYNAMICS
METERS
```

## MIX

Includes:

- 12 input channel strips
- channel selection
- mute
- solo
- faders
- selected-channel editing

## EQ

Selected-channel controls for:

- low EQ
- mid EQ
- high EQ
- frequency
- gain
- shelf / Hi-Q where applicable

## DYNAMICS

Selected-channel controls for:

- gate
- gate threshold
- compressor threshold
- ratio
- response
- makeup gain

## METERS

Includes:

- 12 input meters
- Aux 1–4 meters
- Main L/R meters
- 31-band selected-channel RTA
- analyser channel selection
- RTA start/stop/status
- REC / STOP controls
- recording status
- elapsed time
- free disk space
- current recording filename

Current layout version:

```text
v26
```

v26 fixed:

- clipped meter scales,
- recording status overlap,
- channel meter rendering width.

---

# 9. Physical fader calibration

The StudioLive physical fader position is not linearly equivalent to dB.

Measured calibration:

```text
-inf 0.016
-60  0.024
-50  0.075
-40  0.129
-30  0.247
-20  0.376
-10  0.502
-5   0.624
U    0.769
+5   0.906
+10  1.000
```

The O-S-C faders use this custom range.

Do not replace this with a naïve linear dB mapping unless intentionally recalibrating.

---

# 10. RTA / spectrum analyser

Current script:

```text
/home/mixer/studiolive/studiolive_spectrum_jack.py
```

Important design point:

The RTA is a **JACK client**.

It does **not** open ALSA directly.

It:

1. creates one JACK input,
2. connects it dynamically to the currently selected StudioLive input channel,
3. receives the selected channel over OSC,
4. performs FFT analysis,
5. publishes 31 bands to Open Stage Control.

Selected source naming pattern:

```text
firewire_pcm:000a920404e1443c_Ch N_in
```

Selection OSC:

```text
/select N
```

Local UDP control port:

```text
9100
```

RTA results sent to O-S-C:

```text
/analyser/band/1
...
/analyser/band/31
```

31 centre frequencies:

```text
20
25
32
40
50
63
80
100
125
160
200
250
320
400
500
640
800
1000
1300
1600
2000
2500
3200
4000
5000
6400
8000
10000
13000
16000
20000 Hz
```

The analyser follows selected-channel changes successfully in real time.

Example bridge output:

```text
[RTA] RTA connected to firewire_pcm:000a920404e1443c_Ch 1_in
[RTA] RTA connected to firewire_pcm:000a920404e1443c_Ch 2_in
[RTA] RTA connected to firewire_pcm:000a920404e1443c_Ch 3_in
```

Intentional RTA shutdowns are distinguished from crashes, so STOP does not produce a false `RTA ERROR`.

---

# 11. Multitrack recorder

Recorder script:

```text
/home/mixer/studiolive/studiolive_jack_recorder.py
```

The recorder is also a **JACK client**.

It connects to:

```text
Ch 1_in
...
Ch 16_in
```

and records all 16 channels simultaneously.

Recording directory:

```text
/home/mixer/Recordings/StudioLive/
```

Filename pattern:

```text
StudioLive_YYYY-MM-DD_HH-MM-SS.rf64.wav
```

Format:

```text
RF64
16 channels
44.1 kHz
24-bit PCM
```

RF64 is deliberately used to avoid the ordinary WAV 4 GiB file size limit.

The recorder writes audio through a queue so disk I/O does not happen directly inside the JACK process callback.

Confirmed live test:

```text
SAMPLE_RATE=44100
CHANNELS=16
DROPPED_BLOCKS=0
```

---

# 12. systemd boot architecture

The system boots the complete stack automatically.

Services:

```text
studiolive-jack.service
studiolive-osclive.service
studiolive-bridge.service
studiolive-osc.service
```

All are enabled.

## Dependency order

```text
studiolive-jack
    ↓
studiolive-osclive
    ↓
studiolive-bridge
    ↓
studiolive-osc
```

## JACK service

File:

```text
/etc/systemd/system/studiolive-jack.service
```

Core command:

```bash
/usr/bin/jackd -d firewire -r 44100 -p 1024 -n 3
```

Realtime configuration:

```text
LimitRTPRIO=95
LimitMEMLOCK=infinity
Nice=-10
```

Runs as:

```text
User=mixer
Group=audio
```

## osclive service

File:

```text
/etc/systemd/system/studiolive-osclive.service
```

Exec:

```text
/home/mixer/.venvs/osclive/bin/osclive
```

## bridge service

File:

```text
/etc/systemd/system/studiolive-bridge.service
```

Exec:

```text
/home/mixer/.venvs/osclive/bin/python \
/home/mixer/studiolive/osc_tcp1_bridge_jack.py
```

Environment:

```text
STUDIOLIVE_ANALYSER=/home/mixer/studiolive/studiolive_spectrum_jack.py
STUDIOLIVE_RECORDER=/home/mixer/studiolive/studiolive_jack_recorder.py
STUDIOLIVE_RECORD_DIR=/home/mixer/Recordings/StudioLive
```

The service waits for osclive TCP 4301 before launching:

```bash
until /usr/bin/nc -z 127.0.0.1 4301; do sleep 1; done
```

## Open Stage Control service

File:

```text
/etc/systemd/system/studiolive-osc.service
```

Runs directly under Node and does **not** require X11 or a logged-in GUI session.

The complete stack has been reboot-tested successfully and comes back automatically without logging into Xfce.

---

# 13. Useful service commands

Status:

```bash
systemctl --no-pager --full status \
  studiolive-jack \
  studiolive-osclive \
  studiolive-bridge \
  studiolive-osc
```

Live logs:

```bash
journalctl -f \
  -u studiolive-jack \
  -u studiolive-osclive \
  -u studiolive-bridge \
  -u studiolive-osc
```

Restart control stack only:

```bash
sudo systemctl restart \
  studiolive-osclive \
  studiolive-bridge \
  studiolive-osc
```

Restart everything:

```bash
sudo systemctl restart \
  studiolive-jack \
  studiolive-osclive \
  studiolive-bridge \
  studiolive-osc
```

---

# 14. Mixer OSC address model

Important input channel addresses follow this pattern:

```text
/channel/ch1/gain
/channel/ch1/pan
/channel/ch1/linkedpan

/channel/ch1/aux1
/channel/ch1/aux2
/channel/ch1/aux3
/channel/ch1/aux4

/channel/ch1/fxa
/channel/ch1/fxb

/channel/ch1/hpf
/channel/ch1/hpffreq

/channel/ch1/gate
/channel/ch1/gatethresh

/channel/ch1/phantom
/channel/ch1/firewire
/channel/ch1/phase
/channel/ch1/post
/channel/ch1/mute
/channel/ch1/solo

/channel/ch1/comp
/channel/ch1/compauto
/channel/ch1/complimit
/channel/ch1/compthresh
/channel/ch1/compratio
/channel/ch1/compresponse
/channel/ch1/compgain

/channel/ch1/eqlow
/channel/ch1/eqlowfreq
/channel/ch1/eqlowgain
/channel/ch1/eqlowshelf

/channel/ch1/eqmid
/channel/ch1/eqmidfreq
/channel/ch1/eqmidgain
/channel/ch1/eqmidhiq

/channel/ch1/eqhigh
/channel/ch1/eqhighfreq
/channel/ch1/eqhighgain
/channel/ch1/eqhighshelf

/channel/ch1/toMain
```

Replace `ch1` with:

```text
ch1 ... ch12
```

Aux buses:

```text
/channel/aux1/...
...
/channel/aux4/...
```

FX:

```text
/channel/fxa/...
/channel/fxb/...
```

Main:

```text
/channel/main/...
```

Graphic EQ:

```text
/channel/geq0/20Hz
...
/channel/geq0/20kHz
```

Masters include:

```text
/masters/monLevelMain
/masters/monLevelSolo
/masters/fxagain
/masters/fxbgain
/masters/monLevelPhones
/masters/soloPFL
```

---

# 15. Mixer control value model

Most mixer parameters use normalised values:

```text
0.0 -> 1.0
```

Do not assume every normalised value maps linearly to an engineering unit.

Examples:

- channel fader values require calibrated nonlinear mapping,
- EQ frequency values map to frequency ranges,
- compressor ratio and response are device-specific,
- pan centre is around 0.5 but board values may not be exactly 0.500,
- UI display rounding must never force writes back to the board.

For any DSP automation project, use the existing mixer values as the source of truth and only write when deliberately applying a change.

---

# 16. Important UI/state safety rule

This is critical for any future automation.

**Selecting or inspecting a channel must never cause parameter writes.**

Previously, a UI approach that copied hidden mirror values into visible selected controls risked setting mixer values accidentally.

The safe architecture is:

```text
mixer state
    ↓
osclive
    ↓
bridge cache
    ↓
UI / analyser / recommendation engine
```

Only an explicit operator action or explicit automation decision should produce:

```text
write → mixer
```

For future DSP code, preserve the same rule:

```text
READ != WRITE
```

---

# 17. Proposed integration with the live-vocals DSP project

The existing architecture is now extremely suitable for a real-time vocal DSP/advisory layer.

The new project should ideally be implemented as **another JACK + OSC client**, not by modifying the low-level FireWire stack.

Recommended architecture:

```text
StudioLive input
      │
      ▼
FFADO/JACK
      │
      ├── recorder
      ├── RTA
      └── vocal DSP analyser
              │
              ├── feature extraction
              │   ├── spectrum
              │   ├── pitch
              │   ├── resonances
              │   ├── sibilance
              │   ├── plosives
              │   ├── dynamic range
              │   └── feedback-risk indicators
              │
              ├── recommendation engine
              │
              └── OSC control client
                        │
                        ▼
                    bridge
                        │
                        ▼
                     osclive
                        │
                        ▼
                  StudioLive EQ/dynamics
```

---

# 18. Suggested DSP integration phases

## Phase 1 — read-only advisory mode

The safest initial implementation.

The DSP process should:

1. connect to one or more JACK input channels,
2. read current mixer parameters through the bridge/osclive state,
3. analyse the live vocal signal,
4. generate recommendations,
5. display recommendations in the UI,
6. make **no automatic mixer writes**.

Example recommendations:

```text
CH1:
- HPF appears too low for this source
- Suggested HPF: ~105 Hz

Mid EQ:
- Possible nasal resonance around 850 Hz
- Suggested cut: -2.5 dB, medium Q

High EQ:
- Sibilance energy elevated around 6.5–8 kHz
- Consider de-essing / gentle HF reduction

Compressor:
- Current threshold may be too high
- Suggested threshold: -18 dB
- Suggested ratio: 3:1
```

This mode is useful for validating DSP logic safely.

---

## Phase 2 — assisted apply

Add buttons to Open Stage Control such as:

```text
APPLY HPF
APPLY EQ
APPLY COMP
APPLY ALL
UNDO
```

The DSP engine generates proposed changes, but the operator explicitly approves them.

The bridge should ideally implement a dedicated automation namespace such as:

```text
/dsp/ch1/recommendation/...
/dsp/ch1/apply
/dsp/ch1/revert
```

rather than having the DSP process directly spray mixer OSC commands.

This keeps policy and state handling centralised.

---

## Phase 3 — guarded automatic adjustment

Only after the recommendation system is proven.

Automatic changes should include:

- maximum adjustment size per step,
- maximum adjustment rate,
- safe EQ bounds,
- safe compressor bounds,
- per-parameter enable/disable,
- operator lockout,
- emergency disable,
- stored previous-state snapshot,
- revert/undo.

Recommended guardrails:

```text
Never change gain/fader automatically by default.
Never toggle phantom automatically.
Never change routing automatically.
Never mute/solo automatically unless explicitly designed.
Never alter unrelated channels.
Never apply large EQ moves in a single step.
Never continuously chase short-term spectral fluctuations.
```

---

# 19. Recommended DSP process design

A good independent service might be:

```text
studiolive-vocal-dsp.service
```

Process responsibilities:

```text
JACK client:
    vocal_dsp

JACK inputs:
    ch1
    ch2
    ...

OSC:
    read state via bridge
    publish recommendations to O-S-C
    submit controlled writes via bridge
```

Suggested internal modules:

```text
audio/
    jack_client.py
    ring_buffer.py

analysis/
    spectrum.py
    pitch.py
    resonance.py
    sibilance.py
    dynamics.py
    feedback.py

mixer/
    state.py
    mappings.py
    osc_client.py
    constraints.py

recommendation/
    eq.py
    hpf.py
    compressor.py
    confidence.py

ui/
    osc_publish.py

automation/
    apply.py
    undo.py
    safety.py
```

---

# 20. Suggested DSP state model

For each channel:

```python
ChannelState:
    mixer:
        gain
        pan
        hpf_enabled
        hpf_freq
        eq_low_enabled
        eq_low_freq
        eq_low_gain
        eq_mid_enabled
        eq_mid_freq
        eq_mid_gain
        eq_high_enabled
        eq_high_freq
        eq_high_gain
        gate_enabled
        gate_threshold
        comp_enabled
        comp_threshold
        comp_ratio
        comp_response
        comp_gain

    analysis:
        rms
        peak
        crest_factor
        spectral_centroid
        pitch
        resonances
        sibilance_score
        plosive_score
        feedback_risk

    recommendation:
        proposed_changes
        confidence
        explanation
        timestamp
```

---

# 21. Mixer-state access for future DSP

There are two sensible approaches.

## Option A — extend the existing bridge

Preferred.

Add a small subscription/state API to the bridge.

For example:

```text
/dsp/state/ch1/...
```

The bridge already has the latest state cache from osclive, so this avoids duplicate connections and keeps one canonical mixer-state broker.

Advantages:

- one osclive TCP client,
- one state cache,
- easier undo/history,
- easy safety gating,
- easier UI updates,
- automation policy stays central.

## Option B — DSP connects directly to osclive

Technically possible but less attractive.

The DSP app would need to:

- implement osclive OSC/TCP framing,
- maintain its own mixer-state cache,
- coordinate writes with Open Stage Control,
- avoid state races.

For Cursor work, prefer **Option A** unless there is a strong reason otherwise.

---

# 22. Recommended new OSC namespace for DSP

Suggested namespace:

```text
/dsp/status
/dsp/enabled

/dsp/selected_channel

/dsp/ch1/confidence
/dsp/ch1/summary

/dsp/ch1/hpf/suggested
/dsp/ch1/hpf/confidence

/dsp/ch1/eq/low/freq
/dsp/ch1/eq/low/gain

/dsp/ch1/eq/mid/freq
/dsp/ch1/eq/mid/gain
/dsp/ch1/eq/mid/q

/dsp/ch1/eq/high/freq
/dsp/ch1/eq/high/gain

/dsp/ch1/comp/threshold
/dsp/ch1/comp/ratio
/dsp/ch1/comp/gain

/dsp/ch1/apply
/dsp/ch1/revert
```

For actual mixer writes, the bridge can translate validated DSP commands into existing mixer OSC addresses.

---

# 23. Safety requirements for automatic mixer changes

Cursor should treat these as hard requirements.

## Never automate

Unless explicitly enabled later:

```text
phantom power
channel routing
FireWire routing
mute
solo
main routing
master levels
FX routing
```

## Parameter limits

Add central clamp functions.

Examples:

```text
HPF:
    sensible vocal range only

EQ:
    max ±3 dB automatic move per decision
    max cumulative automatic boost/cut configurable

Compressor:
    threshold/ratio bounded
    makeup gain tightly limited

Timing:
    minimum interval between automatic changes
    hysteresis required
    confidence threshold required
```

## Snapshot before write

Before applying a group of changes:

```text
capture current mixer state
store snapshot
apply changes
allow undo
```

## Fail safe

If DSP disconnects or crashes:

```text
do nothing to mixer
leave existing mixer state untouched
```

The bridge must not reset values on client disconnect.

---

# 24. Recording and DSP coexistence

JACK is specifically useful because multiple clients can consume the same StudioLive input stream at once.

The following can run simultaneously:

```text
JACK
├── multitrack recorder
├── RTA
├── vocal DSP analyser
└── optional future DAW / monitor tools
```

The DSP project should therefore consume JACK ports and **not** try to open the FireWire device directly.

---

# 25. Performance notes

The current machine handles:

```text
16-channel FireWire stream
JACK @ 44.1 kHz
1024 frame buffer
RTA
recording
osclive
Open Stage Control
```

with stable operation.

A 16-channel recording test completed with:

```text
Overruns: 0
Xruns: 0
DROPPED_BLOCKS=0
```

The current JACK buffer is intentionally conservative.

For recommendation DSP there is no need to reduce latency aggressively unless the project begins inserting processed audio back into the live signal path.

For advisory analysis, a 1024-frame JACK block is acceptable.

---

# 26. Disk/storage notes

The internal SSD is approximately:

```text
112 GB
```

16-channel recording can consume significant space.

The Open Stage Control UI already displays free disk space from:

```text
/recording/disk_free_gb
```

Any future DSP service should avoid writing large analysis logs continuously unless rotation/limits are implemented.

---

# 27. Current working files

Main project files on host:

```text
/home/mixer/studiolive/
├── StudioLive1602_OpenStageControl_v26.json
├── osc_tcp1_bridge_jack.py
├── studiolive_spectrum_jack.py
└── studiolive_jack_recorder.py
```

Virtual environment:

```text
/home/mixer/.venvs/osclive/
```

Recordings:

```text
/home/mixer/Recordings/StudioLive/
```

Open Stage Control runtime:

```text
/home/mixer/.local/opt/open-stage-control/
└── open-stage-control_1.30.3_linux-x64/
```

---

# 28. Current known rough edge

Changing selected channels can trigger multiple:

```text
/bridge/resync
```

requests because selector state exists across several tabs.

Typical bridge output:

```text
Replayed 799 cached mixer states
```

sometimes appears multiple times during one selection.

This is currently harmless, but a future cleanup could debounce `/bridge/resync` or eliminate duplicate selector-triggered resyncs.

Do not confuse this with a mixer feedback loop.

---

# 29. Important historical issue to watch for

During early Bazzite development there was once suspicious continuous CH1 parameter update traffic after UI/bridge activity.

It was not consistently reproducible.

When modifying the bridge or adding DSP writes, watch for the difference between:

```text
StudioLive: Upd ...
```

and genuine mixer writes such as:

```text
StudioLive: Set ...
```

A recommendation engine must not create an OSC feedback loop where a state update is interpreted as a new requested write.

---

# 30. Suggested next task for Cursor

The live-vocals DSP project should be refactored so that its audio source is JACK rather than a standalone capture device.

Recommended initial milestone:

```text
1. Connect existing vocal DSP engine to:
   firewire_pcm:000a920404e1443c_Ch N_in

2. Allow channel selection.

3. Subscribe to current mixer state through the existing bridge.

4. Produce a structured recommendation object:
   HPF
   EQ low
   EQ mid
   EQ high
   compressor

5. Publish recommendations over OSC to Open Stage Control.

6. Add a read-only recommendation panel to the UI.

7. Do NOT apply mixer changes automatically yet.

8. Add an explicit APPLY button once recommendations have been validated.
```

A useful first API between DSP and bridge could be JSON internally, translated to OSC externally.

Example recommendation:

```json
{
  "channel": 1,
  "timestamp": 1792400000,
  "confidence": 0.84,
  "changes": {
    "hpf": {
      "enabled": true,
      "frequency_hz": 105
    },
    "eq_mid": {
      "frequency_hz": 820,
      "gain_db": -2.5
    },
    "compressor": {
      "threshold_db": -18,
      "ratio": 3.0
    }
  },
  "reasoning": [
    "Low-frequency energy below vocal fundamental is elevated",
    "Persistent resonance detected near 820 Hz",
    "Dynamic range suggests moderate compression"
  ]
}
```

---

# 31. Design principle for Cursor

The existing mixer gateway is now stable.

Treat this stack as infrastructure:

```text
FFADO/JACK
osclive
bridge
Open Stage Control
```

The new DSP project should plug into it rather than replacing it.

Preferred additions:

```text
new JACK client
new bridge DSP namespace
new O-S-C recommendation panel
new guarded apply/revert logic
```

Avoid changing:

```text
FireWire ownership
FFADO/JACK launch model
osclive/raw1394 control model
existing mixer-state cache semantics
```

unless there is a specific problem that requires it.

---

# 32. Final architecture summary

The StudioLive system is now:

```text
HEADLESS
BOOT-AUTOMATIC
BIDIRECTIONAL
MULTITRACK-CAPABLE
RTA-CAPABLE
JACK-BASED
OSC-CONTROLLABLE
DSP-EXTENSIBLE
```

The most important foundation for the vocal-DSP project is already present:

- every mixer input is available as a JACK source,
- mixer state is readable in real time,
- mixer parameters are writable through OSC,
- the UI already has a managed local-control namespace,
- automatic services survive reboot,
- audio capture and mixer control coexist successfully.

The next phase should build a **read-only vocal recommendation engine first**, then add operator-approved mixer changes, and only later consider guarded automatic control.
