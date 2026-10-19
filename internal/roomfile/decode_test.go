package roomfile

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxmatters/jive-vocals/internal/room"
)

func TestDecodePCM16WAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.wav")
	const rate = 48000
	samples := make([]int16, rate/2)
	for i := range samples {
		samples[i] = int16(4000 * math.Sin(2*math.Pi*1000*float64(i)/rate))
	}
	if err := writeWAV(path, rate, samples); err != nil {
		t.Fatal(err)
	}
	pcm, err := Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	if pcm.SampleRate != rate || pcm.Channels != 1 || len(pcm.Samples) != len(samples) {
		t.Fatalf("decoded metadata/samples: rate=%d channels=%d samples=%d", pcm.SampleRate, pcm.Channels, len(pcm.Samples))
	}
}

func TestDecodeFloat32WAVFromWriteFloatWAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture-f32.wav")
	const rate = 44100
	in := make([]float64, rate/2)
	for i := range in {
		in[i] = 3e-5 * math.Sin(2*math.Pi*440*float64(i)/rate)
	}
	if err := room.WriteFloatWAV(path, room.PCM{Samples: in, SampleRate: rate, Channels: 1}); err != nil {
		t.Fatal(err)
	}
	pcm, err := Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	if pcm.SampleRate != rate || pcm.Channels != 1 || len(pcm.Samples) != len(in) {
		t.Fatalf("rate=%d channels=%d samples=%d", pcm.SampleRate, pcm.Channels, len(pcm.Samples))
	}
	for i, v := range pcm.Samples {
		if math.Abs(v-float64(float32(in[i]))) > 0 {
			t.Fatalf("sample %d: %v != %v (float32 must round-trip exactly)", i, v, in[i])
		}
	}
}

func writeWAV(path string, rate int, samples []int16) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dataBytes := uint32(len(samples) * 2) //nolint:gosec // bounded test fixture
	if _, err = f.Write([]byte("RIFF")); err != nil {
		return err
	}
	for _, v := range []any{uint32(36) + dataBytes, [4]byte{'W', 'A', 'V', 'E'}, [4]byte{'f', 'm', 't', ' '}, uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16), [4]byte{'d', 'a', 't', 'a'}, dataBytes} { //nolint:gosec // fixed 48 kHz fixture rate
		if err = binary.Write(f, binary.LittleEndian, v); err != nil {
			return err
		}
	}
	return binary.Write(f, binary.LittleEndian, samples)
}
