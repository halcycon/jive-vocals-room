package live

import (
	"context"

	"github.com/linuxmatters/jive-vocals/internal/room"
)

type FilterType string

const (
	FilterHighPass FilterType = "high_pass"
	FilterLowPass  FilterType = "low_pass"
	FilterPeaking  FilterType = "peaking"
	FilterNotch    FilterType = "notch"
)

type Filter struct {
	Type                   FilterType
	FrequencyHz, GainDB, Q float64
	Bypass                 bool
}

type Configuration struct {
	SampleRateHz, Channels int
	HardBypass             bool
	Filters                []Filter
}

// Telemetry is fixed-size engine health. Audio xruns and analysis overruns
// are counted separately: an analysis drop is not an audible xrun.
type Telemetry struct {
	QuantumFrames    int
	SampleRateHz     int
	Channels         int
	XRuns            uint64
	AnalysisOverruns uint64
	ProcessedSamples uint64
	CPULoad          float64
	EngineHealthy    bool
}

type Snapshot struct {
	Telemetry
	RMSDB             float64
	PeakDB            float64
	HasSpectrum       bool
	SpectralFlatness  float64
	TonalHz           float64
	TonalProminenceDB float64
	HumFundamentalHz  float64
	// Bands is the Jive 15-band view on the same FFT/dBFS axis as offline analysis.
	// Values are relative, not microphone-calibrated SPL.
	Bands []room.SpectralPoint
}

type Engine interface {
	Start(context.Context, Configuration) error
	Update(Configuration) error
	Telemetry() Telemetry
	HardBypass() error
	Close() error
}

type InputAnalyser interface {
	Start(context.Context, func(Snapshot)) error
	Close() error
}
