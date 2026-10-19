//go:build jack

package live

import (
	"github.com/linuxmatters/jive-vocals/internal/live/jack"
)

// jackSource activates the client and wires resolved sources on Start, so no
// port is connected until analysis begins.
type jackSource struct {
	*jack.Capture
	sources []string
}

func (s *jackSource) Start() error {
	if err := s.Activate(); err != nil {
		return err
	}
	for i, src := range s.sources {
		if err := s.Connect(i, src); err != nil {
			return err
		}
	}
	return nil
}

func openJACK(opts InputOptions) (InputAnalyser, error) {
	cap, err := jack.NewCapture(opts.JACKClientName, DefaultRingFrames, len(opts.JACKSources))
	if err != nil {
		return nil, err
	}
	sources, err := ResolveJACKSources(cap.PhysicalSources(), opts.JACKSources)
	if err != nil {
		_ = cap.Close()
		return nil, err
	}
	return newRingAnalyser(&jackSource{Capture: cap, sources: sources}, "jack"), nil
}

func JACKBuilt() bool { return true }
