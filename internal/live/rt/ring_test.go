package rt

import "testing"

func TestRingRoundTrip(t *testing.T) {
	r, err := NewRing(8, 2)
	if err != nil {
		t.Fatal(err)
	}
	in := []float32{1, 2, 3, 4, 5, 6}
	if n := r.Write(in); n != 3 {
		t.Fatalf("write frames=%d", n)
	}
	out := make([]float32, 8)
	n := r.Read(out)
	if n != 3 {
		t.Fatalf("read frames=%d", n)
	}
	for i, v := range in {
		if out[i] != v {
			t.Fatalf("sample %d: got %v want %v", i, out[i], v)
		}
	}
}

func TestRingCountsOverrunsWithoutBlocking(t *testing.T) {
	r, err := NewRing(4, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n := r.Write([]float32{1, 2, 3, 4}); n != 4 {
		t.Fatalf("fill write=%d", n)
	}
	if n := r.Write([]float32{9, 8}); n != 0 {
		t.Fatalf("expected drop, wrote %d", n)
	}
	if r.Overruns() != 2 {
		t.Fatalf("overruns=%d", r.Overruns())
	}
	out := make([]float32, 4)
	if n := r.Read(out); n != 4 || out[0] != 1 || out[3] != 4 {
		t.Fatalf("overwritten consumer data: n=%d out=%v", n, out[:4])
	}
}

func TestRingRejectsBadCapacity(t *testing.T) {
	if _, err := NewRing(6, 1); err == nil {
		t.Fatal("expected non-power-of-two to fail")
	}
}
