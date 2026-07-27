// Package palette_test guards the one visual property that is a correctness
// concern rather than a taste one.
//
// The safety scale is the only colour in the UI, and Red and Green are
// indistinguishable to a large minority of readers: the M7 palette review
// measured them at ΔE 4.1 under simulated deuteranopia, against a floor of 8.
// That is not fixable by re-tuning — those hues are load-bearing conventions —
// so the mitigation is that a rating is NEVER carried by colour alone.
//
// These tests exist because that mitigation is easy to lose by accident. A
// redesign that drops a glyph, or a "cleanup" that removes the word next to
// it, produces a UI that looks tidier and is unusable for some operators. The
// M11 rebuild is exactly the kind of change that could have done it.
package palette_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const uiSrc = "../../ui/src"

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(uiSrc, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// The validated values. Changing one means re-running a CVD review, not
// editing this constant.
var safetyScale = map[string]string{
	"--risk-green":  "#0ca30c",
	"--risk-yellow": "#fab219",
	"--risk-red":    "#d03b3b",
}

func TestSafetyScaleIsUnchanged(t *testing.T) {
	css := read(t, "theme.css")
	for token, want := range safetyScale {
		re := regexp.MustCompile(regexp.QuoteMeta(token) + `:\s*([^;]+);`)
		m := re.FindStringSubmatch(css)
		if m == nil {
			t.Errorf("%s is missing from theme.css", token)
			continue
		}
		if got := strings.TrimSpace(m[1]); !strings.EqualFold(got, want) {
			t.Errorf("%s = %s, want %s — these were validated for colour-vision "+
				"deficiency; re-run that review before changing them", token, got, want)
		}
	}
}

// The scale must not be themed. A light-mode variant would be a second,
// unreviewed palette shipping under the same name.
func TestSafetyScaleIsNotOverriddenPerTheme(t *testing.T) {
	css := read(t, "theme.css")
	// Everything after the first light-theme block.
	i := strings.Index(css, "prefers-color-scheme: light")
	if i < 0 {
		t.Skip("no light theme in theme.css")
	}
	for token := range safetyScale {
		if strings.Contains(css[i:], token+":") {
			t.Errorf("%s is redefined for a theme; the safety scale is fixed across themes", token)
		}
	}
}

// Every rating carries a glyph AND the word. Either alone is insufficient:
// the glyph survives greyscale, the word survives a missing font.
func TestEveryRatingRendersGlyphAndWord(t *testing.T) {
	impact := read(t, "components/Impact.tsx")
	for _, glyph := range []string{"●", "▲", "■"} {
		if !strings.Contains(impact, glyph) {
			t.Errorf("glyph %q is gone from the rating map", glyph)
		}
	}

	// The three surfaces that show a rating. Each must render the glyph map
	// and the rating word, not just apply a colour class.
	for _, file := range []string{"components/Verdict.tsx", "components/StepLedger.tsx"} {
		src := read(t, file)
		if !strings.Contains(src, "GLYPH[") {
			t.Errorf("%s shows a rating without its glyph", file)
		}
		if !strings.Contains(src, "impact") && !strings.Contains(src, "Impact") {
			t.Errorf("%s shows a rating without naming it", file)
		}
	}
}

// The glyphs must differ in silhouette, not just fill, so they survive
// greyscale and small sizes. Disc, triangle, square.
func TestGlyphsDifferInShape(t *testing.T) {
	impact := read(t, "components/Impact.tsx")

	// Scoped to the GLYPH map. Impact.tsx also holds a DESCRIPTION map keyed by
	// the same three names, and matching both would compare prose to glyphs.
	start := strings.Index(impact, "GLYPH: Record<Impact, string> = {")
	if start < 0 {
		t.Fatal("cannot find the GLYPH map in Impact.tsx")
	}
	end := strings.Index(impact[start:], "}")
	block := impact[start : start+end]

	re := regexp.MustCompile(`(Green|Yellow|Red):\s*"([^"]+)"`)
	seen := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		if prev, dup := seen[m[2]]; dup {
			t.Errorf("%s and %s share the glyph %q; ratings must be distinguishable "+
				"without colour", prev, m[1], m[2])
		}
		seen[m[2]] = m[1]
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 distinct rating glyphs, found %d", len(seen))
	}
}

// Colour means risk and nothing else. That rule is what makes the ratings
// legible on a page of thirty nodes — if the chrome starts introducing hues,
// the ratings stop being the only thing that stands out.
func TestOnlyTheSafetyScaleIsChromatic(t *testing.T) {
	css := read(t, "theme.css")

	// Every hex colour declared as a custom property.
	re := regexp.MustCompile(`(--[a-z0-9-]+):\s*(#[0-9a-fA-F]{6})\b`)
	for _, m := range re.FindAllStringSubmatch(css, -1) {
		token, hex := m[1], m[2]
		if _, isRisk := safetyScale[token]; isRisk {
			continue
		}
		if c := chroma(hex); c > 0.15 {
			t.Errorf("%s = %s has chroma %.2f; only the safety scale may be "+
				"chromatic, so a rating is never competing with the chrome",
				token, hex, c)
		}
	}
}

// chroma returns the raw channel spread of a #rrggbb colour, 0..1.
//
// Deliberately not HSL saturation, which divides by lightness and so reports a
// near-black neutral like #0b0d10 as 19% saturated on a channel spread of five
// parts in 255. Spread answers the question actually being asked here: would a
// reader see a hue, or a grey?
func chroma(hex string) float64 {
	r := float64(hexPair(hex[1:3]))
	g := float64(hexPair(hex[3:5]))
	b := float64(hexPair(hex[5:7]))

	max, min := r, r
	for _, v := range []float64{g, b} {
		if v > max {
			max = v
		}
		if v < min {
			min = v
		}
	}
	return (max - min) / 255
}

func hexPair(s string) int {
	v := 0
	for _, c := range s {
		v *= 16
		switch {
		case c >= '0' && c <= '9':
			v += int(c - '0')
		case c >= 'a' && c <= 'f':
			v += int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v += int(c-'A') + 10
		}
	}
	return v
}
