package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteJSONVersionedRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	want := Session{SchemaVersion: SchemaVersion, VenueLabel: "Hall", CreatedAt: time.Unix(1, 0).UTC(), EmptyRoom: Measurement{Source: "empty.wav"}, Mixer: DefaultParametricMixer()}
	if err := WriteJSON(path, want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Session
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion || got.VenueLabel != "Hall" {
		t.Fatalf("round trip: %+v", got)
	}
}

func TestWriteJSONRejectsUnknownSchema(t *testing.T) {
	if err := WriteJSON(filepath.Join(t.TempDir(), "x.json"), Session{SchemaVersion: "future"}); err == nil {
		t.Fatal("expected schema error")
	}
}
