package main

import (
	"encoding/json"
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
	micCal := fs.String("mic-cal", "", "optional relative microphone calibration JSON (frequency_hz/level_db points). Not an SPL calibration")
	micIdentity := fs.String("mic-identity", "", "optional measurement-microphone identity")
	appliedEQ := fs.String("applied-eq", "", "optional operator-applied EQ JSON array")
	verification := fs.String("verification", "", "optional verification WAV/FLAC captured after applying EQ")
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
		Microphone:    room.Microphone{Identity: *micIdentity},
	}
	if *micCal != "" {
		cal, err := room.LoadCalibrationCurve(*micCal)
		if err != nil {
			return err
		}
		s.Microphone.CalibrationFile = *micCal
		s.Microphone.Calibration = &cal
		s.EmptyRoom = room.ApplyCalibration(s.EmptyRoom, cal)
	}
	s.NoteCapture(room.StageEmpty, s.EmptyRoom.DurationSeconds)
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
		if s.Microphone.Calibration != nil {
			calibrated := room.ApplyCalibration(m, *s.Microphone.Calibration)
			s.OccupiedRoom = &calibrated
		}
		c := room.Compare(s.EmptyRoom, *s.OccupiedRoom)
		comparison = &c
		s.NoteCapture(room.StageOccupied, s.OccupiedRoom.DurationSeconds)
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
		if s.Microphone.Calibration != nil {
			m = room.ApplyCalibration(m, *s.Microphone.Calibration)
		}
		noise := s.EmptyRoom
		if s.OccupiedRoom != nil {
			noise = *s.OccupiedRoom
		}
		presenterTest := room.AnalysePresenter(m, noise)
		s.Presenter = &presenterTest
		s.NoteCapture(room.StagePresenter, m.DurationSeconds)
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
		if pa.MeasuredCapture != nil {
			s.NoteCapture(room.StagePA, pa.MeasuredCapture.DurationSeconds)
		} else {
			s.CompletedStages = append(s.CompletedStages, room.StagePA)
		}
	}
	if *appliedEQ != "" {
		eq, err := loadAppliedEQ(*appliedEQ)
		if err != nil {
			return err
		}
		s.AppliedEQ = eq
		s.CompletedStages = append(s.CompletedStages, room.StageAppliedEQ)
	}
	s.SuggestedEQ = room.RecommendSession(s)
	if *verification != "" {
		p, err := roomfile.Decode(*verification)
		if err != nil {
			return err
		}
		m, err := room.Analyse(p)
		if err != nil {
			return err
		}
		if s.Microphone.Calibration != nil {
			m = room.ApplyCalibration(m, *s.Microphone.Calibration)
		}
		s.Verification = &m
		s.VerificationNotes = room.VerifyRecommendations(s.EmptyRoom, m, s.SuggestedEQ)
		s.NoteCapture(room.StageVerify, m.DurationSeconds)
	}
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

func loadAppliedEQ(path string) ([]room.EQSetting, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var eq []room.EQSetting
	if err = json.Unmarshal(b, &eq); err != nil {
		return nil, err
	}
	return eq, nil
}
