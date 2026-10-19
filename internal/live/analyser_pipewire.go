//go:build pipewire

package live

import (
	"context"
	"sync"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/live/pipewire"
	"github.com/linuxmatters/jive-vocals/internal/room"
)

type pwAnalyser struct {
	cap    *pipewire.Capture
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func OpenInputAnalyser() (InputAnalyser, error) {
	cap, err := pipewire.NewCapture(DefaultRingFrames, 1)
	if err != nil {
		return nil, err
	}
	return &pwAnalyser{cap: cap}, nil
}

func PipeWireBuilt() bool { return true }

func (a *pwAnalyser) Start(ctx context.Context, cb func(Snapshot)) error {
	if err := a.cap.Start(); err != nil {
		return err
	}
	ctx, a.cancel = context.WithCancel(ctx)
	a.wg.Add(1)
	go a.consume(ctx, cb)
	return nil
}

func (a *pwAnalyser) consume(ctx context.Context, cb func(Snapshot)) {
	defer a.wg.Done()
	var acc []float64
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if cb == nil {
				continue
			}
			cb(a.snapshot(&acc))
		}
	}
}

func (a *pwAnalyser) snapshot(acc *[]float64) Snapshot {
	overruns := DrainRing(a.cap.Ring(), acc)
	rate := a.cap.Rate()
	ch := a.cap.Channels()
	if ch <= 0 {
		ch = a.cap.Ring().Channels()
	}
	tel := Telemetry{
		QuantumFrames:    a.cap.Quantum(),
		SampleRateHz:     rate,
		Channels:         ch,
		XRuns:            a.cap.XRuns(),
		AnalysisOverruns: overruns,
		ProcessedSamples: a.cap.Samples(),
		EngineHealthy:    true,
	}
	need := liveWindowSamples(ch)
	if len(*acc) < need || rate <= 0 {
		return Snapshot{Telemetry: tel}
	}
	win := append([]float64(nil), (*acc)[len(*acc)-need:]...)
	*acc = (*acc)[len(*acc)-need/2:]
	return SnapshotFromPCM(room.PCM{Samples: win, SampleRate: rate, Channels: ch, Source: "pipewire"}, tel)
}

func liveWindowSamples(channels int) int {
	if channels < 1 {
		channels = 1
	}
	return 8192 * channels
}

func (a *pwAnalyser) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()
	if a.cap != nil {
		_ = a.cap.Close()
		a.cap = nil
	}
	return nil
}
