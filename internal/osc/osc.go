// Package osc is a minimal OSC 1.0 message and bundle codec for the
// StudioLive bridge. It supports the argument types osclive and
// python-osc emit: i, f, s, b, h, d, T, F, and N.
package osc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

type Message struct {
	Address string
	Args    []any
}

var ErrMalformed = errors.New("malformed OSC packet")

const bundleTag = "#bundle"

func pad4(n int) int { return (n + 3) &^ 3 }

func appendString(b []byte, s string) []byte {
	b = append(b, s...)
	b = append(b, 0)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// Encode serialises a message. Supported argument types are int32, int,
// int64, float32, float64, string, []byte, bool, and nil.
func Encode(m Message) ([]byte, error) {
	if len(m.Address) == 0 || m.Address[0] != '/' {
		return nil, fmt.Errorf("osc address %q must start with /", m.Address)
	}
	tags := []byte{','}
	var data []byte
	for _, a := range m.Args {
		switch v := a.(type) {
		case int32:
			tags = append(tags, 'i')
			data = binary.BigEndian.AppendUint32(data, uint32(v)) //nolint:gosec // OSC int32 bit pattern
		case int:
			if v < math.MinInt32 || v > math.MaxInt32 {
				return nil, fmt.Errorf("osc int %d overflows int32", v)
			}
			tags = append(tags, 'i')
			data = binary.BigEndian.AppendUint32(data, uint32(int32(v))) //nolint:gosec // range checked above
		case int64:
			tags = append(tags, 'h')
			data = binary.BigEndian.AppendUint64(data, uint64(v)) //nolint:gosec // OSC int64 bit pattern
		case float32:
			tags = append(tags, 'f')
			data = binary.BigEndian.AppendUint32(data, math.Float32bits(v))
		case float64:
			tags = append(tags, 'd')
			data = binary.BigEndian.AppendUint64(data, math.Float64bits(v))
		case string:
			tags = append(tags, 's')
			data = appendString(data, v)
		case []byte:
			tags = append(tags, 'b')
			data = binary.BigEndian.AppendUint32(data, uint32(len(v))) //nolint:gosec // UDP payloads are far below 4 GiB
			data = append(data, v...)
			for len(data)%4 != 0 {
				data = append(data, 0)
			}
		case bool:
			if v {
				tags = append(tags, 'T')
			} else {
				tags = append(tags, 'F')
			}
		case nil:
			tags = append(tags, 'N')
		default:
			return nil, fmt.Errorf("unsupported osc argument type %T", a)
		}
	}
	out := appendString(nil, m.Address)
	out = appendString(out, string(tags))
	return append(out, data...), nil
}

// Decode parses a message or a (possibly nested) bundle into messages in
// packet order. Bundle time tags are ignored.
func Decode(p []byte) ([]Message, error) {
	var out []Message
	if err := decodeInto(p, &out, 0); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeInto(p []byte, out *[]Message, depth int) error {
	if depth > 8 {
		return ErrMalformed
	}
	if bytes.HasPrefix(p, []byte(bundleTag+"\x00")) {
		if len(p) < 16 {
			return ErrMalformed
		}
		rest := p[16:]
		for len(rest) > 0 {
			if len(rest) < 4 {
				return ErrMalformed
			}
			n := int(binary.BigEndian.Uint32(rest))
			rest = rest[4:]
			if n < 0 || n > len(rest) {
				return ErrMalformed
			}
			if err := decodeInto(rest[:n], out, depth+1); err != nil {
				return err
			}
			rest = rest[n:]
		}
		return nil
	}
	m, err := decodeMessage(p)
	if err != nil {
		return err
	}
	*out = append(*out, m)
	return nil
}

func readString(p []byte) (string, []byte, error) {
	i := bytes.IndexByte(p, 0)
	if i < 0 {
		return "", nil, ErrMalformed
	}
	n := pad4(i + 1)
	if n > len(p) {
		return "", nil, ErrMalformed
	}
	return string(p[:i]), p[n:], nil
}

func decodeMessage(p []byte) (Message, error) {
	addr, rest, err := readString(p)
	if err != nil || len(addr) == 0 || addr[0] != '/' {
		return Message{}, ErrMalformed
	}
	m := Message{Address: addr}
	if len(rest) == 0 {
		return m, nil
	}
	tags, rest, err := readString(rest)
	if err != nil || len(tags) == 0 || tags[0] != ',' {
		return Message{}, ErrMalformed
	}
	for _, t := range tags[1:] {
		switch t {
		case 'i', 'f':
			if len(rest) < 4 {
				return Message{}, ErrMalformed
			}
			u := binary.BigEndian.Uint32(rest)
			rest = rest[4:]
			if t == 'i' {
				m.Args = append(m.Args, int32(u)) //nolint:gosec // OSC int32 bit pattern
			} else {
				m.Args = append(m.Args, math.Float32frombits(u))
			}
		case 'h', 'd':
			if len(rest) < 8 {
				return Message{}, ErrMalformed
			}
			u := binary.BigEndian.Uint64(rest)
			rest = rest[8:]
			if t == 'h' {
				m.Args = append(m.Args, int64(u)) //nolint:gosec // OSC int64 bit pattern
			} else {
				m.Args = append(m.Args, math.Float64frombits(u))
			}
		case 's':
			var s string
			s, rest, err = readString(rest)
			if err != nil {
				return Message{}, err
			}
			m.Args = append(m.Args, s)
		case 'b':
			if len(rest) < 4 {
				return Message{}, ErrMalformed
			}
			n := int(binary.BigEndian.Uint32(rest))
			rest = rest[4:]
			if n < 0 || pad4(n) > len(rest) {
				return Message{}, ErrMalformed
			}
			m.Args = append(m.Args, append([]byte(nil), rest[:n]...))
			rest = rest[pad4(n):]
		case 'T':
			m.Args = append(m.Args, true)
		case 'F':
			m.Args = append(m.Args, false)
		case 'N':
			m.Args = append(m.Args, nil)
		default:
			return Message{}, fmt.Errorf("%w: unsupported type tag %q", ErrMalformed, t)
		}
	}
	return m, nil
}

// Float returns a numeric argument as float64.
func (m Message) Float(i int) (float64, bool) {
	if i < 0 || i >= len(m.Args) {
		return 0, false
	}
	switch v := m.Args[i].(type) {
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}
