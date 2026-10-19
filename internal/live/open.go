package live

import (
	"errors"
	"fmt"
	"strings"
)

type Backend string

const (
	BackendPipeWire Backend = "pipewire"
	BackendJACK     Backend = "jack"

	DefaultJACKClientName = "jive-room"
)

var ErrJACKDisabled = errors.New("jive-room was built without JACK capture; rebuild with just build-room-jack")

// InputOptions selects a read-only capture backend. JACKSources are physical
// capture port names, or unique suffixes such as "_Ch 3_in"; each becomes one
// interleaved analysis channel.
type InputOptions struct {
	Backend        Backend
	JACKSources    []string
	JACKClientName string
	// SampleTap, when set, receives each batch of newly captured frames as
	// mono samples on the non-real-time consumer goroutine.
	SampleTap func(mono []float64, rateHz int)
	// RingFrames overrides DefaultRingFrames (power of two). Captures use a
	// larger ring so a slow consumer tick cannot drop samples.
	RingFrames int
	// Playback, JACK only, registers one output port wired to Sink. Nothing is
	// played until the analyser's Player.Play is called.
	Playback *PlaybackRequest
}

// PlaybackRequest is an operator-started test signal for one physical sink.
type PlaybackRequest struct {
	Sink    string
	Samples []float32
}

type PlaybackState int

const (
	PlaybackIdle PlaybackState = iota
	PlaybackPlaying
	PlaybackStopping
	PlaybackDone
)

// Player is implemented by analysers opened with a PlaybackRequest.
type Player interface {
	Play() error
	// StopPlayback fades out quickly, then outputs silence.
	StopPlayback()
	PlaybackState() PlaybackState
	PlaybackFrames() int
	PlaybackSink() string
}

func (o InputOptions) ringFrames() int {
	if o.RingFrames > 0 {
		return o.RingFrames
	}
	return DefaultRingFrames
}

func OpenInputAnalyser(opts InputOptions) (InputAnalyser, error) {
	switch opts.Backend {
	case "", BackendPipeWire:
		if opts.Playback != nil {
			return nil, fmt.Errorf("test-signal playback is only available on the jack backend")
		}
		return openPipeWire(opts)
	case BackendJACK:
		if len(opts.JACKSources) == 0 {
			return nil, fmt.Errorf("jack backend needs at least one source port")
		}
		if opts.JACKClientName == "" {
			opts.JACKClientName = DefaultJACKClientName
		}
		if p := opts.Playback; p != nil && (p.Sink == "" || len(p.Samples) == 0) {
			return nil, fmt.Errorf("playback needs a sink port and samples")
		}
		return openJACK(opts)
	default:
		return nil, fmt.Errorf("unknown live backend %q (want pipewire or jack)", opts.Backend)
	}
}

// ResolveJACKSources maps each wanted entry to exactly one available port,
// by exact name first and then by unique suffix.
func ResolveJACKSources(available, wanted []string) ([]string, error) {
	out := make([]string, 0, len(wanted))
	for _, w := range wanted {
		if w == "" {
			return nil, fmt.Errorf("empty jack source")
		}
		var match []string
		for _, a := range available {
			if a == w {
				match = []string{a}
				break
			}
			if strings.HasSuffix(a, w) {
				match = append(match, a)
			}
		}
		switch len(match) {
		case 0:
			return nil, fmt.Errorf("jack port %q not found among %d physical ports", w, len(available))
		case 1:
			out = append(out, match[0])
		default:
			return nil, fmt.Errorf("jack port %q is ambiguous: %s", w, strings.Join(match, ", "))
		}
	}
	return out, nil
}
