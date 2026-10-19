package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/feedback"
	"github.com/linuxmatters/jive-vocals/internal/osc"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

func TestPrintDiff(t *testing.T) {
	st := studiolive.NewState()
	st.Apply(osc.Message{Address: "/channel/ch3/eqmidgain", Args: []any{float32(128.0 / 255)}}, time.Unix(0, 0))
	var b bytes.Buffer
	printDiff(&b, []studiolive.WireChange{{Address: "/channel/ch3/eqmidgain", Raw: 102}}, st)
	if out := b.String(); !strings.Contains(out, "raw 128 -> 102") || !strings.Contains(out, "-3.0 dB") {
		t.Fatalf("diff line %q", out)
	}
}

func TestUndoDryRunNeedsNoBridge(t *testing.T) {
	if err := run([]string{"-studiolive-undo", "-studiolive-bridge", "127.0.0.1:1"}); err != nil {
		t.Fatalf("dry run should not contact the bridge: %v", err)
	}
}

func TestRingOutSubmitterRefusesUnverifiedCalibration(t *testing.T) {
	s := &ringOutSubmitter{mode: ringOutSubmitMode{enabled: true}, bridge: &studiolive.BridgeClient{}}
	cs := []feedback.Candidate{{FrequencyHz: 1250, Confidence: 0.9}}
	s.consider(cs, 3, nil)
	if s.last != "" || !strings.Contains(s.lastRefuse, "not been verified") {
		t.Fatalf("submitted with unverified calibration: last=%q refuse=%q", s.last, s.lastRefuse)
	}
}
