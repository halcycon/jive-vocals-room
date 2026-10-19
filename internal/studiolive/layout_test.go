package studiolive

import (
	"reflect"
	"testing"
)

func TestCaptureStreams(t *testing.T) {
	cases := map[int][]int{1: {1}, 8: {8}, 9: {9, 10}, 10: {11, 12}, 11: {13, 14}, 12: {15, 16}}
	for ch, want := range cases {
		got, err := CaptureStreams(ch)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("channel %d: got %v %v, want %v", ch, got, err, want)
		}
	}
	for _, ch := range []int{0, 13, -1} {
		if _, err := CaptureStreams(ch); err == nil {
			t.Fatalf("channel %d accepted", ch)
		}
	}
}

func TestCapturePortSuffixes(t *testing.T) {
	got, err := CapturePortSuffixes(10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"_Ch 11_in", "_Ch 12_in"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
