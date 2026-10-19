package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/osc"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

func TestPrintMixerState(t *testing.T) {
	s := studiolive.NewState()
	now := time.Unix(0, 0)
	for addr, v := range map[string]any{
		"/channel/ch3/hpf":       int32(1),
		"/channel/ch3/hpffreq":   float32(0.5),
		"/channel/ch3/eqmid":     int32(1),
		"/channel/ch3/eqmidfreq": float32(0.5),
		"/channel/ch3/eqmidgain": float32(102.0 / 255),
		"/channel/ch3/eqmidhiq":  int32(1),
		"/channel/ch3/post":      int32(0),
		"/channel/geq0/enable":   int32(1),
		"/channel/geq0/18":       float32(120.0 / 255),
	} {
		s.Apply(osc.Message{Address: addr, Args: []any{v}}, now)
	}
	var b bytes.Buffer
	printMixerState(&b, s, 3)
	out := b.String()
	for _, want := range []string{"CH3  (FireWire send pre-DSP)", "HPF   on   155 Hz", "Mid   on   954 Hz", "-3.0 dB (raw 102)", "hi-Q", "GEQ  on", "1.00 kHz    -7", "not yet verified"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMixerStateRejectsBadChannel(t *testing.T) {
	if err := run([]string{"-studiolive-state", "-studiolive-channel", "13"}); !isUsage(err) {
		t.Fatalf("got %v", err)
	}
}
