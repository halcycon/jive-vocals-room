package studiolive

import (
	"math"
	"testing"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/osc"
)

func TestParseAddress(t *testing.T) {
	c, ok := ParseAddress("/channel/ch3/eqmidgain")
	if !ok || c != (Control{"ch3", "eqmidgain"}) || c.Address() != "/channel/ch3/eqmidgain" {
		t.Fatalf("got %+v %v", c, ok)
	}
	for _, a := range []string{"/channel/ch3", "/analyser/band/1", "/channel//x", "/channel/ch1/a/b"} {
		if _, ok := ParseAddress(a); ok {
			t.Fatalf("%q accepted", a)
		}
	}
}

func TestProposableIsHPFEQAndGEQBandsOnly(t *testing.T) {
	yes := []Control{{"ch1", "hpffreq"}, {"ch12", "eqmidhiq"}, {"geq0", "1"}, {"geq0", "31"}}
	no := []Control{
		{"ch1", "gain"},
		{"ch1", "phantom"},
		{"ch1", "mute"},
		{"ch1", "solo"},
		{"ch1", "toMain"},
		{"ch1", "firewire"},
		{"ch1", "post"},
		{"ch1", "aux1"},
		{"ch1", "fxa"},
		{"ch1", "compthresh"},
		{"geq0", "enable"},
		{"geq0", "reset"},
		{"geq0", "32"},
		{"geq0", "0"},
		{"ch13", "eqmidgain"},
		{"main", "eqmidgain"},
		{"aux1", "eqmidgain"},
	}
	for _, c := range yes {
		if !Proposable(c) {
			t.Fatalf("%v should be proposable", c)
		}
	}
	for _, c := range no {
		if Proposable(c) {
			t.Fatalf("%v must never be proposable", c)
		}
	}
}

func TestGEQBandAddressing(t *testing.T) {
	if len(GEQBands) != 31 {
		t.Fatalf("%d GEQ bands", len(GEQBands))
	}
	c, err := GEQBandControl(17)
	if err != nil || c.Address() != "/channel/geq0/18" || GEQBands[17] != 1000 {
		t.Fatalf("1 kHz band: %v %v", c, err)
	}
	if i, ok := GEQBandIndex(c); !ok || i != 17 {
		t.Fatal("round trip")
	}
}

// pythonOscliveStore mirrors osclive: SLRemote clamps, then the raw backend
// stores int(value*255) from the received float32.
func pythonOscliveStore(sent float32) int {
	v := math.Max(0, math.Min(1, float64(sent)))
	return int(v * 255)
}

func TestValueForRawLandsOnEveryStep(t *testing.T) {
	for r := range 256 {
		if got := pythonOscliveStore(ValueForRaw(uint8(r))); got != r {
			t.Fatalf("raw %d stored as %d", r, got)
		}
		if RawFromValue(float64(r)/255) != uint8(r) {
			t.Fatalf("read-back raw %d", r)
		}
	}
	if pythonOscliveStore(0.5) != GEQFlatRaw {
		t.Fatal("osclive's 0.5 reset should store raw 127")
	}
}

func TestCalibrationMatchesSessionLabels(t *testing.T) {
	mid := map[string]float64{"hpffreq": 155, "eqlowfreq": 129, "eqmidfreq": 954, "eqhighfreq": 5000, "eqmidgain": 0, "compthresh": -28}
	for name, want := range mid {
		got := InputCalibration[name].ToUnit(0.5)
		if math.Abs(got-want) > math.Max(1, 0.01*math.Abs(want)) {
			t.Fatalf("%s midpoint %.1f, session label %.1f", name, got, want)
		}
	}
	cal := InputCalibration["eqmidfreq"]
	for _, hz := range []float64{260, 500, 954, 2000, 3500} {
		if back := cal.ToUnit(cal.FromUnit(hz)); math.Abs(back-hz) > 1e-6*hz {
			t.Fatalf("round trip %.1f -> %.1f", hz, back)
		}
	}
	if cal.FromUnit(10) != 0 || cal.FromUnit(1e6) != 1 {
		t.Fatal("FromUnit must clamp")
	}
	if step := InputCalibration["eqmidgain"].UnitStep(127); math.Abs(step-30.0/255) > 1e-9 {
		t.Fatalf("gain step %.4f dB", step)
	}
	for name, c := range InputCalibration {
		if c.Verified {
			t.Fatalf("%s marked verified without a desk check", name)
		}
	}
	if _, ok := CalibrationFor(Control{"geq0", "18"}); ok {
		t.Fatal("GEQ gain has no known calibration")
	}
}

func TestStateApply(t *testing.T) {
	s := NewState()
	now := time.Unix(0, 0)
	m := osc.Message{Address: "/channel/ch2/eqmidgain", Args: []any{float32(127.0 / 255)}}
	c, v, changed := s.Apply(m, now)
	if !changed || c != (Control{"ch2", "eqmidgain"}) || v.Raw != 127 {
		t.Fatalf("first apply %v %+v %v", c, v, changed)
	}
	if _, _, changed := s.Apply(m, now); changed {
		t.Fatal("same raw reported as change")
	}
	if _, v, changed := s.Apply(osc.Message{Address: "/channel/ch2/eqmid", Args: []any{int32(1)}}, now); !changed || !v.On() || v.Raw != 1 {
		t.Fatalf("switch %+v", v)
	}
	for _, addr := range []string{"/channel/ch2/level", "/connection_ping", "/channel/ch2/label"} {
		if _, _, changed := s.Apply(osc.Message{Address: addr, Args: []any{int32(3)}}, now); changed {
			t.Fatalf("%s treated as a control", addr)
		}
	}
	if s.Len() != 2 {
		t.Fatalf("len %d", s.Len())
	}
}
