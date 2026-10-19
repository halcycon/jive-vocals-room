package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/osc"
	"github.com/linuxmatters/jive-vocals/internal/room"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

// uiSink records what the daemon sends to the bridge.
type uiSink struct {
	conn *net.UDPConn
	mu   sync.Mutex
	msgs []osc.Message
}

func newUISink(t *testing.T) *uiSink {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s := &uiSink{conn: c}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, _, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			ms, _ := osc.Decode(buf[:n])
			s.mu.Lock()
			s.msgs = append(s.msgs, ms...)
			s.mu.Unlock()
		}
	}()
	t.Cleanup(func() { _ = c.Close() })
	return s
}

func (s *uiSink) waitFor(t *testing.T, addr, contains string) osc.Message {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, m := range s.msgs {
			if m.Address == addr && len(m.Args) > 0 {
				if v, _ := m.Args[0].(string); strings.Contains(v, contains) {
					s.mu.Unlock()
					return m
				}
			}
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("no %s containing %q; got %v", addr, contains, s.msgs)
	return osc.Message{}
}

func testDaemon(t *testing.T, sink string) (*daemon, *uiSink) {
	t.Helper()
	ui := newUISink(t)
	bc, err := studiolive.DialBridge(ui.conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bc.Close() })
	dir := t.TempDir()
	cfg := daemonConfig{
		bridge: ui.conn.LocalAddr().String(), sessionsRoot: filepath.Join(dir, "sessions"), profilesDir: filepath.Join(dir, "profiles"),
		playSink: sink, captureSeconds: 15, pink: room.DefaultPinkSpec(), jackClient: "jive-room-test",
	}
	cfg.pink.HeadroomDB = minPlaybackHeadroomDB
	for _, p := range []string{cfg.sessionsRoot, cfg.profilesDir} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := &daemon{cfg: cfg, bc: bc, ctx: context.Background(), out: &bytes.Buffer{}, channel: 1}
	d.newSession()
	return d, ui
}

func cmd(name string, args ...any) osc.Message {
	return osc.Message{Address: studiolive.CommandPrefix + name, Args: args}
}

func TestDaemonChannelValidation(t *testing.T) {
	d, ui := testDaemon(t, "")
	for _, bad := range []any{int32(0), int32(13), float32(2.5), "3"} {
		d.command(cmd("channel", bad))
	}
	if d.channel != 1 {
		t.Fatalf("invalid channel accepted: %d", d.channel)
	}
	d.command(cmd("channel", int32(7)))
	ui.waitFor(t, "/dsp/ui/room/stage", "CH7")
	d.heartbeat()
	ui.waitFor(t, "/dsp/ui/room/daemon", "CH7 · monitor off · idle")
}

func TestDaemonPAArmConfirmGate(t *testing.T) {
	d, ui := testDaemon(t, "")
	d.command(cmd("pa/arm", float32(1)))
	ui.waitFor(t, "/dsp/ui/room/stage", "disabled")
	d2, ui2 := testDaemon(t, "_Ch 16_out")
	d2.command(cmd("pa/confirm", float32(1)))
	ui2.waitFor(t, "/dsp/ui/room/stage", "press ARM first")
	d2.command(cmd("pa/arm", float32(0)))
	d2.command(cmd("pa/confirm", float32(1)))
	if d2.task != "" {
		t.Fatal("release (0) armed playback")
	}
	d2.command(cmd("pa/arm", float32(1)))
	ui2.waitFor(t, "/dsp/ui/room/stage", "ARMED")
	d2.paArmedUntil = time.Now().Add(-time.Second)
	d2.command(cmd("pa/confirm", float32(1)))
	ui2.waitFor(t, "/dsp/ui/room/stage", "not started")
}

func TestDaemonAnalyseNeedsEmptyRoom(t *testing.T) {
	d, ui := testDaemon(t, "")
	d.command(cmd("analyse", float32(1)))
	ui.waitFor(t, "/dsp/ui/room/stage", "capture the empty room first")
}

func TestDaemonProfileSaveAndRestoreSubmitsPendingOnly(t *testing.T) {
	d, ui := testDaemon(t, "")
	now := time.Unix(0, 0)
	for n := 1; n <= studiolive.InputChannels; n++ {
		for name, kind := range studiolive.InputControls {
			v := any(float32(128.0 / 255))
			if kind == studiolive.KindSwitch {
				v = int32(0)
			}
			d.bc.State.Apply(osc.Message{Address: studiolive.Control{Channel: studiolive.InputChannelName(n), Name: name}.Address(), Args: []any{v}}, now)
		}
	}
	for i := range studiolive.GEQBands {
		c, _ := studiolive.GEQBandControl(i)
		d.bc.State.Apply(osc.Message{Address: c.Address(), Args: []any{float32(127.0 / 255)}}, now)
	}
	if err := d.saveProfile(); err != nil {
		t.Fatal(err)
	}
	ui.waitFor(t, "/dsp/ui/room/stage", "saved 199 desk controls")
	if err := d.submitProfile(); err != nil {
		t.Fatal(err)
	}
	ui.waitFor(t, "/dsp/ui/room/stage", "already matches")
	d.bc.State.Apply(osc.Message{Address: "/channel/ch4/eqlowgain", Args: []any{float32(90.0 / 255)}}, now)
	if err := d.submitProfile(); err != nil {
		t.Fatal(err)
	}
	ui.waitFor(t, "/dsp/ui/room/stage", "double-tap APPLY")
	m := ui.waitFor(t, "/dsp/proposal/set", `"source":"profile"`)
	if !strings.Contains(m.Args[0].(string), `"/channel/ch4/eqlowgain","raw":128`) {
		t.Fatalf("proposal %v", m.Args[0])
	}
	ui.mu.Lock()
	defer ui.mu.Unlock()
	for _, msg := range ui.msgs {
		if msg.Address == "/dsp/apply" || strings.HasPrefix(msg.Address, "/channel/") {
			t.Fatalf("daemon sent %s; it must only submit pending proposals", msg.Address)
		}
	}
}

func TestSummariseSession(t *testing.T) {
	lines := summariseSession(room.Session{
		CompletedStages: []string{room.StageEmpty, room.StagePA},
		PARoom:          &room.PAResponse{Status: "insufficient_signal", SignalToNoise: &room.PASignalToNoise{MedianDB: 0.2}},
		SuggestedEQ:     []room.Recommendation{{Kind: "warning", Reason: "converter floor"}},
	})
	text := strings.Join(lines, "\n")
	for _, want := range []string{"empty_room, pa_room_response", "insufficient_signal (median 0.2 dB", "warning: converter floor"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}
