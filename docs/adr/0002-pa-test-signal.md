# ADR 0002: Averaged pink noise for the initial PA test

Status: accepted

## Decision

Start PA/room measurement with band-limited pink noise played through the actual PA and averaged over several seconds. Capture a reference or use the known generator spectrum, smooth the measured response, and offer conservative cuts. Do not invert deep nulls or comb notches.

## Rationale and consequences

Pink noise is simple to generate, robust to modest timing uncertainty, and easy for inexperienced operators to repeat. Averaging reduces incidental noise and directly supports broad practical EQ advice. A logarithmic sweep provides higher signal-to-noise ratio and can separate impulse response/distortion, but needs synchronized routing, deconvolution, level safeguards, and more careful handling of time variance. Sweep support remains a compatible future method in the versioned schema.

The implementation must warn before playback, require the operator to set a safe level, and never raise PA gain automatically. This ADR selects the method; playback and response extraction are not claimed by M1.
