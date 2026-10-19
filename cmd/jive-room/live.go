package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/linuxmatters/jive-vocals/internal/live"
)

func runLive(plain bool) error {
	analyser, err := live.OpenInputAnalyser()
	if err != nil {
		return err
	}
	defer analyser.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !plain && isTTY() {
		return runLiveTUI(ctx, analyser)
	}
	return runLivePlain(ctx, analyser)
}

func runLivePlain(ctx context.Context, analyser live.InputAnalyser) error {
	fmt.Println("Read-only PipeWire analyser. No playback, no PA gain change. Ctrl+C to stop.")
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
