package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

const resultTimeout = 10 * time.Second

// bridgeSession is a subscribed BridgeClient with its initial state replayed.
type bridgeSession struct {
	c       *studiolive.BridgeClient
	results chan studiolive.ApplyResult
	done    chan error
	cancel  context.CancelFunc
}

func openBridgeSession(addr string) (*bridgeSession, error) {
	c, err := studiolive.DialBridge(addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &bridgeSession{c: c, results: make(chan studiolive.ApplyResult, 8), done: make(chan error, 1), cancel: cancel}
	replayed := make(chan int, 1)
	go func() {
		s.done <- c.Run(ctx, studiolive.Events{
			OnReplayed: func(n int) {
				select {
				case replayed <- n:
				default:
				}
			},
			OnResult: func(r studiolive.ApplyResult) { s.results <- r },
		})
	}()
	select {
	case <-replayed:
		return s, nil
	case err := <-s.done:
		s.cancel()
		_ = s.c.Close()
		return nil, err
	case <-time.After(replayTimeout):
		s.close()
		return nil, fmt.Errorf("no state replay from bridge %s within %s (is the DSP-enabled bridge running?)", addr, replayTimeout)
	}
}

func (s *bridgeSession) await(action, id string) (studiolive.ApplyResult, error) {
	deadline := time.After(resultTimeout)
	for {
		select {
		case r := <-s.results:
			if r.Action == action && (id == "" || r.ID == id) {
				return r, nil
			}
		case <-deadline:
			return studiolive.ApplyResult{}, fmt.Errorf("no %s result from the bridge within %s", action, resultTimeout)
		}
	}
}

// close stops Run and waits for it, so its /dsp/unsubscribe is sent before
// the socket closes; otherwise each CLI run would hold a subscriber slot
// until the bridge's TTL expires.
func (s *bridgeSession) close() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(time.Second):
	}
	_ = s.c.Close()
}

func runProfileSave(bridgeAddr, path string, channel int, label string) error {
	s, err := openBridgeSession(bridgeAddr)
	if err != nil {
		return err
	}
	defer s.close()
	var channels []int
	if channel != 0 {
		channels = []int{channel}
	}
	p, err := studiolive.ProfileFromState(s.c.State, channels, channel == 0, label, time.Now())
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	fmt.Printf("Saved %d HPF/EQ/GEQ controls to %s. Read-only: nothing was written to the desk.\n", len(p.Controls), path)
	return nil
}

func loadProfile(path string) (studiolive.Profile, error) {
	var p studiolive.Profile
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

func printDiff(w io.Writer, changes []studiolive.WireChange, st *studiolive.State) {
	for _, ch := range changes {
		ctl, _ := studiolive.ParseAddress(ch.Address)
		cur, _ := st.Get(ctl)
		line := fmt.Sprintf("  %-26s raw %3d -> %3d", ctl, cur.Raw, ch.Raw)
		if cal, ok := studiolive.CalibrationFor(ctl); ok {
			line += fmt.Sprintf("   (model %s -> %s)", formatUnit(cal.ToUnit(cur.Value), cal.Unit), formatUnit(cal.ToUnit(float64(ch.Raw)/255), cal.Unit))
		}
		fmt.Fprintln(w, line)
	}
}

func runProfilePush(bridgeAddr, path string, confirmed bool) error {
	p, err := loadProfile(path)
	if err != nil {
		return err
	}
	s, err := openBridgeSession(bridgeAddr)
	if err != nil {
		return err
	}
	defer s.close()
	changes, err := studiolive.ProfileDiff(p, s.c.State)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		fmt.Printf("The desk already matches %s; nothing to write.\n", path)
		return nil
	}
	fmt.Printf("Profile %q differs from the desk on %d control(s):\n", p.Label, len(changes))
	printDiff(os.Stdout, changes, s.c.State)
	if !confirmed {
		fmt.Println("Dry run: nothing was written. Re-run with -confirm-apply to write these through the bridge (snapshot and undo included).")
		return nil
	}
	w, err := studiolive.ProposalFromProfile(p, changes, time.Now())
	if err != nil {
		return err
	}
	return submitAndApply(s, w)
}

func submitAndApply(s *bridgeSession, w studiolive.WireProposal) error {
	if err := s.c.SubmitProposal(w); err != nil {
		return err
	}
	r, err := s.await("submit", w.ID)
	if err != nil {
		return err
	}
	if r.Status != "pending" {
		return fmt.Errorf("bridge rejected the proposal: %s", r.Reason)
	}
	if err := s.c.RequestApply(w.ID); err != nil {
		return err
	}
	r, err = s.await("apply", w.ID)
	if err != nil {
		return err
	}
	return reportResult(r)
}

func reportResult(r studiolive.ApplyResult) error {
	msg := fmt.Sprintf("%s %s: %s", r.Action, r.ID, r.Status)
	if r.Written != nil {
		msg += fmt.Sprintf(" (%d written, %d already set)", *r.Written, deref(r.Unchanged))
	}
	if r.Reason != "" {
		msg += " - " + r.Reason
	}
	fmt.Println(msg)
	switch r.Status {
	case "verified":
		if r.Action == "apply" {
			fmt.Println("Undo with: jive-room -studiolive-undo -confirm-apply (or UNDO in the ROOM tab).")
		}
		return nil
	case "unverified":
		return fmt.Errorf("the desk did not confirm every value; check it, and undo if needed")
	default:
		return fmt.Errorf("%s %s", r.Action, r.Status)
	}
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func runUndo(bridgeAddr string, confirmed bool) error {
	if !confirmed {
		fmt.Println("Dry run: re-run with -confirm-apply to revert the bridge's most recent apply.")
		return nil
	}
	s, err := openBridgeSession(bridgeAddr)
	if err != nil {
		return err
	}
	defer s.close()
	if err := s.c.RequestRevert(); err != nil {
		return err
	}
	r, err := s.await("revert", "")
	if err != nil {
		return err
	}
	return reportResult(r)
}
