package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/linuxmatters/jive-vocals/internal/live"
	"github.com/linuxmatters/jive-vocals/internal/room"
)

func TestLiveModelFreezeAndReset(t *testing.T) {
	m := newLiveModel()
	updated, _ := m.Update(liveSnapMsg{Snapshot: live.Snapshot{
		HasSpectrum: true,
		RMSDB:       -20,
		Bands:       []room.SpectralPoint{{FrequencyHz: 290, LevelDB: -12}},
	}})
	m = updated.(*liveModel)
	updated, cmd := m.Update(tea.KeyPressMsg{Text: " ", Code: tea.KeySpace})
	m = updated.(*liveModel)
	if cmd != nil {
		t.Fatalf("space should not quit, got %v", cmd)
	}
	if !m.mon.HasFreeze() {
		t.Fatal("expected freeze after space")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Text: "r", Code: 'r'})
	m = updated.(*liveModel)
	if m.mon.HasFreeze() {
		t.Fatal("reset should clear freeze")
	}
}
