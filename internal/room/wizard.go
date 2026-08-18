package room

import "fmt"

const (
	StageEmpty     = "empty_room"
	StageOccupied  = "occupied_room"
	StagePA        = "pa_room_response"
	StagePresenter = "presenter_test"
	StageAppliedEQ = "operator_eq"
	StageVerify    = "verification"

	CaptureDurationMinSeconds = 10.0
	CaptureDurationMaxSeconds = 20.0
)

func CaptureDurationNote(stage string, seconds float64) string {
	if seconds < CaptureDurationMinSeconds {
		return fmt.Sprintf("%s capture is %.1f s; %.0f–%.0f s is recommended for stable averaging.", stage, seconds, CaptureDurationMinSeconds, CaptureDurationMaxSeconds)
	}
	if seconds > CaptureDurationMaxSeconds {
		return fmt.Sprintf("%s capture is %.1f s; %.0f–%.0f s is recommended. Longer files are analysed, but extra length is usually incidental.", stage, seconds, CaptureDurationMinSeconds, CaptureDurationMaxSeconds)
	}
	return ""
}

func (s *Session) NoteCapture(stage string, seconds float64) {
	s.CompletedStages = append(s.CompletedStages, stage)
	if n := CaptureDurationNote(stage, seconds); n != "" {
		s.CaptureNotes = append(s.CaptureNotes, n)
	}
}
