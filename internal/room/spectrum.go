package room

import "math"

const (
	fftSize  = 8192
	frameHop = fftSize / 2
	floorDB  = -140.0
)

func downmix(in []float64, channels int) []float64 {
	n := len(in) / channels
	out := make([]float64, n)
	for i := range n {
		for c := range channels {
			out[i] += in[i*channels+c]
		}
		out[i] /= float64(channels)
	}
	return out
}

func rmsDB(s []float64) float64 {
	var v float64
	for _, x := range s {
		v += x * x
	}
	return db(math.Sqrt(v / float64(len(s))))
}

func peakDB(s []float64) float64 {
	var p float64
	for _, x := range s {
		p = max(p, math.Abs(x))
	}
	return db(p)
}

func db(v float64) float64 {
	if v <= 0 {
		return floorDB
	}
	return max(floorDB, 20*math.Log10(v))
}

func spectra(s []float64, rate int) [][]float64 {
	var out [][]float64
	for start := 0; start+fftSize <= len(s); start += frameHop {
		x := make([]complex128, fftSize)
		for i := range fftSize {
			w := .5 - .5*math.Cos(2*math.Pi*float64(i)/float64(fftSize-1))
			x[i] = complex(s[start+i]*w, 0)
		}
		fft(x)
		p := make([]float64, fftSize/2+1)
		for i := range p {
			p[i] = real(x[i])*real(x[i]) + imag(x[i])*imag(x[i])
		}
		out = append(out, p)
	}
	return out
}

func fft(x []complex128) {
	for i, j := 1, 0; i < len(x); i++ {
		bit := len(x) >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for n := 2; n <= len(x); n <<= 1 {
		wlen := complex(math.Cos(-2*math.Pi/float64(n)), math.Sin(-2*math.Pi/float64(n)))
		for i := 0; i < len(x); i += n {
			w := complex(1, 0)
			for j := 0; j < n/2; j++ {
				u, v := x[i+j], x[i+j+n/2]*w
				x[i+j], x[i+j+n/2] = u+v, u-v
				w *= wlen
			}
		}
	}
}

func averagePower(frames [][]float64) []float64 {
	out := make([]float64, len(frames[0]))
	for _, f := range frames {
		for i, v := range f {
			out[i] += v
		}
	}
	for i := range out {
		out[i] /= float64(len(frames))
	}
	return out
}

func level(power float64) float64 {
	if power <= 0 {
		return floorDB
	}
	return max(floorDB, 10*math.Log10(power))
}

func finiteDB(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return floorDB
	}
	return max(floorDB, min(-floorDB, v))
}

func fractionalOctave(p []float64, rate, divisions int) []SpectralPoint {
	centres := []float64{}
	ratio := math.Pow(2, 1/float64(divisions))
	for f := 25.0; f < float64(rate)/2; f *= ratio {
		centres = append(centres, f)
	}
	return aggregateAtCentres(p, rate, centres)
}

func aggregateAtCentres(p []float64, rate int, centres []float64) []SpectralPoint {
	out := make([]SpectralPoint, 0, len(centres))
	nyquist := float64(rate) / 2
	for i, c := range centres {
		// Keep a centre exactly at Nyquist for the legacy afftdn-compatible view.
		// Its lower half-band is measurable even though the centre itself is the
		// boundary. Centres above Nyquist remain omitted.
		if c > nyquist {
			continue
		}
		lo, hi := bandEdges(centres, i)
		a, b := int(lo*fftSize/float64(rate)), int(math.Ceil(hi*fftSize/float64(rate)))
		a = max(1, a)
		b = min(len(p), max(a+1, b))
		var sum float64
		for _, v := range p[a:b] {
			sum += v
		}
		out = append(out, SpectralPoint{FrequencyHz: c, LevelDB: finiteDB(level(sum / float64(b-a)))})
	}
	return out
}

func bandEdges(centres []float64, i int) (lo, hi float64) {
	c := centres[i]
	if i == 0 {
		if len(centres) > 1 {
			lo = c / math.Sqrt(centres[1]/c)
		} else {
			lo = c / math.Sqrt2
		}
	} else {
		lo = math.Sqrt(centres[i-1] * c)
	}
	if i == len(centres)-1 {
		if i > 0 {
			hi = c * math.Sqrt(c/centres[i-1])
		} else {
			hi = c * math.Sqrt2
		}
	} else {
		hi = math.Sqrt(c * centres[i+1])
	}
	return lo, hi
}

func binFrequency(bin, rate int) float64 {
	return float64(bin) * float64(rate) / fftSize
}
