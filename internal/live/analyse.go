package live

import (
	"errors"
	"math"

	"github.com/linuxmatters/jive-vocals/internal/live/rt"
	"github.com/linuxmatters/jive-vocals/internal/room"
)

const DefaultRingFrames = 16384

var ErrPipeWireDisabled = errors.New("jive-room was built without PipeWire capture; rebuild with just build-room-live")

// DrainRing copies pending analysis frames into acc (interleaved float64).
func DrainRing(r *rt.Ring, acc *[]float64) uint64 {
	buf := make([]float32, 2048*r.Channels())
	for {
		n := r.Read(buf)
		if n == 0 {
			break
		}
		samples := buf[:n*r.Channels()]
		for _, v := range samples {
			*acc = append(*acc, float64(v))
		}
	}
	return r.Overruns()
}

func SnapshotFromPCM(p room.PCM, tel Telemetry) Snapshot {
	s := Snapshot{Telemetry: tel, RMSDB: math.Inf(-1), PeakDB: math.Inf(-1)}
	if len(p.Samples) == 0 {
		return s
	}
	s.RMSDB, s.PeakDB = pcmLevels(p.Samples)
	m, err := room.Analyse(p)
	if err != nil {
		return s
	}
	s.HasSpectrum = true
	s.RMSDB = m.BroadbandRMSDB
	s.PeakDB = m.PeakDB
	s.SpectralFlatness = m.SpectralFlatness
	s.Bands = append([]room.SpectralPoint(nil), m.LegacyBands...)
	if len(m.TonalCandidates) > 0 {
		s.TonalHz = m.TonalCandidates[0].FrequencyHz
		s.TonalProminenceDB = m.TonalCandidates[0].ProminenceDB
	}
	if m.Hum != nil {
		s.HumFundamentalHz = m.Hum.FundamentalHz
	}
	return s
}

func pcmLevels(samples []float64) (rmsDB, peakDB float64) {
	var sum, peak float64
	for _, x := range samples {
		sum += x * x
		if a := math.Abs(x); a > peak {
			peak = a
		}
	}
	rms := math.Sqrt(sum / float64(len(samples)))
	return dbFS(rms), dbFS(peak)
}

func dbFS(v float64) float64 {
	if v <= 0 {
		return -140
	}
	return math.Max(-140, 20*math.Log10(v))
}
