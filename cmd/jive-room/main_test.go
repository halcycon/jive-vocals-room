package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxmatters/jive-vocals/internal/room"
)

func TestCLIGeneratePinkAndAnalysePAResponse(t *testing.T) {
	dir := t.TempDir()
	emptyPath := filepath.Join(dir, "empty.wav")
	refPath := filepath.Join(dir, "pink.wav")
	outPath := filepath.Join(dir, "session.json")

	emptySpec := room.DefaultPinkSpec()
	emptySpec.DurationSeconds = 2
	emptySpec.Seed = 11
	emptySpec.HeadroomDB = 18
	if _, err := room.WritePinkWAV(emptyPath, emptySpec); err != nil {
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
