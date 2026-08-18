package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestReadJSONRejectsUnsupportedMajorVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"jive-room-session/v2"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadJSON(path)
	if err == nil || !strings.Contains(err.Error(), "v1 only") {
		t.Fatalf("expected clear major-version error, got %v", err)
	}
}

func TestReadJSONGoldenV1(t *testing.T) {
	got, err := ReadJSON("schema_v1.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion || got.VenueLabel != "Golden Hall" {
		t.Fatalf("golden header: %+v", got)
	}
	if got.Microphone.Calibration == nil || got.Microphone.Calibration.SPLCalibrated {
		t.Fatal("golden calibration must exist and must not claim SPL")
	}
	raw, err := os.ReadFile("schema_v1.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "empty_room", "pa_room_response", "mixer", "suggested_eq", "completed_stages"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("golden missing key %s", key)
		}
	}
}
