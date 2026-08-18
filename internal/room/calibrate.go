package room

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

func LoadCalibrationCurve(path string) (CalibrationCurve, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return CalibrationCurve{}, err
	}
	var cal CalibrationCurve
	if err = json.Unmarshal(b, &cal); err != nil {
		return CalibrationCurve{}, err
	}
	if len(cal.Points) < 2 {
		return CalibrationCurve{}, fmt.Errorf("calibration curve %q needs at least two frequency points", path)
	}
	sort.Slice(cal.Points, func(i, j int) bool { return cal.Points[i].FrequencyHz < cal.Points[j].FrequencyHz })
	for _, p := range cal.Points {
		if p.FrequencyHz <= 0 || math.IsNaN(p.LevelDB) || math.IsInf(p.LevelDB, 0) {
			return CalibrationCurve{}, fmt.Errorf("calibration curve %q has an invalid point", path)
		}
	}
	cal.Source = path
	cal.Applied = true
	cal.SPLCalibrated = false
	return cal, nil
}

func ApplyCalibration(m Measurement, cal CalibrationCurve) Measurement {
	m.FineSpectrum = subtractCurve(m.FineSpectrum, cal.Points)
	m.LegacyBands = subtractCurve(m.LegacyBands, cal.Points)
	return m
}

func subtractCurve(spectrum, curve []SpectralPoint) []SpectralPoint {
	out := make([]SpectralPoint, len(spectrum))
	for i, p := range spectrum {
		out[i] = SpectralPoint{FrequencyHz: p.FrequencyHz, LevelDB: finiteDB(p.LevelDB - interpolateLog(curve, p.FrequencyHz))}
	}
	return out
}

func interpolateLog(curve []SpectralPoint, freq float64) float64 {
	if len(curve) == 0 {
		return 0
	}
	if freq <= curve[0].FrequencyHz {
		return curve[0].LevelDB
	}
	last := curve[len(curve)-1]
	if freq >= last.FrequencyHz {
		return last.LevelDB
	}
	for i := 1; i < len(curve); i++ {
		lo, hi := curve[i-1], curve[i]
		if freq > hi.FrequencyHz {
			continue
		}
		span := math.Log(hi.FrequencyHz / lo.FrequencyHz)
		if span == 0 {
			return lo.LevelDB
		}
		t := math.Log(freq/lo.FrequencyHz) / span
		return lo.LevelDB + t*(hi.LevelDB-lo.LevelDB)
	}
	return 0
}
