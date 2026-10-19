package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/linuxmatters/jive-vocals/internal/live"
	"github.com/linuxmatters/jive-vocals/internal/room"
	"github.com/linuxmatters/jive-vocals/internal/roomfile"
	"github.com/linuxmatters/jive-vocals/internal/studiolive"
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
	var u usageError
	return errors.As(err, &u)
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
	liveInput := fs.Bool("live", false, "read-only live input analyser TUI; never plays audio or raises PA gain")
	livePlain := fs.Bool("live-plain", false, "with -live, print one status line instead of the spectrum TUI")
	liveBackend := fs.String("live-backend", string(live.BackendPipeWire), "live capture backend: pipewire or jack")
	var jackSources stringList
	fs.Var(&jackSources, "jack-source", "with -live-backend jack, a physical capture port name or unique suffix (repeatable)")
	studioLiveChannel := fs.Int("studiolive-channel", 0, "StudioLive 16.0.2 input channel 1-12 to analyse over JACK (implies -live -live-backend jack)")
	jackClient := fs.String("jack-client", live.DefaultJACKClientName, "JACK client name")
	mixerState := fs.Bool("studiolive-state", false, "print StudioLive HPF/EQ/GEQ state read from the bridge (read-only), then exit")
	mixerWatch := fs.Bool("studiolive-watch", false, "stream StudioLive control changes read from the bridge (read-only) until Ctrl+C")
	bridgeAddr := fs.String("studiolive-bridge", studiolive.DefaultBridgeAddr, "bridge DSP state address")
	ringOut := fs.Bool("studiolive-ringout", false, "read-only ring-out assistant on -studiolive-channel over JACK; proposes bounded cuts, never writes")
	ringOutFile := fs.String("ringout-file", "", "run the ring-out detector over a WAV/FLAC recording and print candidates (offline)")
	captureStage := fs.String("studiolive-capture", "", "record a stage (empty, occupied, presenter, verification) from -studiolive-channel over JACK into -session-dir; read-only")
	captureSeconds := fs.Float64("capture-seconds", 15, "stage capture length in seconds (5–60; 10–20 recommended)")
	sessionDir := fs.String("session-dir", "", "directory for stage captures; for analysis, fills unset -empty/-occupied/-presenter/-pa-*/-verification from it")
	paMeasure := fs.Bool("studiolive-pa-measure", false, "play pink noise to -play-pink-to and record -studiolive-channel (the measurement mic); needs -confirm-playback")
	playPinkTo := fs.String("play-pink-to", "", "JACK physical playback port or unique suffix for PA measurement, e.g. '_Ch 15_out'")
	confirmPlayback := fs.Bool("confirm-playback", false, "confirm that the desk is set up for operator-started test-signal playback")
	profileSave := fs.String("studiolive-profile-save", "", "save the desk's HPF/EQ (and GEQ when no -studiolive-channel) to a profile JSON; read-only")
	profileLabel := fs.String("profile-label", "", "label stored in a saved profile")
	profilePush := fs.String("studiolive-profile-push", "", "show how a profile differs from the desk; with -confirm-apply, write it through the bridge")
	undo := fs.Bool("studiolive-undo", false, "with -confirm-apply, revert the bridge's most recent apply")
	confirmApply := fs.Bool("confirm-apply", false, "confirm writing to the desk through the bridge (snapshot and undo included)")
	ringOutSubmit := fs.Bool("ringout-submit", false, "with -studiolive-ringout, hold the top channel-mid proposal pending for APPLY in the ROOM tab")
	allowUnverified := fs.Bool("allow-unverified-calibration", false, "let -ringout-submit use the unverified EQ calibration model (operator takes responsibility)")
	daemonMode := fs.Bool("studiolive-daemon", false, "run headless for the ROOM tab: operator commands arrive through the bridge; never writes to the desk")
	home, _ := os.UserHomeDir()
	sessionsRoot := fs.String("sessions-root", filepath.Join(home, "studiolive", "sessions"), "daemon: directory for timestamped session folders")
	profilesDir := fs.String("profiles-dir", filepath.Join(home, "studiolive", "profiles"), "daemon: directory for saved desk profiles")
	liveAdviceFile := fs.String("live-advice-file", "", "run the live recommender over a WAV/FLAC voice recording and print its advice (offline)")
	calibrate := fs.Bool("studiolive-calibrate", false, "measure channel HPF/EQ curves on a mono -studiolive-channel looped through its FireWire return (needs -confirm-apply -confirm-playback)")
	calibrationPath := fs.String("studiolive-calibration", filepath.Join(home, "studiolive", "calibration.json"), "measured calibration file; loaded when present, written by -studiolive-calibrate")
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
	if *mixerState || *mixerWatch {
		return runMixerState(*bridgeAddr, *studioLiveChannel, *mixerWatch)
	}
	if *ringOutFile != "" {
		return runRingOutFile(os.Stdout, *ringOutFile)
	}
	if *captureStage != "" || *paMeasure {
		return runStudioLiveCapture(fs, captureArgs{
			stage: *captureStage, pa: *paMeasure, seconds: *captureSeconds, dir: *sessionDir,
			sink: *playPinkTo, confirmed: *confirmPlayback, pink: pink, bridge: *bridgeAddr,
			channel: *studioLiveChannel, sources: jackSources, client: *jackClient,
		})
	}
	if *sessionDir != "" {
		sessionDirInputs(*sessionDir, empty, occupied, presenter, paReference, paMeasured, verification)
		if !flagSet(fs, "out") {
			*out = filepath.Join(*sessionDir, "jive-room-session.json")
		}
	}
	if *calibrate {
		return runCalibrate(*bridgeAddr, *studioLiveChannel, *jackClient, *calibrationPath, *confirmApply && *confirmPlayback, os.Stdout)
	}
	if loaded, err := studiolive.LoadCalibration(*calibrationPath); err != nil {
		return fmt.Errorf("calibration %s: %w", *calibrationPath, err)
	} else if loaded && (*daemonMode || *ringOut) {
		fmt.Fprintf(os.Stderr, "using measured calibration %s\n", *calibrationPath)
	}
	if *daemonMode {
		return runDaemon(daemonConfig{
			bridge: *bridgeAddr, sessionsRoot: *sessionsRoot, profilesDir: *profilesDir, playSink: *playPinkTo,
			captureSeconds: *captureSeconds, pink: pink, jackClient: *jackClient, sources: jackSources,
		})
	}
	if *liveAdviceFile != "" {
		return runLiveAdviceFile(os.Stdout, *liveAdviceFile)
	}
	if *profileSave != "" {
		return runProfileSave(*bridgeAddr, *profileSave, *studioLiveChannel, *profileLabel)
	}
	if *profilePush != "" {
		return runProfilePush(*bridgeAddr, *profilePush, *confirmApply)
	}
	if *undo {
		return runUndo(*bridgeAddr, *confirmApply)
	}
	if *ringOut {
		opts, err := liveOptions(string(live.BackendJACK), jackSources, *studioLiveChannel, *jackClient)
		if err != nil {
			return usageError{err}
		}
		return runRingOutLive(opts, *studioLiveChannel, *bridgeAddr, ringOutSubmitMode{enabled: *ringOutSubmit, allowUnverified: *allowUnverified})
	}
	if *studioLiveChannel != 0 || len(jackSources) > 0 {
		*liveInput = true
		*liveBackend = string(live.BackendJACK)
	}
	if *liveInput {
		opts, err := liveOptions(*liveBackend, jackSources, *studioLiveChannel, *jackClient)
		if err != nil {
			return usageError{err}
		}
		return runLive(opts, *livePlain)
	}
	if *empty == "" {
		fs.Usage()
		return usageError{fmt.Errorf("flag -empty is required unless -generate-pink or -live is set")}
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
		measM, err := room.Analyse(meas)
		if err != nil {
			return err
		}
		if s.Microphone.Calibration != nil {
			measM = room.ApplyCalibration(measM, *s.Microphone.Calibration)
		}
		room.GatePAResponse(&pa, measM, s.EmptyRoom, room.StageEmpty)
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
