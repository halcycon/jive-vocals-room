package main

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/liverec"
	"github.com/linuxmatters/jive-vocals/internal/roomfile"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

const (
	liveEvalPeriod = time.Second
	liveUISlots    = 5
	// deskCutNoticeDB: an existing EQ cut at least this deep within 1/3 octave
	// suppresses the same advice.
	deskCutNoticeDB = -1.5
)

// deskFor reads the channel's HPF/EQ through the (unverified) session-label
// model so advice the desk already follows is not repeated.
func deskFor(st *studiolive.State, channel int) liverec.Desk {
	if st == nil || channel == 0 {
		return liverec.Desk{}
	}
	ch := studiolive.InputChannelName(channel)
	get := func(name string) (studiolive.Value, bool) {
		return st.Get(studiolive.Control{Channel: ch, Name: name})
	}
	return liverec.Desk{
		HPFHz: func() (float64, bool) {
			on, ok1 := get("hpf")
			f, ok2 := get("hpffreq")
			if !ok1 || !ok2 || !on.On() {
				return 0, false
			}
			return studiolive.InputCalibration["hpffreq"].ToUnit(f.Value), true
		},
		CutNear: func(hz float64) bool {
			for _, band := range []string{"low", "mid", "high"} {
				on, ok1 := get("eq" + band)
				f, ok2 := get("eq" + band + "freq")
				g, ok3 := get("eq" + band + "gain")
				if !ok1 || !ok2 || !ok3 || !on.On() {
					continue
				}
				fc := studiolive.InputCalibration["eq"+band+"freq"].ToUnit(f.Value)
				gain := studiolive.InputCalibration["eq"+band+"gain"].ToUnit(g.Value)
				if gain <= deskCutNoticeDB && math.Abs(math.Log2(fc/hz)) <= 1.0/3 {
					return true
				}
			}
			return false
		},
	}
}

func liveKey(recs []liverec.Recommendation) string {
	var b strings.Builder
	for _, r := range recs {
		fmt.Fprintf(&b, "%s@%.0f;", r.Kind, r.FrequencyHz)
	}
	return b.String()
}

func liveStatusLine(s liverec.Status) string {
	if !s.Ready {
		return fmt.Sprintf("live advice: listening (%.1f / %.0f s of active signal)", s.ActiveSeconds, liverec.MinActiveSeconds)
	}
	return fmt.Sprintf("live advice: %.0f s of active signal; floor %.0f dBFS; voice assumed; pre-DSP send, so desk EQ is not in this signal", s.ActiveSeconds, s.FloorDBFS)
}

func reportLive(w io.Writer, s liverec.Status, recs []liverec.Recommendation) {
	fmt.Fprintf(w, "  %s\n", liveStatusLine(s))
	if s.Ready && len(recs) == 0 {
		fmt.Fprintln(w, "    no live recommendations")
	}
	for _, r := range recs {
		fmt.Fprintf(w, "    - %s (%s)\n", r.Summary, r.Evidence)
	}
}

func runLiveAdviceFile(w io.Writer, path string) error {
	pcm, err := roomfile.Decode(path)
	if err != nil {
		return err
	}
	r, err := liverec.New(pcm.SampleRate)
	if err != nil {
		return err
	}
	mono := downmix(pcm.Samples, pcm.Channels)
	var recs []liverec.Recommendation
	for i := 0; i < len(mono); i += pcm.SampleRate {
		r.Push(mono[i:min(len(mono), i+pcm.SampleRate)])
		recs = r.Recommendations(liverec.Desk{})
	}
	fmt.Fprintf(w, "Live advice for %s (%d Hz, %.1f s). Offline; desk settings not consulted.\n", path, pcm.SampleRate, float64(len(mono))/float64(pcm.SampleRate))
	reportLive(w, r.Status(), recs)
	return nil
}

func (p *ringOutPublisher) publishLive(s liverec.Status, recs []liverec.Recommendation, channel int) {
	if p.bridge == nil {
		return
	}
	status := "live advice: monitor stopped"
	if channel != 0 {
		status = fmt.Sprintf("CH%d %s", channel, liveStatusLine(s))
	}
	_ = p.bridge.PublishUI("/dsp/ui/live/status", status)
	for i := range liveUISlots {
		line := ""
		if i < len(recs) {
			line = fmt.Sprintf("%s (%s)", recs[i].Summary, recs[i].Evidence)
		}
		_ = p.bridge.PublishUI(fmt.Sprintf("/dsp/ui/live/%d", i+1), line)
	}
}
