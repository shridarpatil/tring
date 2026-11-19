// Package g711 codes G.711 A-law and µ-law: one byte per 8 kHz sample, the
// codec a phone room carries end to end. The media plane forwards G.711 as is;
// only the barge mixer, the recorder's silence and the e2e tests code samples.
// The algorithms are the ITU-T G.711 reference ones (as in Sun's public
// g711.c), on 16-bit linear PCM.
package g711

import "github.com/shridarpatil/tring/internal/media/room"

// Decode decodes data in law c (room.PCMA or room.PCMU) into pcm, one sample
// per byte, and returns the samples written.
func Decode(c room.Codec, data []byte, pcm []int16) int {
	n := min(len(data), len(pcm))
	for i, b := range data[:n] {
		if c == room.PCMA {
			pcm[i] = alawToLinear(b)
		} else {
			pcm[i] = ulawToLinear(b)
		}
	}
	return n
}

// Encode encodes pcm in law c into data, one byte per sample, and returns the
// bytes written.
func Encode(c room.Codec, pcm []int16, data []byte) int {
	n := min(len(pcm), len(data))
	for i, v := range pcm[:n] {
		if c == room.PCMA {
			data[i] = linearToAlaw(v)
		} else {
			data[i] = linearToUlaw(v)
		}
	}
	return n
}

// Silence is law c's zero-amplitude sample.
func Silence(c room.Codec) byte {
	if c == room.PCMA {
		return 0xD5
	}
	return 0xFF
}

// segment returns the index of the first segment end >= v (len(ends) when v
// is past the last).
func segment(v int, ends *[8]int) int {
	for i, e := range ends {
		if v <= e {
			return i
		}
	}
	return len(ends)
}

var alawSegEnds = [8]int{0x1F, 0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF}

func linearToAlaw(pcm int16) byte {
	v := int(pcm) >> 3
	mask := 0xD5
	if v < 0 {
		mask = 0x55
		v = -v - 1
	}
	seg := segment(v, &alawSegEnds)
	if seg >= 8 {
		return byte(0x7F ^ mask)
	}
	a := seg << 4
	if seg < 2 {
		a |= (v >> 1) & 0xF
	} else {
		a |= (v >> seg) & 0xF
	}
	return byte(a ^ mask)
}

func alawToLinear(a byte) int16 {
	a ^= 0x55
	t := int(a&0xF) << 4
	switch seg := int(a&0x70) >> 4; seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= seg - 1
	}
	if a&0x80 != 0 {
		return int16(t)
	}
	return int16(-t)
}

const (
	ulawBias = 0x84
	ulawClip = 8159
)

var ulawSegEnds = [8]int{0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF, 0x1FFF}

func linearToUlaw(pcm int16) byte {
	v := int(pcm) >> 2
	mask := 0xFF
	if v < 0 {
		v = -v
		mask = 0x7F
	}
	v = min(v, ulawClip) + ulawBias>>2
	seg := segment(v, &ulawSegEnds)
	if seg >= 8 {
		return byte(0x7F ^ mask)
	}
	return byte(((seg << 4) | ((v >> (seg + 1)) & 0xF)) ^ mask)
}

func ulawToLinear(u byte) int16 {
	u = ^u
	t := (int(u&0xF) << 3) + ulawBias
	t <<= int(u&0x70) >> 4
	if u&0x80 != 0 {
		return int16(ulawBias - t)
	}
	return int16(t - ulawBias)
}
