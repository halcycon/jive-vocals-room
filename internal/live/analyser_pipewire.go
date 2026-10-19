//go:build pipewire

package live

import (
	"github.com/linuxmatters/jive-vocals/internal/live/pipewire"
)

func openPipeWire() (InputAnalyser, error) {
	cap, err := pipewire.NewCapture(DefaultRingFrames, 1)
	if err != nil {
		return nil, err
	}
	return newRingAnalyser(cap, "pipewire"), nil
}

func PipeWireBuilt() bool { return true }
