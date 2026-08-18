package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/room"
	"github.com/linuxmatters/jive-vocals/internal/roomfile"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "jive-room:", err)
		if isUsage(err) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

type usageError struct{ error }

func isUsage(err error) bool {
	_, ok := err.(usageError)
	return ok
}

func run(args []string) error {
	fs := flag.NewFlagSet("jive-room", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	empty := fs.String("empty", "", "10–20 second empty-room WAV or FLAC")
	occupied := fs.String("occupied", "", "optional occupied-room WAV or FLAC")
	presenter := fs.String("presenter", "", "optional presenter-test WAV or FLAC")
	paReference := fs.String("pa-reference", "", "reference WAV/FLAC for PA/room response, typically the generated pink-noise file")
	paMeasured := fs.String("pa-measured", "", "microphone capture of the test signal through the PA")
	generatePink := fs.String("generate-pink", "", "write a band-limited pink-noise WAV and exit; never starts playback or raises PA gain")
	venue := fs.String("venue", "", "venue or room label")
	out := fs.String("out", "jive-room-session.json", "session JSON path")
	pink := room.DefaultPinkSpec()
	fs.IntVar(&pink.SampleRateHz, "pink-rate", pink.SampleRateHz, "generated pink-noise sample rate in Hz")
	fs.Float64Var(&pink.DurationSeconds, "pink-duration", pink.DurationSeconds, "generated pink-noise duration in seconds")
	fs.Float64Var(&pink.HeadroomDB, "pink-headroom-db", pink.HeadroomDB, "generated pink-noise peak headroom in dBFS")
	fs.Float64Var(&pink.FadeInSeconds, "pink-fade-in", pink.FadeInSeconds, "generated pink-noise fade-in seconds")
	fs.Float64Var(&pink.FadeOutSeconds, "pink-fade-out", pink.FadeOutSeconds, "generated pink-noise fade-out seconds")
	fs.Int64Var(&pink.Seed, "pink-seed", pink.Seed, "generated pink-noise RNG seed")
	fs.Float64Var(&pink.HighPassHz, "pink-highpass-hz", pink.HighPassHz, "generated pink-noise high-pass in Hz")
	fs.Float64Var(&pink.LowPassHz, "pink-lowpass-hz", pink.LowPassHz, "generated pink-noise low-pass in Hz")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if *generatePink != "" {
		if _, err := room.WritePinkWAV(*generatePink, pink); err != nil {
			return err
		}
		fmt.Printf("Wrote %s and %s\n", *generatePink, room.SignalSpecPath(*generatePink))
		fmt.Println("This is a test signal only. Play it through the PA at a safe level you set. jive-room never starts playback or raises PA gain.")
		return nil
	}
	if *empty == "" {
		fs.Usage()
		return usageError{fmt.Errorf("flag -empty is required unless -generate-pink is set")}
	}
	if (*paReference == "") != (*paMeasured == "") {
		return fmt.Errorf("pa response needs both -pa-reference and -pa-measured; jive-room will not play the test signal or raise PA gain")
	}
	emptyPCM, err := roomfile.Decode(*empty)
	if err != nil {
		return err
	}
	emptyMeasurement, err := room.Analyse(emptyPCM)
	if err != nil {
		return err
	}
	s := room.Session{
		SchemaVersion: room.SchemaVersion,
		VenueLabel:    *venue,
		CreatedAt:     time.Now(),
		Device:        room.DeviceMetadata{SampleRate: emptyPCM.SampleRate, Channels: emptyPCM.Channels},
		EmptyRoom:     emptyMeasurement,
		Mixer:         room.DefaultParametricMixer(),
		PARoom:        room.UnmeasuredPAResponse(),
	}
	var comparison *room.Comparison
	if *occupied != "" {
		p, err := roomfile.Decode(*occupied)
		if err != nil {
			return err
		}
		m, err := room.Analyse(p)
		if err != nil {
			return err
		}
		s.OccupiedRoom = &m
		c := room.Compare(s.EmptyRoom, m)
		comparison = &c
	}
	if *presenter != "" {
		p, err := roomfile.Decode(*presenter)
		if err != nil {
			return err
		}
		m, err := room.Analyse(p)
		if err != nil {
			return err
		}
		noise := s.EmptyRoom
		if s.OccupiedRoom != nil {
			noise = *s.OccupiedRoom
		}
		presenterTest := room.AnalysePresenter(m, noise)
		s.Presenter = &presenterTest
	}
	if *paReference != "" {
		ref, err := roomfile.Decode(*paReference)
		if err != nil {
			return err
		}
		meas, err := roomfile.Decode(*paMeasured)
		if err != nil {
			return err
		}
		pa, err := room.AnalysePAResponse(ref, meas, room.DefaultResponseAnalysisConfig())
		if err != nil {
			return err
		}
		spec, err := room.LoadTestSignalSpec(*paReference)
		if err != nil {
			return err
		}
		if spec != nil {
			pa.TestSignal = spec
		} else {
			pa.TestSignal = &pink
		}
		s.PARoom = &pa
	}
	s.SuggestedEQ = room.Recommend(s.EmptyRoom, s.OccupiedRoom, s.PARoom, s.Mixer)
	if err = room.WriteJSON(*out, s); err != nil {
		return err
	}
	reportPath := (*out)[:len(*out)-len(filepath.Ext(*out))] + ".md"
	if err = os.WriteFile(reportPath, []byte(room.Markdown(s, comparison)), 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote %s and %s\n", *out, reportPath)
	return nil
}
