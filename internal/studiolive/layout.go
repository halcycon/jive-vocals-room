// Package studiolive models the PreSonus StudioLive 16.0.2 as seen through
// FFADO/JACK and osclive. It is pure Go and performs no I/O.
package studiolive

import "fmt"

const (
	InputChannels      = 12
	MonoInputChannels  = 8
	FireWireCaptureMax = 16
)

// CaptureStreams returns the 1-based FireWire capture streams for control
// channel 1..12. Channels 1–8 are mono; 9–12 are stereo pairs filling streams
// 9–16. This follows osclive's level layout and must be verified on hardware.
func CaptureStreams(channel int) ([]int, error) {
	switch {
	case channel >= 1 && channel <= MonoInputChannels:
		return []int{channel}, nil
	case channel > MonoInputChannels && channel <= InputChannels:
		left := MonoInputChannels + 2*(channel-MonoInputChannels) - 1
		return []int{left, left + 1}, nil
	default:
		return nil, fmt.Errorf("studiolive input channel %d out of range 1..%d", channel, InputChannels)
	}
}

// CapturePortSuffix is the FFADO JACK port suffix for a capture stream.
func CapturePortSuffix(stream int) string {
	return fmt.Sprintf("_Ch %d_in", stream)
}

// CapturePortSuffixes returns JACK port suffixes for control channel 1..12.
func CapturePortSuffixes(channel int) ([]string, error) {
	streams, err := CaptureStreams(channel)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(streams))
	for i, s := range streams {
		out[i] = CapturePortSuffix(s)
	}
	return out, nil
}
