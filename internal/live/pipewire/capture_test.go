//go:build pipewire

package pipewire

import "testing"

func TestCaptureLinksWithoutStartingSession(t *testing.T) {
	c, err := NewCapture(4096, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
