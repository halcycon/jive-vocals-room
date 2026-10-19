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
}

func OpenInputAnalyser(opts InputOptions) (InputAnalyser, error) {
	switch opts.Backend {
	case "", BackendPipeWire:
		return openPipeWire()
	case BackendJACK:
		if len(opts.JACKSources) == 0 {
			return nil, fmt.Errorf("jack backend needs at least one source port")
		}
		if opts.JACKClientName == "" {
			opts.JACKClientName = DefaultJACKClientName
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
			return nil, fmt.Errorf("jack source %q not found among %d physical capture ports", w, len(available))
		case 1:
			out = append(out, match[0])
		default:
			return nil, fmt.Errorf("jack source %q is ambiguous: %s", w, strings.Join(match, ", "))
		}
	}
	return out, nil
}
