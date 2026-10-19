package studiolive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/osc"
)

const (
	DefaultBridgeAddr = "127.0.0.1:9002"

	addrSubscribe   = "/dsp/subscribe"
	addrUnsubscribe = "/dsp/unsubscribe"
	addrReplayDone  = "/dsp/replay_done"

	// The bridge expires subscribers after 15 s without a renewal.
	subscribeInterval     = 5 * time.Second
	replayReadBufferBytes = 4 << 20
)

// BridgeClient subscribes to the bridge's mixer-state stream. It sends
// /dsp/subscribe, /dsp/unsubscribe, /dsp/ui/... display values, and
// allow-listed proposals plus apply/revert requests that the bridge validates
// and executes. It has no path to emit /channel/... writes itself.
type BridgeClient struct {
	conn   *net.UDPConn
	bridge *net.UDPAddr
	State  *State
}

// DialBridge opens a localhost UDP socket for the bridge's DSP port.
func DialBridge(addr string) (*BridgeClient, error) {
	bridge, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: bridge.IP})
	if err != nil {
		return nil, err
	}
	// The cache replay is a burst of ~800 small datagrams; a slow first read
	// must not overflow the default receive buffer.
	_ = conn.SetReadBuffer(replayReadBufferBytes)
	return &BridgeClient{conn: conn, bridge: bridge, State: NewState()}, nil
}

func (c *BridgeClient) send(addr string, args ...any) error {
	b, err := osc.Encode(osc.Message{Address: addr, Args: args})
	if err != nil {
		return err
	}
	_, err = c.conn.WriteToUDP(b, c.bridge)
	return err
}

// UIPrefix is the only namespace PublishUI accepts; the bridge forwards it to
// Open Stage Control and nowhere else.
const UIPrefix = "/dsp/ui/"

// PublishUI sends a display value to Open Stage Control through the bridge.
func (c *BridgeClient) PublishUI(addr string, args ...any) error {
	if !strings.HasPrefix(addr, UIPrefix) || len(addr) == len(UIPrefix) {
		return fmt.Errorf("refusing to publish %q: only %s... display addresses are allowed", addr, UIPrefix)
	}
	return c.send(addr, args...)
}

func (c *BridgeClient) port() int {
	if a, ok := c.conn.LocalAddr().(*net.UDPAddr); ok {
		return a.Port
	}
	return 0
}

// Update is one control change observed from the bridge.
type Update struct {
	Control Control
	Value   Value
}

// Events are delivered from Run. Replayed reports the bridge's cache replay
// count once the initial state is complete.
type Events struct {
	OnUpdate   func(Update)
	OnReplayed func(count int)
	// OnResult receives the bridge's submit/apply/revert reports.
	OnResult func(ApplyResult)
}

// Run subscribes, renews the subscription, and applies updates to State until
// ctx ends, then unsubscribes.
func (c *BridgeClient) Run(ctx context.Context, ev Events) error {
	if err := c.send(addrSubscribe, int32(c.port())); err != nil { //nolint:gosec // UDP port fits int32
		return fmt.Errorf("subscribe to bridge %s: %w", c.bridge, err)
	}
	defer func() { _ = c.send(addrUnsubscribe, int32(c.port())) }() //nolint:gosec // UDP port fits int32
	renew := time.Now().Add(subscribeInterval)
	buf := make([]byte, 65535)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if time.Now().After(renew) {
			_ = c.send(addrSubscribe, int32(c.port())) //nolint:gosec // UDP port fits int32
			renew = time.Now().Add(subscribeInterval)
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, from, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		// The bridge replies from its shared output socket, not the DSP port.
		if !from.IP.Equal(c.bridge.IP) {
			continue
		}
		msgs, err := osc.Decode(buf[:n])
		if err != nil {
			continue
		}
		now := time.Now()
		for _, m := range msgs {
			if m.Address == addrReplayDone {
				if count, ok := m.Float(0); ok && ev.OnReplayed != nil {
					ev.OnReplayed(int(count))
				}
				continue
			}
			if m.Address == addrResult {
				if len(m.Args) != 1 || ev.OnResult == nil {
					continue
				}
				if s, ok := m.Args[0].(string); ok {
					var r ApplyResult
					if json.Unmarshal([]byte(s), &r) == nil {
						ev.OnResult(r)
					}
				}
				continue
			}
			if ctl, v, changed := c.State.Apply(m, now); changed && ev.OnUpdate != nil {
				ev.OnUpdate(Update{Control: ctl, Value: v})
			}
		}
	}
}

func (c *BridgeClient) Close() error { return c.conn.Close() }
