package osc

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := Message{Address: "/channel/ch1/eqmidgain", Args: []any{float32(0.25), int32(-3), "hi", []byte{1, 2, 3}, true, false, nil, int64(7), 0.5}}
	b, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(b)%4 != 0 {
		t.Fatalf("packet length %d not 4-aligned", len(b))
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], in) {
		t.Fatalf("got %#v", got)
	}
}

// Golden bytes from python-osc: OscMessageBuilder("/select").add_arg(3).
func TestDecodePythonOSCGolden(t *testing.T) {
	b := []byte("/select\x00,i\x00\x00\x00\x00\x00\x03")
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Address != "/select" || got[0].Args[0] != int32(3) {
		t.Fatalf("got %#v", got)
	}
	enc, _ := Encode(Message{Address: "/select", Args: []any{3}})
	if string(enc) != string(b) {
		t.Fatalf("encode %q, want %q", enc, b)
	}
}

func bundle(elems ...[]byte) []byte {
	b := append([]byte("#bundle\x00"), 0, 0, 0, 0, 0, 0, 0, 1)
	for _, e := range elems {
		b = binary.BigEndian.AppendUint32(b, uint32(len(e))) //nolint:gosec // test packets are tiny
		b = append(b, e...)
	}
	return b
}

func TestDecodeNestedBundle(t *testing.T) {
	a, _ := Encode(Message{Address: "/channel/ch1/level", Args: []any{int32(4)}})
	c, _ := Encode(Message{Address: "/channel/ch2/level", Args: []any{int32(9)}})
	got, err := Decode(bundle(a, bundle(c)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Address != "/channel/ch1/level" || got[1].Args[0] != int32(9) {
		t.Fatalf("got %#v", got)
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	for _, p := range [][]byte{
		nil,
		[]byte("nope\x00\x00\x00\x00"),
		[]byte("/a\x00\x00,f\x00\x00\x00\x00"),
		bundle([]byte("/a\x00")),
		append([]byte("#bundle\x00"), 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 1, 0),
	} {
		if _, err := Decode(p); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%q: got %v", p, err)
		}
	}
}

func TestFloatAccessor(t *testing.T) {
	m := Message{Address: "/x", Args: []any{int32(1), float32(0.5), "s"}}
	if v, ok := m.Float(0); !ok || v != 1 {
		t.Fatal("int32")
	}
	if v, ok := m.Float(1); !ok || v != 0.5 {
		t.Fatal("float32")
	}
	if _, ok := m.Float(2); ok {
		t.Fatal("string accepted as float")
	}
	if _, ok := m.Float(3); ok {
		t.Fatal("out of range accepted")
	}
}
