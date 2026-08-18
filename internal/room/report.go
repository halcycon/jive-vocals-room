package room

import (
	"fmt"
	"strings"
)

func Markdown(s Session, comparison *Comparison) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Jive Room report: %s\n\n", s.VenueLabel)
	fmt.Fprintf(&b, "Generated %s. Measurements are practical starting points, not a complete acoustic model.\n\n", s.CreatedAt.Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(&b, "## Empty room\n\nBroadband RMS: %.1f dBFS; spectral flatness: %.3f.\n\n", s.EmptyRoom.BroadbandRMSDB, s.EmptyRoom.SpectralFlatness)
	if s.EmptyRoom.Hum != nil {
		fmt.Fprintf(&b, "Likely %.0f Hz hum series detected. Investigate grounding, HVAC, fans, dimmers, and cabling before applying EQ.\n\n", s.EmptyRoom.Hum.FundamentalHz)
	}
	if comparison != nil {
		fmt.Fprintf(&b, "## Occupied-room change\n\nBroadband noise changed by %+.1f dB. Audience noise is a masking problem and cannot be removed by static EQ.\n\n", comparison.BroadbandDeltaDB)
	}
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
	return b.String()
}
