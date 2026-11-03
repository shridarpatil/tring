//go:build e2e

package e2e

import "testing"

func window(freq float64, rate int) []int16 {
	var phase float64
	return Tone(freq, rate, rate/5, &phase) // 200 ms
}

func TestDominantFindsEachTone(t *testing.T) {
	for _, rate := range []int{8000, 48000} {
		for _, f := range Freqs {
			got, ok := Dominant(window(f, rate), rate, Freqs)
			if !ok || got != f {
				t.Errorf("rate %d: Dominant(%v Hz) = %v, %v; want %v, true", rate, f, got, ok, f)
			}
		}
	}
}

func TestDominantSilenceAndMix(t *testing.T) {
	if f, ok := Dominant(make([]int16, 1600), 8000, Freqs); ok {
		t.Errorf("silence: Dominant = %v, true; want ok=false", f)
	}
	a, b := window(FreqCustomer, 8000), window(FreqAgent1, 8000)
	mix := make([]int16, len(a))
	for i := range a {
		mix[i] = a[i]/2 + b[i]/2
	}
	if f, ok := Dominant(mix, 8000, Freqs); ok {
		t.Errorf("equal 300+440 mix: Dominant = %v, true; want ok=false", f)
	}
}

func TestMuLawRoundTripKeepsTone(t *testing.T) {
	pcm := MuLawDecode(MuLawEncode(window(FreqAgent1, 8000)))
	if f, ok := Dominant(pcm, 8000, Freqs); !ok || f != FreqAgent1 {
		t.Errorf("after μ-law round trip: Dominant = %v, %v; want %v", f, ok, FreqAgent1)
	}
}

func TestToneContinuesPhase(t *testing.T) {
	var phase float64
	a := Tone(FreqHold, 8000, 160, &phase)
	b := Tone(FreqHold, 8000, 160, &phase)
	var whole float64
	w := Tone(FreqHold, 8000, 320, &whole)
	for i := range b {
		if d := int(b[i]) - int(w[160+i]); d > 1 || d < -1 {
			t.Fatalf("sample %d: frame 2 = %d, one 320-sample tone = %d (phase not carried)", i, b[i], w[160+i])
		}
	}
	_ = a
}
