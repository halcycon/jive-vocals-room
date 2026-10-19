# Room calibration and live DSP architecture

## Product boundary

`jive-vocals` remains the file-oriented podcast processor. The additive `jive-room` command is the guided room-calibration control plane. It decodes captured WAV/FLAC through the existing FFmpeg adapter, then passes ordinary PCM to `internal/room`, whose analysis, comparison, mixer mapping, persistence, and reporting have no FFmpeg dependency.

The wizard state will progress through empty room, occupied room, PA/room response, presenter test, and verification. Versioned `jive-room-session/v1` JSON is the canonical record. The current CLI analyses empty-room, optional occupied-room, optional presenter, and optional PA/room-response captures. `jive-room -generate-pink` writes a band-limited test-signal WAV; it never starts playback or raises PA gain.

Ambient noise, PA/room transfer response, speech masking, and feedback are separate evidence streams. Recommendations must identify their evidence and remain bounded starting points. In particular, audience chatter cannot be EQed away, tonal noise should be investigated, and deep transfer-response nulls must not invite large boosts.

## Package boundaries

- `internal/room`: pure slow analysis and domain model. It accepts interleaved PCM, produces fine 1/24-octave and legacy 15-band views, persistent tonal peaks, mains-hum diagnostics, comparisons, averaged pink-noise PA/room transfer magnitude, classified excesses/nulls, and conservative mixer-constrained advice.
- `internal/roomfile`: non-real-time FFmpeg decode adapter for WAV/FLAC.
- `internal/live`: control-plane types, rolling ring consumer, and `OpenInputAnalyser`. Default builds return `ErrPipeWireDisabled`.
- `internal/live/rt`: C SPSC analysis ring. Write is RT-safe; read is the non-RT consumer. Offline C harness in `internal/live/rt/harness`.
- `internal/live/pipewire`: native PipeWire input capture (`//go:build pipewire`). The callback copies into the ring and never calls Go.
- `cmd/jive-room`: wizard/automation entry point plus optional `-live` read-only analyser TUI. `cmd/jive-vocals` is unchanged.

The live analyser is a side tap: the C callback copies into a fixed SPSC ring and continues processing. Slow Go analysis consumes the ring independently. Go is never called by the real-time callback. On a TTY, `-live` shows a 15-band spectrum and RMS sparkline with a freeze/Δ view so an engineer can watch a mixer cut settle. Freeze and peak-hold are display-only; they never change audio.

## Live signal and safety

The intended causal path is input, DC/high-pass, manual/recommended parametric EQ, fixed notches, bounded dynamic notches, safe output, output. An explicit hard bypass must work without Go. An unrecoverable engine failure selects bypass and retains the last-known-good coefficients.

The callback may not allocate or free, perform I/O or logging, block, take ordinary mutexes, call Go, run FFTs, or execute FFmpeg graphs. All channel state and notch slots are preallocated. Parameter validation occurs off-thread; complete coefficient banks cross the boundary by atomically swapping indices. Updates use coefficient ramps or parallel-filter crossfades. Telemetry is fixed-size atomic data.

Automatic processing never raises master gain. EQ recommendations default to cuts, boosts are capped at +3 dB, automatic notches are capped at -12 dB, and total automatic positive gain is capped at +3 dB. Actual constants will be validated before M3/M4 rather than silently implied by this scaffold.

## Latency budget

At 48 kHz, the initial target is a 128-frame PipeWire quantum (2.67 ms). One input quantum plus one output quantum is about 5.33 ms; interface converters, scheduling, and graph overhead consume the remaining budget toward the practical target below 10 ms. A 64-frame quantum can approach the stretch target below 5 ms on suitable systems. These are budgets, not claims: the engine must display negotiated quantum, sample rate, xrun count, and measured loopback round-trip latency. The DSP core adds no frame look-ahead; the analyser is not in series.

## Test strategy

Pure analysis tests synthesize deterministic tones/noise and assert frequency, persistence, hum, band deltas, bounded advice, and JSON schema stability. Persistence and recommendation tests must include speech/music-like tonal false positives. The C analysis ring has an offline harness (`just test-rt`) plus a fake Go producer. PipeWire-tagged tests (`just test-pipewire` / `just container test-live`) compile native capture against development headers in the Fedora toolchain image; live hardware tests stay skipped unless `JIVE_LIVE_PW=1`. The future C DSP core requires an offline harness with impulse/frequency-response tests, coefficient-update click tests, bypass equivalence, sanitizer runs, and allocation instrumentation. Existing `just test`, `just lint`, and `just build` remain release gates; PipeWire is not a default-build dependency.

## Milestone status

M0 boundaries and contracts are compilable. Offline M1 covers empty/occupied/presenter/PA-response file analysis. M2 is a read-only live analyser: a C SPSC ring with an offline harness, tagged native PipeWire input capture, and a TTY spectrum TUI (freeze/Δ, peak-hold, RMS sparkline). Pass-through DSP and feedback suppression remain later work.

The first deployment target is a StudioLive 16.0.2 behind a Mac mini running FFADO/JACK and osclive. There, `jive-room` reads JACK input and proposes changes to the desk's own HPF, channel EQ and main graphic EQ through the existing OSC bridge, rather than inserting DSP into the signal path. See [ADR 0003](adr/0003-studiolive-jack-mixer-actuator.md) and [StudioLive-Integration.md](StudioLive-Integration.md).

Operator workflow: [Room-Guide.md](Room-Guide.md).

