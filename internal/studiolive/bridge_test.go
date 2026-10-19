package studiolive

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/osc"
)

// fakeBridge records what the client sends and replays a small cache, as the
// real bridge does on /dsp/subscribe. Like the real bridge, it replies from a
// separate output socket rather than the DSP listening port.
type fakeBridge struct {
	conn *net.UDPConn
	out  *net.UDPConn
	mu   sync.Mutex
	seen []string
}

func newFakeBridge(t *testing.T) *fakeBridge {
	t.Helper()
	lo := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}
	conn, err := net.ListenUDP("udp", lo)
	if err != nil {
		t.Fatal(err)
	}
	out, err := net.ListenUDP("udp", lo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	return &fakeBridge{conn: conn, out: out}
}

func (f *fakeBridge) addresses() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *fakeBridge) serve() {
	buf := make([]byte, 65535)
	for {
		n, from, err := f.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		msgs, err := osc.Decode(buf[:n])
		if err != nil {
			continue
		}
		for _, m := range msgs {
			f.mu.Lock()
			f.seen = append(f.seen, m.Address)
			f.mu.Unlock()
			if m.Address != "/dsp/subscribe" {
				continue
			}
			port, _ := m.Float(0)
			to := &net.UDPAddr{IP: from.IP, Port: int(port)}
			for _, out := range []osc.Message{
				{Address: "/channel/ch1/eqmidfreq", Args: []any{float32(0.5)}},
				{Address: "/channel/ch1/eqmid", Args: []any{int32(1)}},
				{Address: "/channel/geq0/18", Args: []any{float32(120.0 / 255)}},
				{Address: "/channel/ch1/level", Args: []any{int32(5)}},
				{Address: "/dsp/replay_done", Args: []any{int32(4)}},
			} {
				b, _ := osc.Encode(out)
				_, _ = f.out.WriteToUDP(b, to)
			}
		}
	}
}

func TestBridgeClientSubscribesReadsAndNeverWrites(t *testing.T) {
	fb := newFakeBridge(t)
	defer fb.conn.Close()
	go fb.serve()

	c, err := DialBridge(fb.conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	replayed := make(chan int, 1)
	var updates []Update
	var mu sync.Mutex
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, Events{
			OnUpdate: func(u Update) {
				mu.Lock()
				updates = append(updates, u)
				mu.Unlock()
			},
			OnReplayed: func(n int) { replayed <- n },
		})
	}()
	select {
	case n := <-replayed:
		if n != 4 {
			t.Fatalf("replay count %d", n)
		}
	case <-ctx.Done():
		t.Fatal("no replay")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(updates) != 3 {
		t.Fatalf("updates %+v", updates)
	}
	mu.Unlock()
	geq, _ := GEQBandControl(17)
	if v, ok := c.State.Get(geq); !ok || v.Raw != 120 || RawStepsFromFlat(v.Raw) != -7 {
		t.Fatalf("geq 1 kHz %+v", v)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if a := fb.addresses(); len(a) > 0 && a[len(a)-1] == "/dsp/unsubscribe" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, a := range fb.addresses() {
		if !strings.HasPrefix(a, "/dsp/subscribe") && a != "/dsp/unsubscribe" {
			t.Fatalf("client sent %q; it must only subscribe/unsubscribe", a)
		}
	}
	if a := fb.addresses(); a[len(a)-1] != "/dsp/unsubscribe" {
		t.Fatalf("no unsubscribe on exit: %v", a)
	}
}
