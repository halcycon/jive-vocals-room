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

`-live` is a **read-only side tap** of a PipeWire input. It does not sit in the speaker path, does not EQ, and does not change gain. Use it while you turn knobs on the **hardware mixer** (or, later, on the software EQ once pass-through exists).

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
