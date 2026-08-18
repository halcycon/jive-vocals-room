// Package roomfile adapts the existing FFmpeg decoder to the pure room analyser.
package roomfile

import (
	"fmt"
	"unsafe"

	ffmpeg "github.com/linuxmatters/ffmpeg-statigo"
	"github.com/linuxmatters/jive-vocals/internal/audio"
	"github.com/linuxmatters/jive-vocals/internal/room"
)

func Decode(path string) (room.PCM, error) {
	r, meta, err := audio.OpenAudioFile(path)
	if err != nil {
		return room.PCM{}, err
	}
	defer r.Close()
	pcm := room.PCM{SampleRate: meta.SampleRate, Channels: meta.Channels, Source: path}
	for {
		frame, err := r.ReadFrame()
		if err != nil {
			return room.PCM{}, err
		}
		if frame == nil {
			break
		}
		samples, err := frameSamples(frame)
		if err != nil {
			return room.PCM{}, err
		}
		pcm.Samples = append(pcm.Samples, samples...)
	}
	return pcm, nil
}

func frameSamples(frame *ffmpeg.AVFrame) ([]float64, error) {
	format := ffmpeg.AVSampleFormat(frame.Format())
	n, ch := frame.NbSamples(), frame.ChLayout().NbChannels()
	planar := format == ffmpeg.AVSampleFmtS16P || format == ffmpeg.AVSampleFmtFltp || format == ffmpeg.AVSampleFmtS32P || format == ffmpeg.AVSampleFmtDblp
	out := make([]float64, n*ch)
	for c := 0; c < ch; c++ {
		plane := 0
		if planar {
			plane = c
		}
		ptr := frame.Data().Get(uintptr(plane))
		if ptr == nil {
			return nil, fmt.Errorf("decoder returned nil sample plane")
		}
		planeLength := n * ch
		if planar {
			planeLength = n
		}
		for i := 0; i < n; i++ {
			index := i*ch + c
			src := index
			if planar {
				src = i
			}
			switch format {
			case ffmpeg.AVSampleFmtS16, ffmpeg.AVSampleFmtS16P:
				out[index] = float64(unsafe.Slice((*int16)(ptr), planeLength)[src]) / 32768
			case ffmpeg.AVSampleFmtFlt, ffmpeg.AVSampleFmtFltp:
				out[index] = float64(unsafe.Slice((*float32)(ptr), planeLength)[src])
			case ffmpeg.AVSampleFmtS32, ffmpeg.AVSampleFmtS32P:
				out[index] = float64(unsafe.Slice((*int32)(ptr), planeLength)[src]) / 2147483648
			case ffmpeg.AVSampleFmtDbl, ffmpeg.AVSampleFmtDblp:
				out[index] = unsafe.Slice((*float64)(ptr), planeLength)[src]
			default:
				return nil, fmt.Errorf("unsupported decoded sample format %d", format)
			}
		}
	}
	return out, nil
}
