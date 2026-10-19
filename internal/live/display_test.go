package live

import (
	"strings"
	"testing"

	"github.com/linuxmatters/jive-vocals/internal/room"
)

func TestBandLabel(t *testing.T) {
	cases := map[float64]string{
		80:   "80",
		1000: "1k",
		1500: "1.5k",
		2250: "2.2k",
		5000: "5k",
	}
	for hz, want := range cases {
		if got := BandLabel(hz); got != want {
			t.Errorf("BandLabel(%v)=%q want %q", hz, got, want)
		}
	}
}

func TestMonitorPeakHoldAndFreezeDelta(t *testing.T) {
	m := NewMonitor()
	loud := Snapshot{
		HasSpectrum: true,
		RMSDB:       -20,
		Bands: []room.SpectralPoint{
			{FrequencyHz: 290, LevelDB: -12},
			{FrequencyHz: 1000, LevelDB: -24},
		},
	}
	quiet := Snapshot{
		HasSpectrum: true,
		RMSDB:       -26,
		Bands: []room.SpectralPoint{
			{FrequencyHz: 290, LevelDB: -18},
			{FrequencyHz: 1000, LevelDB: -24},
		},
	}
	m.Observe(loud)
	if m.PeakHold[0] != -12 {
		t.Fatalf("peak hold=%v", m.PeakHold)
	}
	m.Freeze(loud)
	m.Observe(quiet)
	text := m.Render(quiet, 72)
	if !strings.Contains(text, "290") || !strings.Contains(text, "1k") {
		t.Fatalf("missing band labels:\n%s", text)
	}
	deltaLine := ""
	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(line, "Δ") {
			deltaLine = line
			break
		}
	}
	if !strings.Contains(deltaLine, "-6") {
		t.Fatalf("expected freeze delta around -6 dB on the Δ row:\n%s", text)
	}
	if m.PeakHold[0] < quiet.Bands[0].LevelDB {
		t.Fatalf("peak hold fell below current: %v", m.PeakHold)
	}
}

func TestSparklineLength(t *testing.T) {
	s := Sparkline([]float64{-40, -30, -20, -30}, 10)
	if n := len([]rune(s)); n != 10 {
		t.Fatalf("len=%d s=%q", n, s)
	}
}

func TestLoudestBand(t *testing.T) {
	b, ok := LoudestBand([]room.SpectralPoint{
		{FrequencyHz: 80, LevelDB: -40},
		{FrequencyHz: 1000, LevelDB: -10},
		{FrequencyHz: 5000, LevelDB: -22},
	})
	if !ok || b.FrequencyHz != 1000 || b.LevelDB != -10 {
		t.Fatalf("got %+v ok=%v", b, ok)
	}
}
