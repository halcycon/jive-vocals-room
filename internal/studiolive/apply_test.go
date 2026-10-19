package studiolive

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/osc"
)

func TestWireProposalValidate(t *testing.T) {
	ok := WireProposal{Schema: ProposalSchema, ID: "p-1", Source: SourceProfile, Changes: []WireChange{{"/channel/ch1/eqmidgain", 100}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*WireProposal){
		"schema":   func(p *WireProposal) { p.Schema = "x" },
		"id":       func(p *WireProposal) { p.ID = "bad id" },
		"source":   func(p *WireProposal) { p.Source = "automatic" },
		"empty":    func(p *WireProposal) { p.Changes = nil },
		"fader":    func(p *WireProposal) { p.Changes = []WireChange{{"/channel/ch1/gain", 1}} },
		"phantom":  func(p *WireProposal) { p.Changes = []WireChange{{"/channel/ch1/phantom", 1}} },
		"geqreset": func(p *WireProposal) { p.Changes = []WireChange{{"/channel/geq0/reset", 1}} },
		"range":    func(p *WireProposal) { p.Changes = []WireChange{{"/channel/ch1/eqmidgain", 256}} },
		"switch":   func(p *WireProposal) { p.Changes = []WireChange{{"/channel/ch1/eqmid", 2}} },
		"dup":      func(p *WireProposal) { p.Changes = append(p.Changes, p.Changes[0]) },
	} {
		p := ok
		p.Changes = append([]WireChange(nil), ok.Changes...)
		mut(&p)
		if p.Validate() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestProposalFromRingOutGatesCalibration(t *testing.T) {
	ps := RingOutProposals(1250, 3, nil)
	if _, err := ProposalFromRingOut(ps[0], true, time.Now()); err == nil {
		t.Fatal("unmappable GEQ proposal accepted")
	}
	if _, err := ProposalFromRingOut(ps[1], false, time.Now()); !errors.Is(err, ErrUnverifiedCalibration) {
		t.Fatalf("unverified channel mid accepted: %v", err)
	}
	w, err := ProposalFromRingOut(ps[1], true, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if w.Source != SourceRingOut || w.Changes[0].Address != "/channel/ch3/eqmidgain" || w.Changes[len(w.Changes)-1].Address != "/channel/ch3/eqmid" {
		t.Fatalf("gain must come first and enable last: %+v", w.Changes)
	}
}

func TestProfileRoundTripAndDiff(t *testing.T) {
	st := NewState()
	now := time.Unix(0, 0)
	for n := 1; n <= InputChannels; n++ {
		for name, kind := range InputControls {
			v := any(float32(128.0 / 255))
			if kind == KindSwitch {
				v = int32(0)
			}
			st.Apply(osc.Message{Address: Control{Channel: InputChannelName(n), Name: name}.Address(), Args: []any{v}}, now)
		}
	}
	for i := range GEQBands {
		c, _ := GEQBandControl(i)
		st.Apply(osc.Message{Address: c.Address(), Args: []any{float32(127.0 / 255)}}, now)
	}
	p, err := ProfileFromState(st, []int{3}, true, "test", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Controls) != len(proposable)+len(GEQBands) {
		t.Fatalf("%d controls", len(p.Controls))
	}
	for _, c := range p.Controls {
		ctl, _ := ParseAddress(c.Address)
		if !Proposable(ctl) {
			t.Fatalf("non-proposable %s saved", c.Address)
		}
	}
	b, _ := json.Marshal(p)
	var back Profile
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if d, err := ProfileDiff(back, st); err != nil || len(d) != 0 {
		t.Fatalf("unchanged desk diff %v %v", d, err)
	}
	st.Apply(osc.Message{Address: "/channel/ch3/eqmidgain", Args: []any{float32(90.0 / 255)}}, now)
	d, err := ProfileDiff(back, st)
	if err != nil || len(d) != 1 || d[0] != (WireChange{"/channel/ch3/eqmidgain", 128}) {
		t.Fatalf("diff %v %v", d, err)
	}
	w, err := ProposalFromProfile(back, d, now)
	if err != nil || w.Source != SourceProfile {
		t.Fatalf("%+v %v", w, err)
	}
	back.Controls = append(back.Controls, ProfileControl{Address: "/channel/ch1/gain", Raw: 200})
	if _, err := ProfileDiff(back, st); err == nil {
		t.Fatal("tampered profile with a fader accepted")
	}
	if _, err := ProfileFromState(NewState(), []int{1}, false, "", now); err == nil {
		t.Fatal("profile from empty state accepted")
	}
}

func TestSubmitApplyRevertAndResults(t *testing.T) {
	lo := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}
	srv, err := net.ListenUDP("udp", lo)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := DialBridge(srv.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got := make(chan ApplyResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx, Events{OnResult: func(r ApplyResult) { got <- r }}) }()

	w := WireProposal{Schema: ProposalSchema, ID: "p-1", Source: SourceProfile, Summary: "s", Changes: []WireChange{{"/channel/ch1/eqmidgain", 100}}}
	if err := c.SubmitProposal(w); err != nil {
		t.Fatal(err)
	}
	if err := c.RequestApply("p-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.RequestRevert(); err != nil {
		t.Fatal(err)
	}
	if err := c.SubmitProposal(WireProposal{}); err == nil {
		t.Fatal("invalid proposal sent")
	}
	seen := map[string]osc.Message{}
	buf := make([]byte, 65535)
	var client *net.UDPAddr
	_ = srv.SetReadDeadline(time.Now().Add(2 * time.Second))
	for len(seen) < 4 {
		n, from, err := srv.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("got %v", seen)
		}
		ms, _ := osc.Decode(buf[:n])
		for _, m := range ms {
			seen[m.Address] = m
		}
		client = from
	}
	var sent WireProposal
	if err := json.Unmarshal([]byte(seen["/dsp/proposal/set"].Args[0].(string)), &sent); err != nil || sent.ID != "p-1" {
		t.Fatalf("proposal payload %v %v", seen["/dsp/proposal/set"], err)
	}
	if seen["/dsp/apply"].Args[0] != "p-1" || len(seen["/dsp/revert"].Args) != 0 {
		t.Fatalf("apply/revert %v", seen)
	}
	res, _ := json.Marshal(ApplyResult{Schema: ResultSchema, Action: "apply", ID: "p-1", Status: "verified"})
	for _, m := range []osc.Message{{Address: "/dsp/result"}, {Address: "/dsp/result", Args: []any{string(res)}}} {
		b, _ := osc.Encode(m)
		_, _ = srv.WriteToUDP(b, client)
	}
	select {
	case r := <-got:
		if r.Status != "verified" || r.ID != "p-1" {
			t.Fatalf("result %+v", r)
		}
	case <-ctx.Done():
		t.Fatal("no result delivered")
	}
}
