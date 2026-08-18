# ADR 0001: Native PipeWire C engine with a Go control plane

Status: accepted

## Decision

Use a native synchronous PipeWire filter and C real-time callback for live transport and DSP. Go owns UI, persistence, slow analysis, and validated configuration. The callback never enters Go. FFmpeg remains a file decoder/processor only.

The C core will expose fixed-capacity configuration and telemetry through a small CGO wrapper. If process integration proves unreliable, the same core may become a dedicated `jive-live-dsp` process; IPC is control-only and never required to finish a buffer.

## Rationale and consequences

Native PipeWire avoids the extra graph quantum of an asynchronous processing node. C makes callback ownership and allocation discipline inspectable while fitting the existing CGO build. This adds a second implementation language and demands an offline harness, sanitizers, atomic handoff, and explicit ownership documentation. Direct JACK is deferred until measurements demonstrate a benefit.

