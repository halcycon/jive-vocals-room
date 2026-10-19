package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/feedback"
	"github.com/linuxmatters/jive-vocals/internal/live"
	"github.com/linuxmatters/jive-vocals/internal/liverec"
	"github.com/linuxmatters/jive-vocals/internal/roomfile"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

const (
	ringOutUISlots       = 4
	ringOutPublishPeriod = 500 * time.Millisecond
	ringOutHeartbeat     = 5 * time.Second
)

func formatCandidate(c feedback.Candidate) string {
	kind := "ring"
	if c.Harmonic {
		kind = "harmonic, likely source"
	}
	trend := "steady"
	if c.Growing {
		trend = fmt.Sprintf("growing %+.0f dB/s", c.GrowthDBPerSecond)
	}
	return fmt.Sprintf("%s  %.0f dBFS  +%.0f dB above neighbours  %s  %.1f s  conf %.2f  [%s]",
		formatUnit(c.FrequencyHz, "Hz"), c.LevelDBFS, c.ProminenceDB, trend, c.DurationSeconds, c.Confidence, kind)
}

func formatProposals(ps []studiolive.Proposal) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		s := p.Summary
		switch {
		case !p.Mappable:
			s += " (raw mapping not calibrated)"
		case !p.Verified:
			s += " (unverified model)"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "  ·  ")
}

func candidateKey(cs []feedback.Candidate) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "%d:%.0f:%t:%t;", c.ID, c.FrequencyHz, c.Growing, c.Harmonic)
	}
	return b.String()
}

// ringOutReport prints candidates and, for confident rings, bounded proposals.
func ringOutReport(w io.Writer, cs []feedback.Candidate, channel int, st *studiolive.State) {
	if len(cs) == 0 {
		fmt.Fprintln(w, "  no ringing candidates")
		return
	}
	for _, c := range cs {
		fmt.Fprintf(w, "  %s\n", formatCandidate(c))
		if c.Harmonic {
			continue
		}
		ps := studiolive.RingOutProposals(c.FrequencyHz, channel, st)
		fmt.Fprintf(w, "    propose: %s\n", formatProposals(ps))
		for _, p := range ps {
			for _, n := range p.Notes {
				fmt.Fprintf(w, "      - %s\n", n)
			}
		}
	}
}

func runRingOutFile(w io.Writer, path string) error {
	pcm, err := roomfile.Decode(path)
	if err != nil {
		return err
	}
	d, err := feedback.New(pcm.SampleRate)
	if err != nil {
		return err
	}
	mono := downmix(pcm.Samples, pcm.Channels)
	step := pcm.SampleRate / 2
	last := ""
	fmt.Fprintf(w, "Ring-out evaluation of %s (%d Hz, %.1f s). Offline; no mixer access.\n", path, pcm.SampleRate, float64(len(mono))/float64(pcm.SampleRate))
	for i := 0; i < len(mono); i += step {
		d.Push(mono[i:min(len(mono), i+step)])
		cs := d.Candidates()
		if key := candidateKey(cs); key != last {
			last = key
			fmt.Fprintf(w, "t=%5.1f s\n", float64(min(len(mono), i+step))/float64(pcm.SampleRate))
			ringOutReport(w, cs, 0, nil)
		}
	}
	return nil
}

func downmix(in []float64, channels int) []float64 {
	if channels <= 1 {
		return in
	}
	out := make([]float64, len(in)/channels)
	for i := range out {
		var s float64
		for c := range channels {
			s += in[i*channels+c]
		}
		out[i] = s / float64(channels)
	}
	return out
}

type ringOutPublisher struct {
	bridge *studiolive.BridgeClient
	last   time.Time
}

func (p *ringOutPublisher) publish(status string, cs []feedback.Candidate, channel int, st *studiolive.State, force bool) {
	if p.bridge == nil || (!force && time.Since(p.last) < ringOutPublishPeriod) {
		return
	}
	p.last = time.Now()
	_ = p.bridge.PublishUI("/dsp/ui/ringout/status", status)
	_ = p.bridge.PublishUI("/dsp/ui/ringout/updated", p.last.Format("15:04:05"))
	for i := range ringOutUISlots {
		cand, prop := "", ""
		if i < len(cs) {
			cand = formatCandidate(cs[i])
			if !cs[i].Harmonic {
				prop = formatProposals(studiolive.RingOutProposals(cs[i].FrequencyHz, channel, st))
			}
		}
		_ = p.bridge.PublishUI(fmt.Sprintf("/dsp/ui/ringout/candidate/%d", i+1), cand)
		_ = p.bridge.PublishUI(fmt.Sprintf("/dsp/ui/ringout/proposal/%d", i+1), prop)
	}
}

type ringOutSubmitMode struct {
	enabled, allowUnverified bool
}

const ringOutSubmitInterval = 2 * time.Second

// ringOutSubmitter holds the most confident ring's channel-mid proposal
// pending on the bridge, so the operator can press APPLY in the ROOM tab.
type ringOutSubmitter struct {
	mode       ringOutSubmitMode
	bridge     *studiolive.BridgeClient
	last       string
	lastAt     time.Time
	lastRefuse string
}

func (s *ringOutSubmitter) consider(cs []feedback.Candidate, channel int, st *studiolive.State) {
	if !s.mode.enabled || s.bridge == nil {
		return
	}
	for _, c := range cs {
		if c.Harmonic || c.Confidence < 0.5 {
			continue
		}
		for _, p := range studiolive.RingOutProposals(c.FrequencyHz, channel, st) {
			if p.Target != studiolive.TargetChannelMid {
				continue
			}
			w, err := studiolive.ProposalFromRingOut(p, s.mode.allowUnverified, time.Now())
			if err != nil {
				if msg := err.Error(); msg != s.lastRefuse {
					s.lastRefuse = msg
					fmt.Printf("  not submitted for APPLY: %v\n", err)
				}
				return
			}
			if p.Summary == s.last || time.Since(s.lastAt) < ringOutSubmitInterval {
				return
			}
			if err := s.bridge.SubmitProposal(w); err != nil {
				fmt.Printf("  submit failed: %v\n", err)
				return
			}
			s.last, s.lastAt = p.Summary, time.Now()
			fmt.Printf("  submitted %q for APPLY in the ROOM tab (bridge re-checks: cuts only, desk quiet, snapshot + undo)\n", p.Summary)
			return
		}
	}
}

func runRingOutLive(opts live.InputOptions, channel int, bridgeAddr string, submit ringOutSubmitMode) error {
	if channel == 0 {
		return usageError{fmt.Errorf("-studiolive-ringout needs -studiolive-channel 1..%d", studiolive.InputChannels)}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var bc *studiolive.BridgeClient
	if c, err := studiolive.DialBridge(bridgeAddr); err != nil {
		fmt.Fprintf(os.Stderr, "bridge %s unavailable (%v); continuing without mixer state or UI\n", bridgeAddr, err)
	} else {
		bc = c
		runDone := make(chan struct{})
		defer func() {
			stop()
			select {
			case <-runDone:
			case <-time.After(time.Second):
			}
			_ = bc.Close()
		}()
		go func() {
			defer close(runDone)
			_ = bc.Run(ctx, studiolive.Events{OnResult: func(r studiolive.ApplyResult) {
				if r.Action != "submit" {
					fmt.Printf("  bridge: %s %s %s %s\n", r.Action, r.ID, r.Status, r.Reason)
				}
			}})
		}()
	}
	fmt.Printf("Monitor on CH%d (%s): ring-out candidates and live recommendations. Read-only: no playback, no mixer writes, no gain changes.\n", channel, strings.Join(opts.JACKSources, ", "))
	fmt.Println("Raise gain on the desk slowly yourself; stop as soon as anything rings.")
	return runMonitor(ctx, opts, channel, bc, submit, nil, os.Stdout)
}

// runMonitor analyses one channel until ctx ends: ring-out candidates with
// bounded proposals, and slow display-only live recommendations. bc may be nil
// (no mixer state, no UI). It never writes to the desk; -ringout-submit only
// holds a proposal pending on the bridge for the operator's APPLY.
// auto, when non-nil, may request guarded automatic cuts from the bridge.
func runMonitor(ctx context.Context, opts live.InputOptions, channel int, bc *studiolive.BridgeClient, submit ringOutSubmitMode, auto *autoCutter, out io.Writer) error {
	var st *studiolive.State
	pub := &ringOutPublisher{}
	sub := &ringOutSubmitter{mode: submit}
	if bc != nil {
		st, pub.bridge, sub.bridge = bc.State, bc, bc
	}
	var det *feedback.Detector
	var rec *liverec.Recommender
	opts.SampleTap = func(mono []float64, rate int) {
		if det == nil || det.SampleRate() != rate {
			det, _ = feedback.New(rate)
			rec, _ = liverec.New(rate)
		}
		if det != nil {
			det.Push(mono)
		}
		if rec != nil {
			rec.Push(mono)
		}
	}
	analyser, err := live.OpenInputAnalyser(opts)
	if err != nil {
		return err
	}
	defer analyser.Close()

	last, lastPrint, lastLive, lastRecs := "", time.Time{}, time.Time{}, ""
	if err := analyser.Start(ctx, func(s live.Snapshot) {
		var cs []feedback.Candidate
		if det != nil {
			cs = det.Candidates()
		}
		status := fmt.Sprintf("CH%d  %d Hz  rms %s  xruns %d  engine %s", channel, s.SampleRateHz, formatDBFS(s.RMSDB), s.XRuns, map[bool]string{true: "up", false: "down"}[s.EngineHealthy])
		key := candidateKey(cs)
		changed := key != last
		if changed || time.Since(lastPrint) >= ringOutHeartbeat {
			last, lastPrint = key, time.Now()
			fmt.Fprintf(out, "%s  %s\n", time.Now().Format("15:04:05"), status)
			ringOutReport(out, cs, channel, st)
		}
		pub.publish(status, cs, channel, st, changed)
		sub.consider(cs, channel, st)
		if auto != nil && bc != nil {
			if w, reason, ok := auto.decide(cs, channel, st, time.Now()); ok {
				if err := bc.AutoApply(w); err != nil {
					reason = "automatic cut not sent: " + err.Error()
				} else {
					reason = "requested " + w.Summary + " (bridge re-checks; UNDO reverts)"
				}
				_, _ = auto.note(reason)
				fmt.Fprintf(out, "  %s\n", reason)
				_ = bc.PublishUI("/dsp/ui/auto/last", time.Now().Format("15:04:05")+" "+reason)
			} else if msg, ok := auto.note(reason); ok {
				fmt.Fprintf(out, "  auto: %s\n", msg)
				_ = bc.PublishUI("/dsp/ui/auto/last", time.Now().Format("15:04:05")+" "+msg)
			}
		}
		if rec != nil && time.Since(lastLive) >= liveEvalPeriod {
			lastLive = time.Now()
			recs := rec.Recommendations(deskFor(st, channel))
			if k := liveKey(recs); k != lastRecs {
				lastRecs = k
				reportLive(out, rec.Status(), recs)
			}
			pub.publishLive(rec.Status(), recs, channel)
		}
	}); err != nil {
		return err
	}
	<-ctx.Done()
	pub.publish("monitor stopped", nil, channel, st, true)
	pub.publishLive(liverec.Status{}, nil, 0)
	return nil
}

func formatDBFS(v float64) string {
	if math.IsInf(v, -1) || math.IsNaN(v) {
		return "-- dBFS"
	}
	if v <= -140 {
		return "-inf dBFS"
	}
	return fmt.Sprintf("%.1f dBFS", v)
}
