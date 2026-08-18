// Package room contains the non-real-time domain and analysis logic for room calibration.
package room

import "time"

const SchemaVersion = "jive-room-session/v1"

type Session struct {
	SchemaVersion string           `json:"schema_version"`
	VenueLabel    string           `json:"venue_label,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	Device        DeviceMetadata   `json:"device"`
	Microphone    Microphone       `json:"microphone,omitempty"`
	EmptyRoom     Measurement      `json:"empty_room"`
	OccupiedRoom  *Measurement     `json:"occupied_room,omitempty"`
	PARoom        *PAResponse      `json:"pa_room_response,omitempty"`
	Presenter     *PresenterTest   `json:"presenter_test,omitempty"`
	Mixer         MixerCapability  `json:"mixer"`
	SuggestedEQ   []Recommendation `json:"suggested_eq"`
	AppliedEQ     []EQSetting      `json:"operator_applied_eq,omitempty"`
	Verification  *Measurement     `json:"verification,omitempty"`
}

type DeviceMetadata struct {
	Interface  string   `json:"interface,omitempty"`
	SampleRate int      `json:"sample_rate_hz"`
	Channels   int      `json:"channels"`
	ChannelMap []string `json:"channel_map,omitempty"`
}

type Microphone struct {
	Identity        string `json:"identity,omitempty"`
	CalibrationFile string `json:"calibration_file,omitempty"`
}

type Measurement struct {
	Source           string          `json:"source"`
	DurationSeconds  float64         `json:"duration_seconds"`
	BroadbandRMSDB   float64         `json:"broadband_rms_dbfs"`
	PeakDB           float64         `json:"peak_dbfs"`
	SpectralFlatness float64         `json:"spectral_flatness"`
	FineSpectrum     []SpectralPoint `json:"fine_spectrum"`
	LegacyBands      []SpectralPoint `json:"jive_15_bands"`
	TonalCandidates  []TonalPeak     `json:"tonal_candidates,omitempty"`
	Hum              *HumDiagnostic  `json:"hum,omitempty"`
}

type SpectralPoint struct {
	FrequencyHz float64 `json:"frequency_hz"`
	LevelDB     float64 `json:"level_db"`
}

type TonalPeak struct {
	FrequencyHz  float64 `json:"frequency_hz"`
	ProminenceDB float64 `json:"prominence_db"`
	Persistence  float64 `json:"persistence"`
}

type HumDiagnostic struct {
	FundamentalHz float64   `json:"fundamental_hz"`
	HarmonicsHz   []float64 `json:"harmonics_hz"`
	Confidence    float64   `json:"confidence"`
}

type Comparison struct {
	BroadbandDeltaDB float64         `json:"broadband_delta_db"`
	BandDeltas       []SpectralPoint `json:"band_deltas"`
}

type PAResponse struct {
	Method string `json:"method"`
	Status string `json:"status"`
}

type PresenterTest struct {
	OverallMarginDB  float64         `json:"overall_margin_db"`
	BandMargins      []SpectralPoint `json:"band_margins,omitempty"`
	PresenceMarginDB float64         `json:"presence_margin_db"`
}

type Recommendation struct {
	Kind          string  `json:"kind"`
	FrequencyHz   float64 `json:"frequency_hz,omitempty"`
	GainDB        float64 `json:"gain_db,omitempty"`
	Q             float64 `json:"q,omitempty"`
	Reason        string  `json:"reason"`
	Confidence    float64 `json:"confidence"`
	StartingPoint bool    `json:"starting_point"`
}

type EQSetting struct {
	FrequencyHz float64 `json:"frequency_hz"`
	GainDB      float64 `json:"gain_db"`
	Q           float64 `json:"q"`
}
