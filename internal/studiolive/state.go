package studiolive

import (
	"sort"
	"sync"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/osc"
)

// Value is one control as last reported by osclive through the bridge.
type Value struct {
	Value     float64
	Raw       uint8
	UpdatedAt time.Time
}

// On reports a switch state using osclive's threshold.
func (v Value) On() bool { return v.Value >= 0.5 }

// State mirrors mixer controls read from the bridge. It is read-only: nothing
// here can produce a mixer write. Meter and status addresses are ignored.
type State struct {
	mu     sync.RWMutex
	values map[Control]Value
}

func NewState() *State { return &State{values: map[Control]Value{}} }

// Apply records a control update and reports whether a known control
// changed raw value (or was seen for the first time).
func (s *State) Apply(m osc.Message, now time.Time) (Control, Value, bool) {
	c, ok := ParseAddress(m.Address)
	if !ok || KindOf(c) == KindUnknown {
		return Control{}, Value{}, false
	}
	v, ok := m.Float(0)
	if !ok {
		return Control{}, Value{}, false
	}
	nv := Value{Value: v, Raw: RawFromValue(v), UpdatedAt: now}
	if KindOf(c) == KindSwitch {
		nv.Raw = 0
		if nv.On() {
			nv.Raw = 1
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, seen := s.values[c]
	s.values[c] = nv
	return c, nv, !seen || old.Raw != nv.Raw
}

func (s *State) Get(c Control) (Value, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[c]
	return v, ok
}

func (s *State) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.values)
}

// Controls returns known controls in a stable order.
func (s *State) Controls() []Control {
	s.mu.RLock()
	out := make([]Control, 0, len(s.values))
	for c := range s.values {
		out = append(out, c)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Channel != out[j].Channel {
			return out[i].Channel < out[j].Channel
		}
		return out[i].Name < out[j].Name
	})
	return out
}
