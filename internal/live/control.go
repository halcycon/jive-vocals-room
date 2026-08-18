// Package live defines the control-plane boundary for the future native PipeWire engine.
// It deliberately contains no audio transport implementation.
package live

import "context"

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
type Telemetry struct {
	QuantumFrames    int
	SampleRateHz     int
	XRuns            uint64
	ProcessedSamples uint64
	CPULoad          float64
	EngineHealthy    bool
}

type Engine interface {
	Start(context.Context, Configuration) error
	Update(Configuration) error
	Telemetry() Telemetry
	HardBypass() error
	Close() error
}

type InputAnalyser interface {
	Start(context.Context, func(Telemetry)) error
	Close() error
}
