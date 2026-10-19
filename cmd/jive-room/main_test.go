package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxmatters/jive-vocals/internal/live"
	"github.com/linuxmatters/jive-vocals/internal/room"
)

func TestCLIGeneratePinkAndAnalysePAResponse(t *testing.T) {
	dir := t.TempDir()
	emptyPath := filepath.Join(dir, "empty.wav")
	refPath := filepath.Join(dir, "pink.wav")
	outPath := filepath.Join(dir, "session.json")

	// Room noise 40 dB below the PA test signal, so the PA capture clears the
	// signal-to-noise gate the way a real measurement must.
	emptySpec := room.DefaultPinkSpec()
	emptySpec.DurationSeconds = 2
	emptySpec.Seed = 11
	emptyPCM, err := room.GeneratePink(emptySpec)
	if err != nil {
		t.Fatal(err)
	}
	for i := range emptyPCM.Samples {
		emptyPCM.Samples[i] *= 0.01
	}
	if err := room.WriteWAV(emptyPath, emptyPCM); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"-generate-pink", refPath,
		"-pink-duration", "2",
		"-pink-seed", "12",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(room.SignalSpecPath(refPath)); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"-empty", emptyPath,
		"-pa-reference", refPath,
		"-pa-measured", refPath,
		"-venue", "CLI Hall",
		"-out", outPath,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var session room.Session
	if err = json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}
	if session.SchemaVersion != room.SchemaVersion || session.VenueLabel != "CLI Hall" {
		t.Fatalf("session header: %+v", session)
	}
	if session.PARoom == nil || session.PARoom.Status != "measured" || session.PARoom.Method != room.PAMethodPinkAveraging {
		t.Fatalf("pa response: %+v", session.PARoom)
	}
	if len(session.PARoom.TransferSmoothedDB) == 0 || session.PARoom.TestSignal == nil || session.PARoom.TestSignal.Seed != 12 {
		t.Fatalf("transfer/spec missing: %+v", session.PARoom)
	}
	md, err := os.ReadFile(strings.TrimSuffix(outPath, filepath.Ext(outPath)) + ".md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(md)
	if !strings.Contains(text, "## PA / room response") || !strings.Contains(text, "measured") {
		t.Fatalf("markdown missing PA section:\n%s", text)
	}
}

func TestCLIRequiresEmptyUnlessGenerating(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("expected usage error")
	}
}

func TestCLIStudioLiveChannelRequiresJACKBuild(t *testing.T) {
	if live.JACKBuilt() {
		t.Skip("JACK capture is linked in this build")
	}
	err := run([]string{"-studiolive-channel", "3", "-live-plain"})
	if !errors.Is(err, live.ErrJACKDisabled) {
		t.Fatalf("got %v", err)
	}
}

func TestCLILiveOptions(t *testing.T) {
	opts, err := liveOptions("jack", []string{"_Ch 1_in"}, 10, "jive-room")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(opts.JACKSources, "|"); got != "_Ch 1_in|_Ch 11_in|_Ch 12_in" {
		t.Fatalf("sources %q", got)
	}
	if _, err := liveOptions("jack", nil, 0, "jive-room"); err == nil {
		t.Fatal("jack without sources accepted")
	}
	if _, err := liveOptions("jack", nil, 13, "jive-room"); err == nil {
		t.Fatal("channel 13 accepted")
	}
	if err := run([]string{"-studiolive-channel", "13"}); !isUsage(err) {
		t.Fatalf("channel 13 should be a usage error, got %v", err)
	}
}

func TestCLILiveRequiresPipeWireBuild(t *testing.T) {
	if live.PipeWireBuilt() {
		t.Skip("PipeWire capture is linked in this build")
	}
	err := run([]string{"-live"})
	if !errors.Is(err, live.ErrPipeWireDisabled) {
		t.Fatalf("got %v", err)
	}
}
