package room

import (
	"fmt"
	"strings"
)

func Markdown(s Session, comparison *Comparison) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Jive Room report: %s\n\n", s.VenueLabel)
	fmt.Fprintf(&b, "Generated %s. Measurements are practical starting points, not a complete acoustic model.\n\n", s.CreatedAt.Format("2006-01-02 15:04 MST"))
	if len(s.CompletedStages) > 0 {
		fmt.Fprintf(&b, "Completed stages: %s.\n\n", strings.Join(s.CompletedStages, ", "))
	}
	for _, n := range s.CaptureNotes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	if len(s.CaptureNotes) > 0 {
		fmt.Fprint(&b, "\n")
	}
	fmt.Fprintf(&b, "## Empty room\n\nBroadband RMS: %.1f dBFS; spectral flatness: %.3f. Levels are relative FFT/dBFS values, not microphone-calibrated SPL.\n\n", s.EmptyRoom.BroadbandRMSDB, s.EmptyRoom.SpectralFlatness)
	if s.Microphone.Calibration != nil {
		fmt.Fprintf(&b, "A relative microphone calibration curve from %s was subtracted from measured spectra. This is not an SPL calibration.\n\n", s.Microphone.Calibration.Source)
	}
	if s.EmptyRoom.Hum != nil {
		fmt.Fprintf(&b, "Likely %.0f Hz hum series detected. Investigate grounding, HVAC, fans, dimmers, and cabling before applying EQ.\n\n", s.EmptyRoom.Hum.FundamentalHz)
	}
	if comparison != nil {
		fmt.Fprintf(&b, "## Occupied-room change\n\nBroadband noise changed by %+.1f dB. Audience noise is a masking problem and cannot be removed by static EQ.\n\n", comparison.BroadbandDeltaDB)
	}
	if s.Presenter != nil {
		fmt.Fprintf(&b, "## Presenter margin (estimate)\n\nOverall margin: %+.1f dB; 1.5–4 kHz presence margin: %+.1f dB; low/low-mid excess: %+.1f dB. This compares separate captures on one FFT/dBFS axis, not simultaneous speech-to-noise separation.\n\n", s.Presenter.OverallMarginDB, s.Presenter.PresenceMarginDB, s.Presenter.LowMidExcessDB)
		if s.Presenter.GainAdvice != "" {
			fmt.Fprintf(&b, "Master-gain advice: %s. jive-room never raises PA gain automatically.\n\n", s.Presenter.GainAdvice)
		}
	}
	writePASection(&b, s.PARoom)
	fmt.Fprint(&b, "## Suggested starting points\n\n")
	if len(s.SuggestedEQ) == 0 {
		fmt.Fprint(&b, "No defensible automatic EQ change was found. Verify gain, microphone placement, and intelligibility by listening.\n")
	}
	for _, r := range s.SuggestedEQ {
		if r.FrequencyHz > 0 {
			fmt.Fprintf(&b, "- **%s at %.0f Hz (%+.1f dB):** %s\n", r.Kind, r.FrequencyHz, r.GainDB, r.Reason)
		} else {
			fmt.Fprintf(&b, "- **%s:** %s\n", r.Kind, r.Reason)
		}
	}
	if len(s.AppliedEQ) > 0 {
		fmt.Fprint(&b, "\n## Operator-applied EQ\n\n")
		for _, eq := range s.AppliedEQ {
			fmt.Fprintf(&b, "- %.0f Hz, %+.1f dB, Q %.2f\n", eq.FrequencyHz, eq.GainDB, eq.Q)
		}
	}
	if len(s.VerificationNotes) > 0 {
		fmt.Fprint(&b, "\n## Verification\n\n")
		for _, n := range s.VerificationNotes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	return b.String()
}

func writePASection(b *strings.Builder, pa *PAResponse) {
	if pa == nil {
		return
	}
	fmt.Fprint(b, "## PA / room response\n\n")
	if pa.Status != paStatusMeasured {
		fmt.Fprintf(b, "Method: %s. Status: %s. No PA/room transfer was measured in this session.\n\n", pa.Method, pa.Status)
		return
	}
	fmt.Fprintf(b, "Method: averaged band-limited pink noise. Status: measured.\n\n")
	fmt.Fprintf(b, "Transfer magnitude is the measured-to-reference power ratio, converted to dB, then shifted so the 500–2000 Hz median is 0 dB (shift applied: %+.1f dB). This is not an SPL calibration and does not recommend raising master gain.\n\n", pa.MidbandOffsetDB)
	if pa.TestSignal != nil {
		fmt.Fprintf(b, "Test signal: %s, %.1f s at %d Hz, headroom %.1f dB, seed %d, band-limited %.0f–%.0f Hz.\n\n", pa.TestSignal.Kind, pa.TestSignal.DurationSeconds, pa.TestSignal.SampleRateHz, pa.TestSignal.HeadroomDB, pa.TestSignal.Seed, pa.TestSignal.HighPassHz, pa.TestSignal.LowPassHz)
	}
	if len(pa.Features) == 0 {
		fmt.Fprint(b, "No broad excess, narrow resonance, or do-not-boost null was classified in the analysis band.\n\n")
		return
	}
	fmt.Fprint(b, "Classified features:\n\n")
	for _, f := range pa.Features {
		boost := ""
		if f.DoNotBoost {
			boost = " do_not_boost."
		}
		fmt.Fprintf(b, "- **%s at %.0f Hz (%+.1f dB):** %s%s\n", f.Kind, f.FrequencyHz, f.MagnitudeDB, f.Reason, boost)
	}
	fmt.Fprint(b, "\n")
}
