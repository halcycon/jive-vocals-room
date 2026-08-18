# Codex Task: Jive Vocals fork — guided room calibration + low-latency live DSP

## Objective

Work from our fork of `linuxmatters/jive-vocals` and build the foundations for a new capability with two related goals:

1. **Primary:** a guided room / PA setup assistant that measures empty-room noise, occupied-room noise, PA/room response, and presenter speech, then gives an inexperienced operator practical, explainable EQ guidance.
2. **Secondary / experimental:** a true live audio application that accepts audio from an interface, applies EQ / DSP in real time, and outputs it with very low latency. This should eventually support reactive acoustic-feedback detection/suppression and could grow into a general-purpose software speech/PA DSP.

Do not try to turn the existing file-oriented FFmpeg processing pipeline directly into a real-time callback. Reuse its analysis concepts, metrics, UX philosophy, and suitable pure logic, but keep real-time audio transport/DSP architecturally separate.

## Repository context you must inspect first

Read the repository's `AGENTS.md` before making changes and follow it.

Pay particular attention to:

- `internal/processor/analyser_vad.go`
  - 250 ms analysis intervals
  - VAD / Otsu split
  - speech/noise separation
  - elected `SpeechProfile` and `NoiseProfile`
  - gate statistics
- `internal/processor/analyser_noise_bands.go`
  - current 15-band room-tone measurements
  - band centres:
    `80, 125, 195, 290, 440, 660, 1000, 1500, 2250, 3350, 5000, 7500, 11200, 16000, 24000 Hz`
  - geometric band edges
- `internal/processor/adaptive.go`
  - `buildAfftdnBandNoise`
  - custom room-noise profile eligibility
  - current 12 dB speech/noise separation threshold
  - current spectral-flatness gate
- `internal/processor/analyser_band_runner.go`
- `internal/processor/analyser_metrics.go`
- `internal/processor/filters.go`
- `internal/processor/frame_processor.go`
- `docs/Pipeline.md`
- existing tests around the above code.

Current Jive Vocals is a Go application using CGO and embedded FFmpeg via `ffmpeg-statigo`, with a Charm/Bubble Tea TUI. Preserve existing Jive Vocals behaviour and tests.

## Important conceptual distinction

The new application must not claim that room noise can simply be "EQed out".

There are several separate measurements/problems:

- **ambient room noise**: HVAC, hum, fans, audience chatter, etc.
- **room + loudspeaker transfer response**: what the PA and room do to a known signal
- **speech-to-noise / masking**: whether useful speech bands sit far enough above room/audience noise
- **acoustic feedback**: unstable narrowband closed-loop coupling between microphone and loudspeaker

Treat them separately and explain recommendations.

For Jive Vocals, strongly tonal room tone is currently a reason not to use the measured custom `afftdn` profile. For this new application, tonal room noise is valuable diagnostic information. Detect and report it rather than rejecting it. Examples include likely 50 Hz mains hum/harmonics, fan tones, or narrow resonances.

---

# PART A — Primary feature: guided room / PA setup assistant

## Desired user workflow

Design around a wizard-like sequence.

### Stage 1: Empty room

Ask the operator for roughly 10–20 seconds of quiet room capture.

Measure and save:

- broadband level / noise floor
- fine spectral profile
- Jive-compatible 15-band profile
- tonal peaks and persistence
- spectral flatness / tonality
- likely mains hum and harmonics where detectable
- measurement device/sample-rate/channel metadata

### Stage 2: Occupied room

Immediately before the event, capture roughly 10–20 seconds with the audience present but no amplified programme speech.

Compute and show:

- occupied-room profile
- delta from empty-room profile
- frequency-dependent increase in masking/noise
- useful warnings, without pretending static EQ can remove audience chatter

### Stage 3: PA / room response

Play a known test signal through the actual PA and record it with a measurement microphone/input.

Evaluate the best practical initial test method. Candidates include:

- pink / speech-shaped noise with averaging
- logarithmic sine sweep with response extraction

Document the trade-off in an ADR and implement the simplest defensible initial method.

Derive:

- measured PA + room response
- smoothed response suitable for practical EQ advice
- broad excesses / resonances
- narrow peaks
- obvious nulls that should **not** be "fixed" with large boosts

Do not blindly invert the transfer function.

Prefer conservative correction:

- cuts before boosts
- broad corrections before narrow corrections
- tightly limit boosts
- do not chase deep room nulls / comb-filter notches
- constrain total correction
- make every recommendation explainable

### Stage 4: Presenter test

Ask the presenter to speak normally for around 10 seconds.

Reuse/adapt Jive's speech/noise analysis concepts to estimate:

- overall speech/noise margin
- band-wise speech/noise margin
- presence/intelligibility-region margin
- excessive low/low-mid energy
- whether more master gain is likely to help or merely move the system closer to feedback

The output should be practical, for example:

- enable HPF around 80–100 Hz
- reduce 250 Hz by ~3 dB
- reduce 500 Hz by ~2 dB
- leave 2–4 kHz alone because speech margin is already good
- investigate likely hum rather than trying to EQ it away

Recommendations must be framed as starting points, not physical truth.

## Mixer model

Create a generic mixer-EQ capability model so recommendations can be translated into the controls the user actually has.

At minimum support representations for:

- simple 3-band fixed EQ
- 4-band / semi-parametric channel EQ
- N-band graphic EQ
- fully parametric EQ

The recommendation engine should solve within the available controls instead of outputting frequencies the mixer cannot set.

Do **not** add vendor-specific mixer control APIs yet. Keep the model extensible for those later.

## Profiles and persistence

Create versioned, machine-readable profile/session structures.

A venue/session should be able to retain:

- venue / room label
- timestamp
- interface/device
- sample rate
- channel map
- measurement microphone identity/calibration metadata if supplied
- empty-room profile
- occupied-room profile
- PA/room response
- presenter test
- mixer EQ capability/profile
- suggested EQ
- operator-applied EQ
- verification measurement

Use JSON for the canonical persisted data and optionally render an objective Markdown report.

Keep the data model stable and versioned from the start.

## Analysis resolution

Retain the existing 15 Jive/`afftdn` bands as a compatibility/reporting view, but add a finer spectral representation for room work.

The fine analyser should be able to identify things that the 15-band representation cannot, such as:

- 50 Hz / 100 Hz / 150 Hz hum series
- narrow resonances
- feedback candidates

Use logarithmic aggregation (for example fractional-octave display bands) where useful, while retaining finer FFT-bin information internally.

---

# PART B — Secondary feature: low-latency live DSP

This is an experimental secondary function, but design its boundaries now so Part A does not paint us into a corner.

## Goal

Accept live audio from an audio interface and produce processed output suitable for insertion before a mixer/amplifier/PA:

`audio input -> software DSP -> audio output`

Target Linux first.

The design should be capable of:

- high-pass / low-pass filters
- parametric EQ
- graphic-EQ approximation
- fixed notches
- dynamic / feedback notches
- optional additional speech processing later
- bypass
- measurable/reportable latency
- xrun monitoring

Do not call this "zero latency". The DSP itself should add essentially no frame-based algorithmic delay where possible, but total round-trip latency is dominated by the interface, hardware buffers, and PipeWire graph quantum.

Initial engineering target:

- stable operation at 48 kHz
- synchronous low-quantum PipeWire graph
- target <10 ms practical input-to-output latency on suitable hardware
- stretch goal <5 ms where the interface and system support it
- always **measure and report** actual latency/quantum instead of assuming it

## Linux audio backend

Use **native PipeWire first**.

Investigate and use the native PipeWire filter API with an RT process callback.

Do not use an asynchronous PipeWire node in the actual processing path because that adds an additional graph quantum.

JACK compatibility can initially be obtained through PipeWire/JACK where practical. A direct JACK backend may be considered later only if it provides a demonstrated benefit.

Do not make FFmpeg the live transport layer.

## Critical real-time architecture rule

The real-time audio callback must remain real-time safe.

It must not:

- allocate/free memory
- perform file I/O
- log
- block
- take ordinary mutexes
- perform network/IPC operations
- invoke slow Go code
- execute file-oriented FFmpeg graphs

Because the existing project already uses CGO, favour this architecture initially:

### C real-time engine

A small C component owns:

- PipeWire connection/filter
- RT audio callback
- sample-format handling
- fixed-capacity DSP state
- biquad filter chain
- dynamic notch bank
- sample counters / xrun-safe telemetry
- lock-free or atomic parameter handoff

### Go control plane

Go owns:

- TUI / later GUI
- profile/session logic
- measurement wizard
- slow spectral analysis
- recommendation engine
- configuration
- persistence
- user interaction
- non-real-time logging/reporting

**Do not call into the Go runtime from the RT callback.**

If one-process CGO integration proves awkward, keep the option open for a dedicated C `jive-live-dsp` process controlled by the Go UI, but no IPC may be required to complete each audio buffer.

## Live signal path

Aim for:

`PipeWire input`
` -> DC/HPF`
` -> manual / recommended parametric EQ`
` -> fixed notch bank`
` -> dynamic feedback notch bank`
` -> optional safe output stage`
` -> PipeWire output`

The analyser must be a **side tap**, not a frame-buffering insert:

`live samples -> lock-free SPSC analysis ring -> non-RT analyser -> control/recommendation -> smoothed coefficient updates`

This lets the audio path remain sample/block causal while the analyser can use longer FFT windows.

## Filter implementation

Implement manual EQ/notches using stable biquad IIR filters.

Requirements:

- no allocation while processing
- preallocated state
- parameter validation
- coefficient smoothing or safe crossfade/update strategy to prevent zipper/click artefacts
- stable bypass behaviour
- deterministic unit tests for frequency response
- denormal handling if required
- channel-independent state

Start with mono and stereo support but keep channel count extensible.

---

# PART C — Acoustic feedback suppression

Treat this as a staged feature.

## Phase 1: "ring out the room" assistant

This is the safest and most useful first feedback feature.

Workflow:

1. operator enables ring-out mode
2. operator manually raises PA gain
3. software monitors the mic/input
4. when a likely feedback frequency starts to build, detect it
5. insert/suggest a narrow notch
6. operator continues manually
7. save resulting notches as **fixed event/venue notches**

The software must **not automatically raise the PA gain**.

Give the user visibility into:

- candidate frequency
- confidence
- peak prominence
- persistence
- growth
- notch frequency/Q/depth
- whether the notch is fixed or dynamic

## Phase 2: reactive live feedback suppressor

Use a non-RT analyser to detect howling candidates and update a preallocated dynamic notch bank.

An FFT is allowed in the analyser because it is not in the actual audio path.

Candidate detection must use more than "largest FFT bin". Investigate and combine signals such as:

- narrowband prominence relative to a local spectral baseline
- persistence over multiple frames
- rapid growth over time
- high Q / narrow bandwidth
- harmonic relationships
- peak stability / frequency tracking
- speech/tonal-content discrimination

False-positive control is crucial.

A musical note or voiced speech harmonic must not immediately create a notch merely because it is tonal.

Start conservative.

Suggested dynamic-notch policy to evaluate:

- fixed maximum number of dynamic notches (e.g. 8–12)
- narrow Q
- shallow initial attenuation
- deepen only when evidence increases
- hold timer
- slow release
- maximum depth limit
- coefficient smoothing
- promote a repeated/stable event to a fixed notch only with explicit user action or strong, documented criteria

Keep all thresholds configurable and expose diagnostics.

## Phase 3: proactive acoustic feedback cancellation — research only initially

Do **not** make full adaptive feedback cancellation an MVP requirement.

Research a future architecture that has access to both:

- the signal sent to the loudspeaker
- the microphone signal

That opens the possibility of estimating the acoustic feedback path and subtracting predicted feedback using adaptive filtering.

Consider established AFC approaches (NLMS/RLS/PEM-style methods, decorrelation, etc.) only after the notch-based system is stable and well tested.

Document this as future R&D, including the risk that adaptive filters can mistake desired correlated source content for feedback.

---

# Safety / failure behaviour

A live PA processor must fail predictably.

Requirements:

- explicit hard bypass
- bypass on unrecoverable engine error
- last-known-good filter coefficients
- bounds on every automatic gain/EQ/notch change
- no automatic master gain increase
- clear clipping/headroom indication
- no uncontrolled positive gain from automatic EQ
- sensible maximum boost
- sensible maximum notch depth
- watchdog/health telemetry where practical
- xrun counter and CPU load visibility
- save/restore configuration safely
- atomic profile writes

Do not implement "AI decides arbitrary EQ continuously". Adaptation must be bounded, explainable and reversible.

---

# UX principles

Jive Vocals is deliberately opinionated and simple. Preserve that spirit.

The user should not need to be a sound engineer.

Prefer:

> 250 Hz is excessive. Reduce the 250 Hz control by about 3 dB.

over:

> Here is a graph. Good luck.

However, offer an expert diagnostics view containing:

- spectrum
- response curve
- noise profile
- speech/noise margin
- feedback candidates
- actual applied filters
- latency/quantum
- xruns
- reason/confidence for each recommendation

Every automatic/recommended action should answer "why?".

---

# Engineering milestones

Structure the work so each milestone is independently useful.

## M0 — architecture/foundation

Complete this first.

Deliver:

- `docs/Room-Live-DSP-Architecture.md`
- at least one ADR covering:
  - PipeWire + C RT engine / Go control-plane split
  - initial PA test-signal choice
- versioned profile/session data model
- package/interface boundaries
- CLI command/binary naming decision
- test strategy
- explicit latency budget
- explicit RT-safety rules

Do not just write prose: create compilable scaffolding/interfaces for the chosen boundaries.

## M1 — offline/file-based room-assistant vertical slice

Implement enough to prove the measurement/recommendation model without requiring live hardware.

It should accept captured WAV/FLAC samples for at least:

- empty-room sample
- occupied-room sample
- optional presenter sample

Produce:

- fine spectral representation
- legacy 15-band representation
- empty-vs-occupied comparison
- tonal/hum diagnostics
- initial conservative EQ suggestions
- JSON profile
- Markdown report
- unit tests using synthetic signals

If PA-response measurement is too large for this first task, define its interfaces/schema and leave a clear next-step test.

## M2 — live analyser, read-only

Capture from PipeWire live input and display:

- levels
- rolling spectrum
- noise profile
- peak/tonality diagnostics
- no audio output processing yet

## M3 — live pass-through + manual EQ

Implement the C RT engine:

- PipeWire input/output
- manual biquad EQ
- bypass
- measured quantum/latency
- xrun reporting
- offline test harness using the same DSP core
- no automatic adaptation yet

## M4 — feedback ring-out + experimental dynamic notches

Add:

- feedback candidate detector
- ring-out workflow
- fixed notch capture
- experimental dynamic notch bank, disabled by default
- synthetic and recorded regression tests
- false-positive tests

## M5 — full guided integration

Combine calibration, mixer mapping, recommendations, verification, venue profiles, and live DSP controls.

## M6 — AFC research

Only after M1–M5 are stable.

---

# What to implement in THIS Codex task

Start working, not merely researching.

1. Inspect the existing repository and tests.
2. Create a branch named something like `feature/room-live-dsp-foundation`.
3. Implement **M0 completely**.
4. Implement a **thin but working M1 vertical slice**:
   - new room-profile domain types
   - a file-based empty/occupied analyser
   - 15-band compatibility output
   - a finer spectral representation
   - comparison/delta calculation
   - tonal/hum candidate detection
   - conservative recommendation object(s)
   - JSON output
   - useful tests using generated synthetic audio
5. Add the interfaces/scaffolding needed for M2/M3 without pretending the live engine exists.
6. Run the existing test suite plus new tests and linters.
7. Do not break the existing `jive-vocals` command.
8. Do not modify or remove upstream behaviour merely to make the new design easier.
9. Do not open a PR against `linuxmatters/jive-vocals`. This work belongs on our fork.
10. At the end, report:
    - architecture decisions
    - changed files
    - commands/tests run and results
    - what is genuinely working
    - what is scaffold only
    - next recommended task

If the existing architecture makes one of these requirements materially unwise, document the conflict and choose the least invasive design rather than forcing a large refactor.

## Quality bar

- idiomatic Go for non-RT code
- C only where justified by the live RT boundary
- small interfaces
- deterministic tests
- no hidden global mutable state unless required for audio callback state
- comments explain DSP reasoning, not syntax
- bounds and units explicit in names
- persisted formats versioned
- existing tests remain green
- no speculative "magic numbers" without naming/documenting them
- no fake latency claims
- no unbounded automatic gain changes

The end goal is an approachable sound-check assistant first, and a robust, measurable low-latency DSP/feedback platform second.
