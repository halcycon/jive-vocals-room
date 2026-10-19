package live

import (
	"context"
	"sync"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/live/rt"
	"github.com/linuxmatters/jive-vocals/internal/room"
)

const (
	snapshotInterval = 200 * time.Millisecond
	// Nine 50%-overlapped 8192-point FFT frames (about 0.9 s at 44.1 kHz).
	// A single frame would make every tonal peak trivially "persistent".
	liveWindowFrames = 8192 + 8*4096
)

// captureSource is a native input tap whose real-time callback only writes
// into an analysis ring. Telemetry getters read C atomics.
type captureSource interface {
	Start() error
	Ring() *rt.Ring
	Rate() int
	Quantum() int
	Channels() int
	XRuns() uint64
	Samples() uint64
	Healthy() bool
	Close() error
}

// ringAnalyser is the non-real-time consumer shared by every backend.
type ringAnalyser struct {
	src    captureSource
	label  string
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newRingAnalyser(src captureSource, label string) *ringAnalyser {
	return &ringAnalyser{src: src, label: label}
}

func (a *ringAnalyser) Start(ctx context.Context, cb func(Snapshot)) error {
	if err := a.src.Start(); err != nil {
		return err
	}
	ctx, a.cancel = context.WithCancel(ctx)
	a.wg.Add(1)
	go a.consume(ctx, cb)
	return nil
}

func (a *ringAnalyser) consume(ctx context.Context, cb func(Snapshot)) {
	defer a.wg.Done()
	var acc []float64
	tick := time.NewTicker(snapshotInterval)
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

func (a *ringAnalyser) snapshot(acc *[]float64) Snapshot {
	overruns := DrainRing(a.src.Ring(), acc)
	rate := a.src.Rate()
	ch := a.src.Channels()
	if ch <= 0 {
		ch = a.src.Ring().Channels()
	}
	tel := Telemetry{
		QuantumFrames:    a.src.Quantum(),
		SampleRateHz:     rate,
		Channels:         ch,
		XRuns:            a.src.XRuns(),
		AnalysisOverruns: overruns,
		ProcessedSamples: a.src.Samples(),
		EngineHealthy:    a.src.Healthy(),
	}
	need := liveWindowSamples(ch)
	if len(*acc) < need || rate <= 0 {
		return Snapshot{Telemetry: tel}
	}
	*acc = (*acc)[len(*acc)-need:]
	win := append([]float64(nil), *acc...)
	return SnapshotFromPCM(room.PCM{Samples: win, SampleRate: rate, Channels: ch, Source: a.label}, tel)
}

func liveWindowSamples(channels int) int {
	if channels < 1 {
		channels = 1
	}
	return liveWindowFrames * channels
}

func (a *ringAnalyser) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()
	if a.src != nil {
		err := a.src.Close()
		a.src = nil
		return err
	}
	return nil
}
