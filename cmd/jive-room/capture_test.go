package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxmatters/jive-vocals/internal/live"
	"github.com/linuxmatters/jive-vocals/internal/room"
	"github.com/linuxmatters/jive-vocals/internal/roomfile"
)

func TestRecorderCountsOnlyArmedDropsIncludingFinalBatch(t *testing.T) {
	r := newRecorder(10)
	r.tap(make([]float64, 5), 44100)
	r.observe(live.Snapshot{Telemetry: live.Telemetry{XRuns: 2, AnalysisOverruns: 7}})
	if len(r.samples) != 0 {
		t.Fatal("recorded before arm")
	}
	r.arm()
	r.tap(make([]float64, 6), 44100)
	r.observe(live.Snapshot{Telemetry: live.Telemetry{XRuns: 2, AnalysisOverruns: 7}})
	if r.drops != 0 || r.xruns != 0 {
		t.Fatalf("pre-arm counts leaked: drops %d xruns %d", r.drops, r.xruns)
	}
	r.tap(make([]float64, 6), 44100)
	select {
	case <-r.full:
	default:
		t.Fatal("target reached but not signalled")
	}
	r.observe(live.Snapshot{Telemetry: live.Telemetry{XRuns: 3, AnalysisOverruns: 9}})
	r.observe(live.Snapshot{Telemetry: live.Telemetry{XRuns: 9, AnalysisOverruns: 99}})
	if len(r.samples) != 10 || r.drops != 2 || r.xruns != 1 {
		t.Fatalf("samples %d drops %d xruns %d", len(r.samples), r.drops, r.xruns)
	}
	if err := r.dropError(); err == nil || !strings.Contains(err.Error(), "discarded") {
		t.Fatalf("drops must discard the capture: %v", err)
	}
}

func TestPAPlanEnforcesHeadroomAndSteadyWindow(t *testing.T) {
	spec := room.DefaultPinkSpec()
	if _, err := planPAMeasurement(spec, 44100); err == nil {
		t.Fatal("default 12 dB headroom (-12 dBFS peak) accepted for playback")
	}
	spec.HeadroomDB = minPlaybackHeadroomDB
	p, err := planPAMeasurement(spec, 44100)
	if err != nil {
		t.Fatal(err)
	}
	var peak float64
	for _, v := range p.reference.Samples {
		peak = math.Max(peak, math.Abs(v))
	}
	if 20*math.Log10(peak) > -minPlaybackHeadroomDB+0.01 {
		t.Fatalf("playback peak %.2f dBFS above -%v", 20*math.Log10(peak), minPlaybackHeadroomDB)
	}
	if p.reference.SampleRate != 44100 || p.startFrame < int(0.6*44100) || p.endFrame > len(p.reference.Samples)-int(0.1*44100) {
		t.Fatalf("plan %+v", p.startFrame)
	}
	spec.DurationSeconds = 2
	if _, err := planPAMeasurement(spec, 44100); err == nil {
		t.Fatal("2 s pink leaves too little steady signal")
	}
}

func TestWriteCaptureMetaNotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.wav")
	hot := make([]float64, 44100*2)
	for i := range hot {
		hot[i] = 0.99 * math.Sin(2*math.Pi*440*float64(i)/44100)
	}
	meta, err := writeCapture(path, hot, 44100, CaptureMeta{Stage: room.StageEmpty, XRunsDuringCapture: 1})
	if err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(meta.Notes, "|")
	if !strings.Contains(notes, "clipping") || !strings.Contains(notes, "xruns") || meta.DurationSeconds != 2 {
		t.Fatalf("meta %+v", meta)
	}
	b, err := os.ReadFile(metaPath(path))
	if err != nil {
		t.Fatal(err)
	}
	var back CaptureMeta
	if err := json.Unmarshal(b, &back); err != nil || back.Schema != captureSchema || back.SampleRateHz != 44100 {
		t.Fatalf("sidecar %s %v", b, err)
	}
	if _, err := roomfile.Decode(path); err != nil {
		t.Fatal(err)
	}
	quiet, _ := writeCapture(filepath.Join(dir, "q.wav"), make([]float64, 44100), 44100, CaptureMeta{})
	if !strings.Contains(strings.Join(quiet.Notes, "|"), "converter floor") {
		t.Fatalf("silent capture not flagged: %v", quiet.Notes)
	}
}

func TestCaptureRefusalsHappenBeforeJACK(t *testing.T) {
	cases := [][]string{
		{"-studiolive-capture", "empty"},
		{"-studiolive-capture", "lobby", "-studiolive-channel", "1"},
		{"-studiolive-capture", "empty", "-studiolive-channel", "1", "-capture-seconds", "2"},
		{"-studiolive-capture", "empty", "-studiolive-pa-measure", "-studiolive-channel", "1"},
		{"-studiolive-pa-measure", "-studiolive-channel", "1"},
		{"-studiolive-pa-measure", "-studiolive-channel", "1", "-play-pink-to", "_Ch 15_out"},
	}
	for _, args := range cases {
		if err := run(append(args, "-session-dir", t.TempDir())); !isUsage(err) {
			t.Fatalf("%v: want usage error, got %v", args, err)
		}
	}
	err := run([]string{"-studiolive-pa-measure", "-studiolive-channel", "1", "-play-pink-to", "_Ch 15_out"})
	if err == nil || !strings.Contains(err.Error(), "-confirm-playback") || !strings.Contains(err.Error(), "fader DOWN") {
		t.Fatalf("missing pre-flight checklist: %v", err)
	}
}

func TestSessionDirAnalysis(t *testing.T) {
	dir := t.TempDir()
	const rate = 44100
	noise := make([]float64, rate*12)
	for i := range noise {
		noise[i] = 0.01*math.Sin(2*math.Pi*100*float64(i)/rate) + 0.003*math.Sin(2*math.Pi*1700*float64(i)/rate)
	}
	if err := room.WriteFloatWAV(filepath.Join(dir, "empty.wav"), room.PCM{Samples: noise, SampleRate: rate, Channels: 1}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-session-dir", dir}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"jive-room-session.json", "jive-room-session.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}
