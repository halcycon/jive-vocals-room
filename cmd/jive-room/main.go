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
	empty := flag.String("empty", "", "10–20 second empty-room WAV or FLAC")
	occupied := flag.String("occupied", "", "optional occupied-room WAV or FLAC")
	presenter := flag.String("presenter", "", "optional presenter-test WAV or FLAC")
	venue := flag.String("venue", "", "venue or room label")
	out := flag.String("out", "jive-room-session.json", "session JSON path")
	flag.Parse()
	if *empty == "" {
		flag.Usage()
		os.Exit(2)
	}
	emptyPCM, err := roomfile.Decode(*empty)
	fatalIf(err)
	emptyMeasurement, err := room.Analyse(emptyPCM)
	fatalIf(err)
	s := room.Session{SchemaVersion: room.SchemaVersion, VenueLabel: *venue, CreatedAt: time.Now(), Device: room.DeviceMetadata{SampleRate: emptyPCM.SampleRate, Channels: emptyPCM.Channels}, EmptyRoom: emptyMeasurement, Mixer: room.DefaultParametricMixer(), PARoom: &room.PAResponse{Method: "pink_noise_averaging", Status: "not_measured"}}
	var comparison *room.Comparison
	if *occupied != "" {
		p, err := roomfile.Decode(*occupied)
		fatalIf(err)
		m, err := room.Analyse(p)
		fatalIf(err)
		s.OccupiedRoom = &m
		c := room.Compare(s.EmptyRoom, m)
		comparison = &c
	}
	if *presenter != "" {
		p, err := roomfile.Decode(*presenter)
		fatalIf(err)
		m, err := room.Analyse(p)
		fatalIf(err)
		noise := s.EmptyRoom
		if s.OccupiedRoom != nil {
			noise = *s.OccupiedRoom
		}
		presenterTest := room.AnalysePresenter(m, noise)
		s.Presenter = &presenterTest
	}
	s.SuggestedEQ = room.Recommend(s.EmptyRoom, s.OccupiedRoom, s.Mixer)
	fatalIf(room.WriteJSON(*out, s))
	reportPath := (*out)[:len(*out)-len(filepath.Ext(*out))] + ".md"
	fatalIf(os.WriteFile(reportPath, []byte(room.Markdown(s, comparison)), 0o644))
	fmt.Printf("Wrote %s and %s\n", *out, reportPath)
}
func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "jive-room:", err)
		os.Exit(1)
	}
}
