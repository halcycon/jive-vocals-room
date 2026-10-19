# Jive Room operator guide

`jive-room` is a room/PA sound-check assistant. It measures what is happening in the room, then suggests **bounded, explainable starting points** for the mixer. You still listen, you still decide, and you still verify.

It is not `jive-vocals`. Vocals processes recorded podcast files to -16 LUFS. Room helps you set up a space before people talk through a PA.

It does **not** play audio, raise PA gain, or flatten a room. Static EQ cannot remove audience chatter. Deep room nulls must not be boosted. Tonal hum is evidence to investigate, not something to hide with a notch and walk away.

Full product rules live in [Room-Live-DSP-Architecture.md](Room-Live-DSP-Architecture.md). This page is how you actually use the tool.

## Four different problems

Keep these separate. Mixing them up is how people chase the wrong knob.

| Problem | What you measure | What EQ can do |
| --- | --- | --- |
| Ambient room noise (HVAC, hum, fans, chatter) | Empty and occupied captures | Almost nothing useful. Find the source. Chatter is masking, not a tone to cut. |
| Loudspeaker + room transfer | Pink-noise reference vs microphone capture | Conservative cuts (and tiny boosts) of **broad** excesses. Not null inversion. |
| Speech-to-noise / masking | Presenter test vs the current noise floor | Placement, HPF, a low-mid cut. Not “more master gain” from software. |
| Acoustic feedback | Later live work; not automatic yet | Stop raising gain. The live analyser can show a rising narrow peak while you ring out **by hand**. |

## Build

Offline analysis needs no PipeWire headers:

```bash
just build-room
./jive-room -empty empty.wav -venue "Hall"
```

The live analyser links PipeWire. On this machine that is meant to happen **inside the Fedora Podman image**, not by installing `pipewire-devel` on the host. See [dev-container.md](dev-container.md).

```bash
just container build-live
just container live          # TUI, host PipeWire socket bind-mounted
# or, after build-live in the container / distrobox:
./jive-room -live
./jive-room -live -live-plain   # one status line, for logs / no TTY
```

Default `just test` / `just build-room` stay hermetic and do not link libpipewire.

## Offline sound-check (the main workflow)

Capture 10–20 seconds per stage. Shorter files still analyse; the report will nag. Use a measurement microphone if you have one, otherwise the presenter’s mic and honesty about that.

### 1. Empty room

Room quiet. HVAC as it will be for the event. No speech, no PA programme.

```bash
./jive-room -empty empty.wav -venue "St Mary's" -out session.json
```

You get `session.json` and `session.md`. Levels are **relative FFT/dBFS**, not calibrated SPL.

### 2. Occupied room (optional, do it)

Audience in, no amplified speech.

```bash
./jive-room -empty empty.wav -occupied occupied.wav -out session.json
```

A large occupied delta is a **masking warning**. Do not try to EQ the crowd out.

### 3. PA / room response

Generate a test signal. **You** play it through the PA at a safe level **you** set. `jive-room` never starts playback and never touches PA gain.

```bash
./jive-room -generate-pink pink.wav
# play pink.wav through the PA yourself; record the room with the measurement mic
./jive-room -empty empty.wav \
  -pa-reference pink.wav -pa-measured pa-capture.wav \
  -out session.json
```

The transfer is a measured/reference power ratio, then shifted so the 500–2000 Hz median is 0 dB. That shift is not a gain recommendation.

Cuts come first. Boosts are capped at +3 dB. Deep/narrow dips are `do_not_boost` (nulls and comb notches). Do not invert them.

### 4. Presenter test (optional)

One person talks as they will in the event. Compare against empty or occupied noise on the **same** FFT/dBFS axis. The presence margin (1.5–4 kHz) is an **estimate** from separate captures, not a simultaneous SNR.

```bash
./jive-room -empty empty.wav -occupied occupied.wav -presenter speech.wav -out session.json
```

### 5. Apply on the mixer, then verify

Read `session.md`. Set the recommended frequencies on the **actual mixer you have** (the JSON already maps onto a mixer model). Listen. Then capture again:

```bash
./jive-room -empty empty.wav \
  -applied-eq applied.json \
  -verification after.wav \
  -out session.json
```

`applied.json` is an array of `{ "frequency_hz", "gain_db", "q" }`. Verification notes whether the measured problem moved, not whether the mix “sounds good”.

Optional relative microphone calibration (frequency/level points). This is **not** an SPL calibration; `spl_calibrated` stays false.

```bash
./jive-room -empty empty.wav -mic-cal mic.json -mic-identity "MM-1 s/n 12"
```

## Reading the report

- **Facts** (RMS, band levels, transfer features, hum harmonics) are measurements.
- **Advice** is labelled as a starting point. Confidence is not a calibrated probability.
- Hum → investigate mains, HVAC, dimmers, cable routes. A notch is a last resort.
- Occupied rise → placement and gain-before-feedback, not graphic-EQ surgery.
- `do_not_boost` → leave the hole. Move the mic or the speaker if it matters.

## Live analyser (watching while you tune)

`-live` is a **read-only side tap** of a PipeWire input (or JACK; see below). It does not sit in the speaker path, does not EQ, and does not change gain. Use it while you turn knobs on the **hardware mixer** (or, later, on the software EQ once pass-through exists).

On a TTY it opens a spectrum TUI:

- **RMS sparkline** over the last several seconds (gping-style time series).
- **15-band bars** at the same centres Jive Vocals uses: `80, 125, 195, 290, 440, 660, 1000, 1500, 2250, 3350, 5000, 7500, 11200, 16000, 24000 Hz`.
- **Peak-hold dots** so a brief ring does not vanish before you see it.
- **Negotiated sample rate and quantum**, as reported by PipeWire, plus **audio xruns** vs **analysis drops** (a dropped FFT window is not an audible xrun).
- Strongest band, spectral flatness, a tonal candidate, and likely mains hum when it persists.

```
jive-room live analyser  ·  read-only  ·  no playback  ·  no PA gain
rate 48000 Hz   quantum 128 frames (2.67 ms reported)   rms -32.1 dBFS   peak -18.4 dBFS
...
RMS  ▁▂▃▄▅▆▇█▇▆▅▄▃▂▁▂▃▄▅

 -20 ┤          █
 -40 ┤    █  █  █  █
     └  80 125 195 290 ...
   Δ           -3
```

Keys:

| Key | Action |
| --- | --- |
| Space | Freeze the current 15-band view as a **reference** |
| r | Clear freeze and peak-hold |
| q / Ctrl+C | Quit |

Non-TTY (or `-live-plain`) prints one line per analysis tick instead.

Each analysis window spans about 0.9 s (nine overlapping 8192-point FFT frames), so a tonal candidate must persist across most of that window before it is shown. A tone line reports prominence relative to the local spectrum, not an absolute level. On an idle StudioLive input the analyser reports steady lines at a quarter and an eighth of the sample rate (11025 and 5512 Hz at 44.1 kHz) on a −116 dBFS floor. These are real converter or stream idle tones, but they are far below anything audible.

### StudioLive over JACK

On the StudioLive Mac mini, audio comes from FFADO/JACK rather than PipeWire. `jive-room` joins the JACK graph as one more read-only client beside the RTA and recorder. It registers input ports only, so it cannot play audio, and it makes no mixer writes.

```bash
jive-room -studiolive-channel 3                  # TUI for desk channel 3
jive-room -studiolive-channel 10 -live-plain     # stereo channel 10, one line per tick
jive-room -jack-source '_Ch 15_in' -jack-source '_Ch 16_in'   # explicit FireWire streams
```

Reading the desk's own settings goes through the bridge's read-only DSP port:

```bash
jive-room -studiolive-state -studiolive-channel 3   # HPF / EQ for channel 3 plus the main GEQ
jive-room -studiolive-state                         # all 12 channels
jive-room -studiolive-watch                         # print each control change as it happens
```

Frequencies and gains come from a model built from the Open Stage Control knob labels (log frequency, linear ±15 dB gain). They have not yet been checked against the desk, and the raw 8-bit value is always shown alongside. GEQ bands are shown as raw steps from flat because their dB size is not yet known.

### Sound-check captures over JACK (StudioLive)

Record each stage straight from a desk channel into a session folder, then analyse the folder:

```bash
S=~/sessions/hall-2026-10-19
jive-room -studiolive-capture empty     -studiolive-channel 12 -capture-seconds 15 -session-dir $S
jive-room -studiolive-capture occupied  -studiolive-channel 12 -session-dir $S
jive-room -studiolive-capture presenter -studiolive-channel 3  -session-dir $S
jive-room -studiolive-pa-measure -studiolive-channel 12 -play-pink-to '_Ch 15_out' -confirm-playback -session-dir $S
jive-room -session-dir $S -venue "Hall"        # writes $S/jive-room-session.json and .md
# after applying EQ on the desk:
jive-room -studiolive-capture verification -studiolive-channel 12 -session-dir $S
jive-room -session-dir $S -applied-eq applied.json
```

- **Captures** are mono 32-bit float WAV. Each one has a `<name>.capture.json` sidecar recording the sample rate, sources, xruns, analysis drops, RMS, peak, and notes for clipping, converter-floor level, or xruns. If the analysis ring dropped any frames, the capture is **discarded** rather than saved with gaps. Ctrl+C saves nothing.
- **PA measurement plays audio,** so it runs only with `-confirm-playback`. Without that flag it prints the pre-flight checklist instead:
  1. Route the chosen FireWire return to only the PA you intend to measure.
  2. Start with the fader down and raise it slowly.
  3. Remember that jive-room never changes desk gain.

  The pink noise is generated at the JACK rate and capped at −20 dBFS peak (`-pink-headroom-db` 20–24). It goes to exactly one named physical playback port. Ctrl+C fades it out over about 23 ms.

  Only the steady part is recorded: from 0.5 s after the fade-in until the fade-out. The reference is saved as `pa-reference.wav` plus `.signal.json`, and the capture as `pa-measured.wav`. Use the measurement-mic channel, set up as for the empty room.
- **Signal-to-noise gate.** The PA capture is compared with the empty-room capture on the same fine-spectrum axis.
  - Any feature within ±1/6 octave of a point less than 10 dB above the room noise is excluded.
  - If the median margin across 80 Hz–10 kHz is under 10 dB, the PA result is `insufficient_signal`, with no features and no PA EQ. Raise the PA level on the desk and measure again.
- **Converter floor.** An empty-room capture at or below −90 dBFS RMS is treated as converter floor, not room noise. You get a warning and no room EQ.

The ROOM tab's italic status line follows each capture as it runs.

### Profiles, apply, and undo (StudioLive)

```bash
jive-room -studiolive-profile-save ~/studiolive/profiles/hall.json -profile-label "Hall, Sunday"   # read-only
jive-room -studiolive-profile-push ~/studiolive/profiles/hall.json                 # dry run: shows the diff
jive-room -studiolive-profile-push ~/studiolive/profiles/hall.json -confirm-apply  # write through the bridge
jive-room -studiolive-undo -confirm-apply                                          # put the previous values back
```

- **Profiles** hold only channel HPF and EQ plus the 31 GEQ bands, as the desk's own raw values. A push never touches faders, phantom, routing, mute, solo, sends, or GEQ enable or reset.
- **The bridge does the writing.** It refuses while a target channel is being adjusted on the desk, snapshots the current values first, writes only what differs, and confirms each value on the desk before reporting `verified`.
- **Undo** reverts the most recent apply, and the bridge remembers 20 of them, even across restarts.
- **The ROOM tab** shows the pending proposal and the last result. APPLY and UNDO need a double-tap.
- **Ring-out cuts** can be held pending for APPLY with `-ringout-submit`, but only once the EQ calibration has been verified against the desk. Until then they are refused, and graphic EQ cuts are never mappable.

### Ringing out a channel (StudioLive)

```bash
jive-room -studiolive-ringout -studiolive-channel 3   # live, read-only; also feeds the ROOM tab
jive-room -ringout-file ringout-take.wav              # same detector over a recording
```

The assistant listens to one channel over JACK, flags ringing candidates and proposes cuts. It never plays audio, changes gain or writes to the desk. **You** raise gain slowly on the desk and stop as soon as anything rings.

A candidate needs all of the following:
- It sits at least 15 dB above its ±1/6-octave neighbourhood.
- It is louder than −70 dBFS, which rules out converter idle tones.
- It lasts at least 0.5 s and is present in at least 80% of frames.
- Its frequency holds to within 0.2%, which rules out gliding speech.

Growth of 3 dB/s or more raises confidence. Peaks related to others by an integer ratio (a voice or an instrument) are shown as `harmonic, likely source` with low confidence and no proposal. These thresholds are justified only by synthetic signals so far. Treat confidence as a ranking, not a probability, until they have been checked against real ring-outs.

For each ring the assistant proposes two options; choose one:
- **Main GEQ:** the nearest of the 31 bands, −3 dB. No raw desk value is proposed, because the GEQ's dB-per-step is not yet calibrated. If the GEQ is switched off, the proposal says so.
- **Channel mid EQ,** for rings between 260 Hz and 3.5 kHz: Hi-Q on, centred on the ring, −3 dB below the current mid gain, never below −6 dB. Raw values come from the unverified knob-label model. The proposal warns if the mid band is already in use elsewhere, and notes that with a pre-DSP FireWire send the cut will not show in this channel's capture.

Re-test after each cut. If a ring keeps coming back after one or two cuts, pull gain back instead of cutting deeper.

The **ROOM** tab in Open Stage Control shows the same status, candidates and proposals. Its widgets are display-only and never send OSC.

`-studiolive-channel` maps desk channels 1–8 to FireWire streams 1–8, and stereo channels 9–12 to stream pairs 9/10, 11/12, 13/14 and 15/16. That mapping follows osclive's meter layout and has **not yet been verified with signal on the hardware**. To check it, play a source into one side of stereo channel 10 and confirm that `-jack-source '_Ch 11_in'` or `'_Ch 12_in'` moves. The `xruns` count covers the whole JACK graph, not just this client. Over SSH the client runs without real-time priority, as the bridge-launched RTA already does. A 60-second soak at 44.1 kHz with 1024-frame periods showed no xruns.

### When to stop adjusting a band

This is the loop the TUI is for:

1. Note the report’s starting point, e.g. a cut at 290 Hz.
2. Press **Space** so every band has a zero Δ.
3. Turn **only that** mixer band. Watch that column.
4. **Stop** when:
   - that band’s Δ is about the recommended cut (a little less is safer than a little more);
   - neighbouring bands have not collapsed with it (you are not just turning the whole mix down);
   - speech still sounds like the person, not like a telephone;
   - nothing new is ringing.
5. If the bar **does not move**, you are on the wrong frequency, or you are staring at a room **null**. Do not boost a null to “fill it”. Move the microphone or the loudspeaker.
6. If a **narrow** peak grows when you talk or walk, that is feedback territory. Turn gain **down**. Do not keep cutting nearby graphic bands hoping it will vanish.

The frozen Δ is visual only. It is not an automatic EQ, and it is not proof the PA is “flat”.

## What is not here yet

- No interactive capture wizard TUI for the offline stages (flags only).
- No software pass-through EQ in the speaker path (that is M3: C DSP core first, then PipeWire filter I/O).
- No automatic feedback suppression. Experimental dynamic notches stay off until that work is explicit and bounded.
- No “zero latency” claim. The TUI shows the negotiated quantum; round-trip must be **measured** before anyone quotes a millisecond budget.

## Safety

- Never let the tool raise master gain. It will not; do not do it “because the plot looks low”.
- Prefer cuts. Boosts are starting points, max +3 dB in the analyser.
- Verify with a second capture and with your ears.
- Keep the live analyser off the PA output path until pass-through exists. Today it only listens.
