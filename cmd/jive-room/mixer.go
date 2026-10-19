package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/studiolive"
)

const replayTimeout = 5 * time.Second

func runMixerState(bridgeAddr string, channel int, watch bool) error {
	if channel != 0 {
		if _, ok := studiolive.InputChannelNumber(studiolive.InputChannelName(channel)); !ok {
			return usageError{fmt.Errorf("studiolive input channel %d out of range 1..%d", channel, studiolive.InputChannels)}
		}
	}
	c, err := studiolive.DialBridge(bridgeAddr)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	replayed := make(chan int, 1)
	var ready atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- c.Run(runCtx, studiolive.Events{
			OnReplayed: func(n int) {
				select {
				case replayed <- n:
				default:
				}
			},
			OnUpdate: func(u studiolive.Update) {
				if watch && ready.Load() {
					fmt.Println(formatUpdate(u))
				}
			},
		})
	}()
	select {
	case n := <-replayed:
		fmt.Fprintf(os.Stderr, "bridge %s replayed %d cached states; %d known controls. Read-only: no mixer writes.\n", bridgeAddr, n, c.State.Len())
	case err := <-done:
		return err
	case <-time.After(replayTimeout):
		cancel()
		<-done
		return fmt.Errorf("no state replay from bridge %s within %s (is the DSP-enabled bridge running?)", bridgeAddr, replayTimeout)
	}
	if !watch {
		printMixerState(os.Stdout, c.State, channel)
		cancel()
		return <-done
	}
	ready.Store(true)
	return <-done
}

func formatUpdate(u studiolive.Update) string {
	s := fmt.Sprintf("%s  %-18s value=%.4f raw=%3d", u.Value.UpdatedAt.Format("15:04:05.000"), u.Control, u.Value.Value, u.Value.Raw)
	if cal, ok := studiolive.CalibrationFor(u.Control); ok {
		s += fmt.Sprintf("  ≈ %s (%s)", formatUnit(cal.ToUnit(float64(u.Value.Raw)/255), cal.Unit), calLabel(cal))
	}
	if _, ok := studiolive.GEQBandIndex(u.Control); ok {
		s += fmt.Sprintf("  %+d raw steps from flat", studiolive.RawStepsFromFlat(u.Value.Raw))
	}
	return s
}

func formatUnit(x float64, unit string) string {
	switch {
	case unit == "Hz" && x >= 1000:
		return fmt.Sprintf("%.2f kHz", x/1000)
	case unit == "Hz":
		return fmt.Sprintf("%.0f Hz", x)
	default:
		return fmt.Sprintf("%+.1f %s", x, unit)
	}
}

var channelSections = []struct {
	label, enable, freq, gain, mode, modeLabel string
}{
	{"HPF", "hpf", "hpffreq", "", "", ""},
	{"Low", "eqlow", "eqlowfreq", "eqlowgain", "eqlowshelf", "shelf"},
	{"Mid", "eqmid", "eqmidfreq", "eqmidgain", "eqmidhiq", "hi-Q"},
	{"High", "eqhigh", "eqhighfreq", "eqhighgain", "eqhighshelf", "shelf"},
}

func printMixerState(w io.Writer, s *studiolive.State, channel int) {
	channels := []int{channel}
	if channel == 0 {
		channels = channels[:0]
		for n := 1; n <= studiolive.InputChannels; n++ {
			channels = append(channels, n)
		}
	}
	fmt.Fprintln(w, calibrationBanner())
	for _, n := range channels {
		ch := studiolive.InputChannelName(n)
		fmt.Fprintf(w, "\n%s  (FireWire send %s)\n", strings.ToUpper(ch), sendLabel(s, ch))
		for _, sec := range channelSections {
			fmt.Fprintf(w, "  %-5s %-4s %s", sec.label, onOff(s, ch, sec.enable), describe(s, ch, sec.freq))
			if sec.gain != "" {
				fmt.Fprintf(w, "  %s", describe(s, ch, sec.gain))
			}
			if sec.mode != "" {
				if v, ok := s.Get(studiolive.Control{Channel: ch, Name: sec.mode}); ok && v.On() {
					fmt.Fprintf(w, "  %s", sec.modeLabel)
				}
			}
			fmt.Fprintln(w)
		}
	}
	fmt.Fprintf(w, "\nGEQ  %s  (raw steps from flat; dB per step not calibrated)\n", onOff(s, "geq0", "enable"))
	for i, hz := range studiolive.GEQBands {
		c, _ := studiolive.GEQBandControl(i)
		label := formatUnit(hz, "Hz")
		if v, ok := s.Get(c); ok {
			fmt.Fprintf(w, "  %-9s %+4d", label, studiolive.RawStepsFromFlat(v.Raw))
		} else {
			fmt.Fprintf(w, "  %-9s    ?", label)
		}
		if i%4 == 3 {
			fmt.Fprintln(w)
		}
	}
	fmt.Fprintln(w)
}

func onOff(s *studiolive.State, ch, name string) string {
	v, ok := s.Get(studiolive.Control{Channel: ch, Name: name})
	switch {
	case !ok:
		return "?"
	case v.On():
		return "on"
	default:
		return "off"
	}
}

func sendLabel(s *studiolive.State, ch string) string {
	v, ok := s.Get(studiolive.Control{Channel: ch, Name: "post"})
	switch {
	case !ok:
		return "?"
	case v.On():
		return "post-DSP"
	default:
		return "pre-DSP"
	}
}

func describe(s *studiolive.State, ch, name string) string {
	c := studiolive.Control{Channel: ch, Name: name}
	v, ok := s.Get(c)
	if !ok {
		return "?"
	}
	if cal, ok := studiolive.CalibrationFor(c); ok {
		return fmt.Sprintf("%s (raw %d)", formatUnit(cal.ToUnit(float64(v.Raw)/255), cal.Unit), v.Raw)
	}
	return fmt.Sprintf("raw %d", v.Raw)
}

func calLabel(c studiolive.Calibration) string {
	if c.Verified {
		return "measured"
	}
	return "model, unverified"
}

func calibrationBanner() string {
	if studiolive.InputCalibration["eqmidgain"].Verified {
		return "Engineering values use the measured calibration (" + studiolive.InputCalibration["eqmidgain"].Source + ")."
	}
	return "Engineering values use the session-label model (log frequency, linear gain); not yet verified against the desk."
}
