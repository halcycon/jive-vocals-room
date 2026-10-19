//go:build jack

package live

import (
	"github.com/linuxmatters/jive-vocals/internal/live/jack"
)

// jackSource activates the client and wires resolved ports on Start, so no
// port is connected until analysis begins. Playback never starts on Start.
type jackSource struct {
	*jack.Capture
	sources []string
	sink    string
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
	if s.sink != "" {
		return s.ConnectOutput(s.sink)
	}
	return nil
}

type jackAnalyser struct {
	*ringAnalyser
	src *jackSource
}

func (a *jackAnalyser) Play() error          { return a.src.Play() }
func (a *jackAnalyser) StopPlayback()        { a.src.StopPlayback() }
func (a *jackAnalyser) PlaybackFrames() int  { return a.src.PlaybackPosition() }
func (a *jackAnalyser) PlaybackSink() string { return a.src.sink }
func (a *jackAnalyser) PlaybackState() PlaybackState {
	switch a.src.PlaybackState() {
	case jack.PlaybackPlaying:
		return PlaybackPlaying
	case jack.PlaybackStopping:
		return PlaybackStopping
	case jack.PlaybackDone:
		return PlaybackDone
	default:
		return PlaybackIdle
	}
}

func openJACK(opts InputOptions) (InputAnalyser, error) {
	cap, err := jack.NewCapture(opts.JACKClientName, opts.ringFrames(), len(opts.JACKSources))
	if err != nil {
		return nil, err
	}
	fail := func(err error) (InputAnalyser, error) {
		_ = cap.Close()
		return nil, err
	}
	sources, err := ResolveJACKSources(cap.PhysicalSources(), opts.JACKSources)
	if err != nil {
		return fail(err)
	}
	src := &jackSource{Capture: cap, sources: sources}
	ra := newRingAnalyser(src, "jack").withTap(opts.SampleTap)
	if opts.Playback == nil {
		return ra, nil
	}
	sinks, err := ResolveJACKSources(cap.PhysicalSinks(), []string{opts.Playback.Sink})
	if err != nil {
		return fail(err)
	}
	if err := cap.EnablePlayback(); err != nil {
		return fail(err)
	}
	if err := cap.LoadPlayback(opts.Playback.Samples); err != nil {
		return fail(err)
	}
	src.sink = sinks[0]
	return &jackAnalyser{ringAnalyser: ra, src: src}, nil
}

// JACKSampleRate briefly opens a client to read the server's sample rate.
func JACKSampleRate(clientName string) (int, error) {
	cap, err := jack.NewCapture(clientName, 1024, 1)
	if err != nil {
		return 0, err
	}
	defer cap.Close()
	return cap.Rate(), nil
}

func JACKBuilt() bool { return true }
