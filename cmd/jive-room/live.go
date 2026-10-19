package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/linuxmatters/jive-vocals/internal/live"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func liveOptions(backend string, jackSources []string, studioLiveChannel int, jackClient string) (live.InputOptions, error) {
	opts := live.InputOptions{Backend: live.Backend(backend), JACKClientName: jackClient}
	if opts.Backend != live.BackendJACK {
		return opts, nil
	}
	opts.JACKSources = append(opts.JACKSources, jackSources...)
	if studioLiveChannel != 0 {
		suffixes, err := studiolive.CapturePortSuffixes(studioLiveChannel)
		if err != nil {
			return opts, err
		}
		opts.JACKSources = append(opts.JACKSources, suffixes...)
	}
	if len(opts.JACKSources) == 0 {
		return opts, fmt.Errorf("-live-backend jack needs -jack-source or -studiolive-channel")
	}
	return opts, nil
}

func runLive(opts live.InputOptions, plain bool) error {
	analyser, err := live.OpenInputAnalyser(opts)
	if err != nil {
		return err
	}
	defer analyser.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !plain && isTTY() {
		return runLiveTUI(ctx, analyser)
	}
	return runLivePlain(ctx, analyser, opts)
}

func runLivePlain(ctx context.Context, analyser live.InputAnalyser, opts live.InputOptions) error {
	backend := opts.Backend
	if backend == "" {
		backend = live.BackendPipeWire
	}
	fmt.Printf("Read-only %s analyser. No playback, no mixer writes, no PA gain change. Ctrl+C to stop.\n", backend)
	if len(opts.JACKSources) > 0 {
		fmt.Printf("sources: %s\n", strings.Join(opts.JACKSources, ", "))
	}
	if err := analyser.Start(ctx, func(s live.Snapshot) {
		fmt.Println(live.FormatPlainLine(s))
	}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func isTTY() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func runLiveTUI(ctx context.Context, analyser live.InputAnalyser) error {
	p := tea.NewProgram(newLiveModel())
	if err := analyser.Start(ctx, func(s live.Snapshot) {
		p.Send(liveSnapMsg{Snapshot: s})
	}); err != nil {
		return err
	}
	_, err := p.Run()
	return err
}
