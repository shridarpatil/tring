//go:build e2e

// Package e2e drives the real Tring stack (api, gateway, FreeSWITCH) the way a
// CRM, an agent's browser and a carrier would, and checks who hears whom. Each
// party plays its own tone; receivers tell sources apart by detecting it, which
// survives FreeSWITCH's transcoding where a payload marker wouldn't.
package e2e

import "math"

// The tone each source plays.
const (
	FreqCustomer = 300.0
	FreqAgent1   = 440.0
	FreqAgent2   = 660.0
	FreqHold     = 880.0
)

// Freqs are the tones a receiver tells apart.
var Freqs = []float64{FreqCustomer, FreqAgent1, FreqAgent2, FreqHold}

const (
	toneAmplitude = 8000
	// silenceRMS: a window quieter than this holds no tone ("hears nothing").
	silenceRMS = 200
	// dominance: the strongest tone must beat the next by this factor.
	dominance = 10
)

// Tone returns samples of a sine at freq, continuing from *phase (radians) so
// consecutive frames join without a click.
func Tone(freq float64, rate, samples int, phase *float64) []int16 {
	out := make([]int16, samples)
	step := 2 * math.Pi * freq / float64(rate)
	for i := range out {
		out[i] = int16(toneAmplitude * math.Sin(*phase))
		*phase += step
	}
	*phase = math.Mod(*phase, 2*math.Pi)
	return out
}

// Detect returns each frequency's Goertzel power over pcm, normalised by the
// window's energy.
func Detect(pcm []int16, rate int, freqs []float64) map[float64]float64 {
	var energy float64
	for _, s := range pcm {
		energy += float64(s) * float64(s)
	}
	out := make(map[float64]float64, len(freqs))
	if energy == 0 {
		return out
	}
	for _, f := range freqs {
		coeff := 2 * math.Cos(2*math.Pi*f/float64(rate))
		var s1, s2 float64
		for _, x := range pcm {
			s0 := float64(x) + coeff*s1 - s2
			s2, s1 = s1, s0
		}
		out[f] = (s1*s1 + s2*s2 - coeff*s1*s2) / (energy * float64(len(pcm)))
	}
	return out
}

// Dominant reports the one tone clearly present in pcm. ok is false for
// silence or when no tone beats the others by the dominance factor.
func Dominant(pcm []int16, rate int, freqs []float64) (float64, bool) {
	if len(pcm) == 0 || rms(pcm) < silenceRMS {
		return 0, false
	}
	var best, second, bestF float64
	for f, p := range Detect(pcm, rate, freqs) {
		switch {
		case p > best:
			second, best, bestF = best, p, f
		case p > second:
			second = p
		}
	}
	if best == 0 || best < dominance*second {
		return 0, false
	}
	return bestF, true
}

func rms(pcm []int16) float64 {
	var sum float64
	for _, s := range pcm {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(pcm)))
}
