package room

import "math"

// Offline RBJ biquads for test-signal generation and synthetic transfer fixtures.
// This is not the live real-time DSP core and may allocate.
type biquad struct {
	b0, b1, b2, a1, a2 float64
	z1, z2             float64
}

func (b *biquad) process(x float64) float64 {
	y := b.b0*x + b.z1
	b.z1 = b.b1*x - b.a1*y + b.z2
	b.z2 = b.b2*x - b.a2*y
	return y
}

func (b biquad) apply(samples []float64) []float64 {
	out := make([]float64, len(samples))
	flt := b
	for i, x := range samples {
		out[i] = flt.process(x)
	}
	return out
}

func rbjPeaking(sampleRateHz, frequencyHz, q, gainDB float64) biquad {
	w0 := 2 * math.Pi * frequencyHz / sampleRateHz
	cos := math.Cos(w0)
	alpha := math.Sin(w0) / (2 * q)
	a := math.Pow(10, gainDB/40)
	b0 := 1 + alpha*a
	b1 := -2 * cos
	b2 := 1 - alpha*a
	a0 := 1 + alpha/a
	a1 := -2 * cos
	a2 := 1 - alpha/a
	return biquad{b0: b0 / a0, b1: b1 / a0, b2: b2 / a0, a1: a1 / a0, a2: a2 / a0}
}

func rbjHighPass(sampleRateHz, frequencyHz, q float64) biquad {
	w0 := 2 * math.Pi * frequencyHz / sampleRateHz
	cos := math.Cos(w0)
	alpha := math.Sin(w0) / (2 * q)
	b0 := (1 + cos) / 2
	b1 := -(1 + cos)
	b2 := (1 + cos) / 2
	a0 := 1 + alpha
	a1 := -2 * cos
	a2 := 1 - alpha
	return biquad{b0: b0 / a0, b1: b1 / a0, b2: b2 / a0, a1: a1 / a0, a2: a2 / a0}
}

func rbjLowPass(sampleRateHz, frequencyHz, q float64) biquad {
	w0 := 2 * math.Pi * frequencyHz / sampleRateHz
	cos := math.Cos(w0)
	alpha := math.Sin(w0) / (2 * q)
	b0 := (1 - cos) / 2
	b1 := 1 - cos
	b2 := (1 - cos) / 2
	a0 := 1 + alpha
	a1 := -2 * cos
	a2 := 1 - alpha
	return biquad{b0: b0 / a0, b1: b1 / a0, b2: b2 / a0, a1: a1 / a0, a2: a2 / a0}
}

func cascade(samples []float64, filters ...biquad) []float64 {
	out := samples
	for _, f := range filters {
		out = f.apply(out)
	}
	return out
}

func feedforwardComb(samples []float64, delaySamples int, gain float64) []float64 {
	out := make([]float64, len(samples))
	for i, x := range samples {
		out[i] = x
		if i >= delaySamples {
			out[i] += gain * samples[i-delaySamples]
		}
	}
	return out
}
