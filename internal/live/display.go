package live

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/linuxmatters/jive-vocals/internal/room"
)

const (
	DefaultHistory = 72
	DefaultFloorDB = -80.0
	DefaultCeilDB  = 0.0
	peakDecayDB    = 0.8
	spectrumRows   = 8
	bandColWidth   = 4
)

var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// Monitor is the non-RT display state for the live analyser. Freeze and peak
// hold are visual only; they never change audio, PA gain, or DSP coefficients.
type Monitor struct {
	History    []float64
	HistoryMax int
	PeakHold   []float64
	Ref        []room.SpectralPoint
	FloorDB    float64
	CeilDB     float64
}

func NewMonitor() Monitor {
	return Monitor{HistoryMax: DefaultHistory, FloorDB: DefaultFloorDB, CeilDB: DefaultCeilDB}
}

func (m *Monitor) Observe(s Snapshot) {
	if m.HistoryMax <= 0 {
		m.HistoryMax = DefaultHistory
	}
	if m.CeilDB <= m.FloorDB {
		m.FloorDB, m.CeilDB = DefaultFloorDB, DefaultCeilDB
	}
	if s.HasSpectrum && !math.IsInf(s.RMSDB, 0) && !math.IsNaN(s.RMSDB) {
		m.History = append(m.History, s.RMSDB)
		if len(m.History) > m.HistoryMax {
			m.History = m.History[len(m.History)-m.HistoryMax:]
		}
	}
	if len(s.Bands) == 0 {
		return
	}
	if len(m.PeakHold) != len(s.Bands) {
		m.PeakHold = make([]float64, len(s.Bands))
		for i := range m.PeakHold {
			m.PeakHold[i] = math.Inf(-1)
		}
	}
	for i, b := range s.Bands {
		if b.LevelDB > m.PeakHold[i] {
			m.PeakHold[i] = b.LevelDB
			continue
		}
		m.PeakHold[i] -= peakDecayDB
		if m.PeakHold[i] < b.LevelDB {
			m.PeakHold[i] = b.LevelDB
		}
	}
}

func (m *Monitor) Freeze(s Snapshot) {
	m.Ref = append([]room.SpectralPoint(nil), s.Bands...)
}

func (m *Monitor) Reset() {
	m.History = nil
	m.PeakHold = nil
	m.Ref = nil
}

func (m Monitor) HasFreeze() bool { return len(m.Ref) > 0 }

func LoudestBand(bands []room.SpectralPoint) (room.SpectralPoint, bool) {
	var best room.SpectralPoint
	found := false
	for _, b := range bands {
		if !found || b.LevelDB > best.LevelDB {
			best = b
			found = true
		}
	}
	return best, found
}

func BandLabel(hz float64) string {
	if hz < 1000 {
		return fmt.Sprintf("%.0f", math.Round(hz))
	}
	k := hz / 1000
	if math.Abs(k-math.Round(k)) < 0.05 {
		return fmt.Sprintf("%.0fk", math.Round(k))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", k), "0"), ".") + "k"
}

func Sparkline(values []float64, width int) string {
	if width < 8 {
		width = 8
	}
	if len(values) == 0 {
		return strings.Repeat("·", width)
	}
	src := values
	if len(src) > width {
		src = src[len(src)-width:]
	}
	minV, maxV := src[0], src[0]
	for _, v := range src {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	span := maxV - minV
	if span < 1 {
		span = 1
	}
	var b strings.Builder
	for _, v := range src {
		idx := min(max(int((v-minV)/span*float64(len(sparkBlocks)-1)), 0), len(sparkBlocks)-1)
		b.WriteRune(sparkBlocks[idx])
	}
	if n := width - utf8.RuneCountInString(b.String()); n > 0 {
		b.WriteString(strings.Repeat("·", n))
	}
	return b.String()
}

func (m Monitor) Render(s Snapshot, width int) string {
	if width < 40 {
		width = 40
	}
	var b strings.Builder
	qms := 0.0
	if s.SampleRateHz > 0 && s.QuantumFrames > 0 {
		qms = 1000 * float64(s.QuantumFrames) / float64(s.SampleRateHz)
	}
	fmt.Fprintf(&b, "jive-room live analyser  ·  read-only  ·  no playback  ·  no PA gain\n")
	fmt.Fprintf(&b, "rate %d Hz   quantum %d frames (%.2f ms reported)   rms %s   peak %s\n",
		s.SampleRateHz, s.QuantumFrames, qms, formatDB(s.RMSDB), formatDB(s.PeakDB))
	fmt.Fprintf(&b, "xruns %d   analysis drops %d   flatness %.3f", s.XRuns, s.AnalysisOverruns, s.SpectralFlatness)
	if s.TonalHz > 0 {
		fmt.Fprintf(&b, "   tone %.0f Hz / %+.1f dB", s.TonalHz, s.TonalProminenceDB)
	}
	if s.HumFundamentalHz > 0 {
		fmt.Fprintf(&b, "   hum %.0f Hz", s.HumFundamentalHz)
	}
	b.WriteByte('\n')
	if loud, ok := LoudestBand(s.Bands); ok {
		fmt.Fprintf(&b, "loudest band %s (%s dBFS). Levels are relative FFT/dBFS, not SPL.\n", BandLabel(loud.FrequencyHz), formatDB(loud.LevelDB))
	} else if !s.HasSpectrum {
		b.WriteString("waiting for a full analysis window…\n")
	}
	sparkW := min(width-8, m.HistoryMax)
	fmt.Fprintf(&b, "\nRMS  %s\n\n", Sparkline(m.History, sparkW))
	b.WriteString(m.renderSpectrum(s))
	if m.HasFreeze() {
		b.WriteString("frozen reference  ·  Δ is current minus freeze  ·  a cut should go negative on that band\n")
	} else {
		b.WriteString("space freeze a reference spectrum   r reset peak-hold   q quit\n")
		b.WriteString("tune one mixer band at a time: freeze, cut, stop when that Δ matches the intended cut and neighbours hold.\n")
	}
	return b.String()
}

func (m Monitor) renderSpectrum(s Snapshot) string {
	bands := s.Bands
	if len(bands) == 0 {
		return "no 15-band spectrum yet\n"
	}
	floor, ceil := m.FloorDB, m.CeilDB
	rows := spectrumRows
	step := (ceil - floor) / float64(rows)
	var b strings.Builder
	for row := rows - 1; row >= 0; row-- {
		thresh := floor + step*(float64(row)+0.5)
		fmt.Fprintf(&b, "%4.0f ┤", thresh)
		for i, band := range bands {
			peak := math.Inf(-1)
			if i < len(m.PeakHold) {
				peak = m.PeakHold[i]
			}
			ch := spectrumGlyph(band.LevelDB, peak, thresh, step)
			b.WriteString(centerPad(string(ch), bandColWidth))
		}
		b.WriteByte('\n')
	}
	b.WriteString("     └")
	for range bands {
		b.WriteString(strings.Repeat("─", bandColWidth))
	}
	b.WriteByte('\n')
	b.WriteString("      ")
	for _, band := range bands {
		b.WriteString(centerPad(BandLabel(band.FrequencyHz), bandColWidth))
	}
	b.WriteByte('\n')
	if m.HasFreeze() {
		b.WriteString("   Δ  ")
		for i, band := range bands {
			delta := band.LevelDB - refLevel(m.Ref, band.FrequencyHz, i)
			b.WriteString(centerPad(formatDelta(delta), bandColWidth))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func spectrumGlyph(level, peak, thresh, step float64) rune {
	if level >= thresh {
		return '█'
	}
	if peak >= thresh && peak < thresh+step {
		return '•'
	}
	return ' '
}

func refLevel(ref []room.SpectralPoint, hz float64, i int) float64 {
	if i >= 0 && i < len(ref) && math.Abs(ref[i].FrequencyHz-hz) < 1 {
		return ref[i].LevelDB
	}
	for _, p := range ref {
		if math.Abs(p.FrequencyHz-hz) < 1 {
			return p.LevelDB
		}
	}
	return math.NaN()
}

func formatDB(v float64) string {
	if math.IsInf(v, -1) || math.IsNaN(v) {
		return "n/a"
	}
	return fmt.Sprintf("%+.1f dBFS", v)
}

func formatDelta(v float64) string {
	if math.IsNaN(v) {
		return "·"
	}
	if math.Abs(v) < 0.15 {
		return "0"
	}
	return fmt.Sprintf("%+.0f", v)
}

func centerPad(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		var b strings.Builder
		i := 0
		for _, r := range s {
			if i >= width {
				break
			}
			b.WriteRune(r)
			i++
		}
		return b.String()
	}
	pad := width - n
	left := pad / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", pad-left)
}

func FormatPlainLine(s Snapshot) string {
	qms := 0.0
	if s.SampleRateHz > 0 && s.QuantumFrames > 0 {
		qms = 1000 * float64(s.QuantumFrames) / float64(s.SampleRateHz)
	}
	loud := "n/a"
	if b, ok := LoudestBand(s.Bands); ok {
		loud = fmt.Sprintf("%s %s", BandLabel(b.FrequencyHz), formatDB(b.LevelDB))
	}
	engine := "up"
	if !s.EngineHealthy {
		engine = "down"
	}
	return fmt.Sprintf("rate=%d Hz  quantum=%d (%.2f ms)  rms=%s  peak=%s  loudest=%s  flatness=%.3f  tone=%.0f Hz / %+.1f dB  hum=%.0f Hz  xruns=%d  analysis_drops=%d  samples=%d  engine=%s",
		s.SampleRateHz, s.QuantumFrames, qms, formatDB(s.RMSDB), formatDB(s.PeakDB), loud, s.SpectralFlatness, s.TonalHz, s.TonalProminenceDB, s.HumFundamentalHz, s.XRuns, s.AnalysisOverruns, s.ProcessedSamples, engine)
}
