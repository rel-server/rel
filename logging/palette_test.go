package logging

import (
	"math"
	"testing"
)

// xterm256RGB is the RGB value xterm gives a code of the 256-color palette's 6x6x6 cube
// (16-231) or grey ramp (232-255). Codes below 16 are theme-dependent, so none is used.
func xterm256RGB(code int) [3]float64 {
	if code >= 232 {
		v := float64(8 + 10*(code-232))
		return [3]float64{v, v, v}
	}
	levels := [6]float64{0, 95, 135, 175, 215, 255}
	code -= 16
	return [3]float64{levels[code/36], levels[(code/6)%6], levels[code%6]}
}

func srgbToLab(c [3]float64) [3]float64 {
	lin := func(u float64) float64 {
		u /= 255
		if u <= 0.04045 {
			return u / 12.92
		}
		return math.Pow((u+0.055)/1.055, 2.4)
	}
	r, g, b := lin(c[0]), lin(c[1]), lin(c[2])
	x := (0.4124*r + 0.3576*g + 0.1805*b) / 0.95047
	y := 0.2126*r + 0.7152*g + 0.0722*b
	z := (0.0193*r + 0.1192*g + 0.9505*b) / 1.08883
	f := func(t float64) float64 {
		if t > 0.008856 {
			return math.Cbrt(t)
		}
		return 7.787*t + 16.0/116
	}
	fx, fy, fz := f(x), f(y), f(z)
	return [3]float64{116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)}
}

// deltaE2000 is the CIEDE2000 color difference of two CIE Lab colors : about 1 is the smallest
// difference an eye notices, 20 and more are clearly different colors.
func deltaE2000(l1, l2 [3]float64) float64 {
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	deg := func(r float64) float64 { return math.Mod(r*180/math.Pi+360, 360) }
	pow7 := func(x float64) float64 { return math.Pow(x, 7) }

	c1, c2 := math.Hypot(l1[1], l1[2]), math.Hypot(l2[1], l2[2])
	cBar := (c1 + c2) / 2
	g := 0.5 * (1 - math.Sqrt(pow7(cBar)/(pow7(cBar)+pow7(25))))
	a1, a2 := (1+g)*l1[1], (1+g)*l2[1]
	c1p, c2p := math.Hypot(a1, l1[2]), math.Hypot(a2, l2[2])
	h1p, h2p := 0.0, 0.0
	if c1p != 0 {
		h1p = deg(math.Atan2(l1[2], a1))
	}
	if c2p != 0 {
		h2p = deg(math.Atan2(l2[2], a2))
	}

	dL, dC := l2[0]-l1[0], c2p-c1p
	dh := 0.0
	if c1p*c2p != 0 {
		dh = h2p - h1p
		if dh > 180 {
			dh -= 360
		} else if dh < -180 {
			dh += 360
		}
	}
	dH := 2 * math.Sqrt(c1p*c2p) * math.Sin(rad(dh/2))

	lBar, cBarP := (l1[0]+l2[0])/2, (c1p+c2p)/2
	hBar := h1p + h2p
	if c1p*c2p != 0 {
		switch {
		case math.Abs(h1p-h2p) <= 180:
			hBar = (h1p + h2p) / 2
		case h1p+h2p < 360:
			hBar = (h1p + h2p + 360) / 2
		default:
			hBar = (h1p + h2p - 360) / 2
		}
	}
	t := 1 - 0.17*math.Cos(rad(hBar-30)) + 0.24*math.Cos(rad(2*hBar)) + 0.32*math.Cos(rad(3*hBar+6)) - 0.20*math.Cos(rad(4*hBar-63))
	dTheta := 30 * math.Exp(-math.Pow((hBar-275)/25, 2))
	rc := 2 * math.Sqrt(pow7(cBarP)/(pow7(cBarP)+pow7(25)))
	sl := 1 + 0.015*math.Pow(lBar-50, 2)/math.Sqrt(20+math.Pow(lBar-50, 2))
	sc := 1 + 0.045*cBarP
	sh := 1 + 0.015*cBarP*t
	rt := -math.Sin(rad(2*dTheta)) * rc
	return math.Sqrt(math.Pow(dL/sl, 2) + math.Pow(dC/sc, 2) + math.Pow(dH/sh, 2) + rt*(dC/sc)*(dH/sh))
}

func codeDistance(a, b int) float64 {
	return deltaE2000(srgbToLab(xterm256RGB(a)), srgbToLab(xterm256RGB(b)))
}

// TestDeltaE2000_KnownPairs : the implementation reproduces published CIEDE2000 test values
// (Sharma, Wu, Dalal 2005, pairs 1 and 2) before the palette is judged with it.
func TestDeltaE2000_KnownPairs(t *testing.T) {
	cases := []struct {
		a, b [3]float64
		want float64
	}{
		{[3]float64{50, 2.6772, -79.7751}, [3]float64{50, 0, -82.7485}, 2.0425},
		{[3]float64{50, 3.1571, -77.2803}, [3]float64{50, 0, -82.7485}, 2.8615},
	}
	for _, c := range cases {
		if got := deltaE2000(c.a, c.b); math.Abs(got-c.want) > 0.001 {
			t.Errorf("deltaE2000(%v, %v) = %.4f, want %.4f", c.a, c.b, got, c.want)
		}
	}
}

// TestPalette_ReadableOnBlackAndWhite : every color stands out from both a black and a white
// terminal background.
func TestPalette_ReadableOnBlackAndWhite(t *testing.T) {
	const minAgainstBackground = 30
	black, white := srgbToLab([3]float64{0, 0, 0}), srgbToLab([3]float64{255, 255, 255})
	for name, code := range paletteEntries() {
		lab := srgbToLab(xterm256RGB(code))
		if d := deltaE2000(lab, black); d < minAgainstBackground {
			t.Errorf("%s (code %d) is too close to a black background : ΔE %.1f", name, code, d)
		}
		if d := deltaE2000(lab, white); d < minAgainstBackground {
			t.Errorf("%s (code %d) is too close to a white background : ΔE %.1f", name, code, d)
		}
	}
}

// TestPalette_EntriesOfALineAreDistinct : colors that can appear on the same access line differ
// pairwise. The warning and error level colors are the 4xx and 5xx ones and the debug level is
// the grey of other verbs ; none of these shares a line with its twin.
func TestPalette_EntriesOfALineAreDistinct(t *testing.T) {
	const minBetweenEntries = 20
	entries := paletteEntries()
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	for i, a := range names {
		for _, b := range names[i+1:] {
			if entries[a] == entries[b] {
				t.Errorf("%s and %s use the same code %d", a, b, entries[a])
				continue
			}
			if d := codeDistance(entries[a], entries[b]); d < minBetweenEntries {
				t.Errorf("%s (%d) and %s (%d) are too close : ΔE %.1f", a, entries[a], b, entries[b], d)
			}
		}
	}
}

// TestPalette_UsesNoThemeColor : codes below 16 are remapped by terminal themes.
func TestPalette_UsesNoThemeColor(t *testing.T) {
	for name, code := range paletteEntries() {
		if code < 16 || code > 255 {
			t.Errorf("%s uses code %d, outside the fixed 16-255 range", name, code)
		}
	}
}

func paletteEntries() map[string]int {
	return map[string]int{
		"path": colorPath, "role": colorRole,
		"GET": colorGET, "POST": colorPOST, "other verbs": colorOtherVerb,
		"2xx": colorStatus2xx, "4xx": colorStatus4xx, "5xx": colorStatus5xx,
		"info level": colorLevelInfo, "targets": colorTargets,
	}
}
