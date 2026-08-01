// Package palette_test guards the visual properties that are correctness
// concerns rather than taste ones, against the redesign's design system
// (assets/design/README.md).
//
// The founding rule here used to be "colour means risk and nothing else",
// enforced by a chroma budget. The redesign retires that rule deliberately:
// there is an accent now, and the verdict colours live in bg/border/text
// trios tuned per theme. What survives, because it was never about taste:
//
//   - A rating is NEVER carried by colour alone. Red and Green sit ΔE 4.1
//     apart under simulated deuteranopia; the mitigation is the glyph and the
//     word, and both are asserted.
//   - Contrast is computed, not trusted. The handoff claims AA everywhere;
//     the old build sat near 1.6:1 in places precisely because nobody checked.
//   - The two anti-inversion rules for the light theme are tests, not lore:
//     verdict colours are REDRAWN per theme (dark's amber is 1.9:1 on white),
//     and light elevation is a border, never a shadow.
//   - Tokens are byte-fixed to the handoff table. Changing one means changing
//     the design system, not editing a stylesheet.
package palette_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// ---------------------------------------------------------------- tokens

// The handoff's dual-theme table, verbatim. These are the contract with the
// design system: a change here is a design revision, and the mockups in
// assets/design/preview are the reference it must be checked against.
var darkTokens = map[string]string{
	"--canvas":         "#06080c",
	"--surface":        "#0a0d13",
	"--surface-alt":    "#0c0f16",
	"--raised":         "#151a24",
	"--inset":          "#0f131b",
	"--border":         "#1a1f29",
	"--border-card":    "#1e2430",
	"--border-strong":  "#2a3346",
	"--hairline":       "#12161f",
	"--group-band":     "#080b10",
	"--text-1":         "#e8ecf3",
	"--text-2":         "#c6cedb",
	"--text-3":         "#98a2b3",
	"--text-4":         "#818c9e",
	"--text-5":         "#667085",
	"--text-faint":     "#4e5a6e",
	"--accent":         "#4c7dff",
	"--accent-text":    "#7fa0ff",
	"--accent-text-2":  "#b9cbff",
	"--nav-active-bg":  "#18203a",
	"--safe":           "#3dd68c",
	"--safe-bg":        "#0f1a16",
	"--safe-border":    "#1d3a2c",
	"--safe-text":      "#7fcfa8",
	"--caution":        "#f5b544",
	"--caution-bg":     "#1a1509",
	"--caution-border": "#3d2f13",
	"--caution-text":   "#d2a85c",
	"--held":           "#f2545b",
	"--held-bg":        "#1c0f11",
	"--held-border":    "#40201f",
	"--held-text":      "#d98a8d",
}

var lightTokens = map[string]string{
	"--canvas":         "#f4f5f7",
	"--surface":        "#ffffff",
	"--surface-alt":    "#fafbfc",
	"--raised":         "#eef0f4",
	"--inset":          "#ffffff",
	"--border":         "#e3e7ec",
	"--border-strong":  "#c2cad6",
	"--hairline":       "#edf0f4",
	"--group-band":     "#f7f8fa",
	"--text-1":         "#0b1220",
	"--text-2":         "#2c3542",
	"--text-3":         "#55606f",
	"--text-4":         "#6b7684",
	"--text-5":         "#8a93a0",
	"--accent":         "#2a5fe0",
	"--accent-text":    "#1f4cbf",
	"--nav-active-bg":  "#e8eefc",
	"--safe":           "#17875a",
	"--safe-bg":        "#e9f6ef",
	"--safe-border":    "#bfe3d0",
	"--safe-text":      "#0e6b46",
	"--caution":        "#9a6400",
	"--caution-bg":     "#fdf3e0",
	"--caution-border": "#f0dbae",
	"--caution-text":   "#7a4f00",
	"--held":           "#c0343c",
	"--held-bg":        "#fdecec",
	"--held-border":    "#f3c9cb",
	"--held-text":      "#9e262e",
}

// themeBlocks splits theme.css into the dark base and the light overrides.
// The light block appears twice (media query and attribute selector, since
// CSS has no mixins); both must carry the same values, so both are checked.
func themeBlocks(t *testing.T) (dark string, lights []string) {
	t.Helper()
	css := read(t, "theme.css")

	darkEnd := strings.Index(css, "prefers-color-scheme: light")
	if darkEnd < 0 {
		t.Fatal("theme.css has no light theme block")
	}
	dark = css[:darkEnd]

	for _, marker := range []string{
		`:root:not([data-theme="dark"])`,
		`:root[data-theme="light"]`,
	} {
		i := strings.Index(css, marker)
		if i < 0 {
			t.Fatalf("theme.css lost the %s selector; the explicit toggle and the OS "+
				"preference must both be honoured", marker)
			continue
		}
		end := strings.Index(css[i:], "\n}")
		lights = append(lights, css[i:i+end])
	}
	return dark, lights
}

func tokenValue(block, token string) string {
	re := regexp.MustCompile(regexp.QuoteMeta(token) + `:\s*([^;]+);`)
	m := re.FindStringSubmatch(block)
	if m == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(m[1]))
}

func TestTokensMatchTheHandoffTable(t *testing.T) {
	dark, lights := themeBlocks(t)

	for token, want := range darkTokens {
		if got := tokenValue(dark, token); got != want {
			t.Errorf("dark %s = %q, want %q — tokens are byte-fixed to "+
				"assets/design/README.md; changing one is a design revision", token, got, want)
		}
	}
	for i, block := range lights {
		for token, want := range lightTokens {
			if got := tokenValue(block, token); got != want {
				t.Errorf("light block %d: %s = %q, want %q", i, token, got, want)
			}
		}
	}
}

// The first anti-inversion rule: verdict colours are redrawn per theme, not
// flipped. Dark's amber as text on the light canvas is ~1.9:1 — if the light
// blocks ever stop overriding the trios, screenshots in light mode become
// unreadable while the build stays green.
func TestVerdictColoursAreRedrawnPerTheme(t *testing.T) {
	_, lights := themeBlocks(t)
	for _, token := range []string{"--safe", "--caution", "--held"} {
		for i, block := range lights {
			got := tokenValue(block, token)
			if got == "" {
				t.Errorf("light block %d does not redefine %s; a theme flip would ship "+
					"dark's verdict colours onto white", i, token)
				continue
			}
			if got == darkTokens[token] {
				t.Errorf("light block %d: %s reuses the dark value %s; the light set is "+
					"a separate drawing, not an inheritance", i, token, got)
			}
		}
	}

	// The concrete failure this rule exists for, kept as arithmetic: dark's
	// amber on the light canvas fails AA, the redrawn amber passes.
	if r := contrast(darkTokens["--caution"], lightTokens["--canvas"]); r > 3 {
		t.Errorf("dark amber on light canvas is %.1f:1; this test's premise is wrong", r)
	}
	if r := contrast(lightTokens["--caution"], lightTokens["--canvas"]); r < 4.5 {
		t.Errorf("light amber on light canvas is %.1f:1, below AA", r)
	}
}

// The second anti-inversion rule: light elevation is a border, never a
// shadow. Dark carries elevation as lighter fills; porting those as shadows
// is the mechanical-inversion mistake the handoff calls out.
func TestLightElevationIsBorderNotShadow(t *testing.T) {
	_, lights := themeBlocks(t)
	for i, block := range lights {
		if strings.Contains(block, "box-shadow") {
			t.Errorf("light block %d declares a box-shadow; light elevation is whiter "+
				"fill plus a 1px border", i)
		}
		if got := tokenValue(block, "--inset-border"); got == "" || got == "transparent" {
			t.Errorf("light block %d: --inset-border = %q; light list items are white "+
				"cards that need their 1px border to exist at all", i, got)
		}
	}
	dark, _ := themeBlocks(t)
	if got := tokenValue(dark, "--inset-border"); got != "transparent" {
		t.Errorf("dark --inset-border = %q, want transparent — dark elevation is fill, "+
			"and a visible border here would double-stroke every tile", got)
	}
}

// ---------------------------------------------------------------- contrast

// Contrast is computed with the WCAG relative-luminance formula rather than
// trusted from the handoff's prose. The pairs are the ones the design
// actually sets in ink.
func TestDocumentedPairsMeetAA(t *testing.T) {
	type pair struct {
		fg, bg string
		min    float64
		what   string
	}
	check := func(t *testing.T, tokens map[string]string, pairs []pair) {
		t.Helper()
		for _, p := range pairs {
			fg, bg := tokens[p.fg], tokens[p.bg]
			if fg == "" || bg == "" {
				t.Errorf("%s: token missing (%s=%q, %s=%q)", p.what, p.fg, fg, p.bg, bg)
				continue
			}
			if r := contrast(fg, bg); r < p.min {
				t.Errorf("%s: %s on %s is %.2f:1, need %.1f:1", p.what, fg, bg, r, p.min)
			}
		}
	}

	common := func(m map[string]string) []pair {
		return []pair{
			{"--text-1", "--canvas", 7, "primary text on canvas"},
			{"--text-1", "--surface", 7, "primary text on chrome"},
			{"--text-1", "--surface-alt", 7, "primary text on cards"},
			{"--text-1", "--inset", 7, "primary text on list items"},
			{"--text-2", "--surface", 7, "body text in dense panes"},
			{"--text-3", "--surface", 4.5, "secondary text"},
			{"--text-3", "--canvas", 4.5, "secondary text on canvas"},
			{"--accent-text", "--surface", 4.5, "links"},
			{"--safe-text", "--safe-bg", 4.5, "safe verdict text in its chip"},
			{"--caution-text", "--caution-bg", 4.5, "caution verdict text in its chip"},
			{"--held-text", "--held-bg", 4.5, "held verdict text in its chip"},
			{"--safe", "--surface", 4.5, "safe verdict as text"},
			{"--caution", "--surface", 4.5, "caution verdict as text"},
			{"--held", "--surface", 4.5, "held verdict as text"},
		}
	}
	t.Run("dark", func(t *testing.T) { check(t, darkTokens, common(darkTokens)) })
	t.Run("light", func(t *testing.T) { check(t, lightTokens, common(lightTokens)) })
}

// ------------------------------------------------------------ glyph + word

// Every rating carries a glyph AND the word. Either alone is insufficient:
// the glyph survives greyscale, the word survives a missing font. This is
// the survivor of the original palette's rules, because it was never about
// colour taste — Red and Green are ΔE 4.1 apart under deuteranopia.
func TestEveryRatingRendersGlyphAndWord(t *testing.T) {
	impact := read(t, "components/Impact.tsx")
	for _, glyph := range []string{"●", "▲", "■"} {
		if !strings.Contains(impact, glyph) {
			t.Errorf("glyph %q is gone from the rating map", glyph)
		}
	}

	// The redesign's rating surfaces. The triage cards and the detail pane
	// carry glyph + label; the step rows carry the verdict word — colour is
	// never the only carrier anywhere.
	for _, file := range []string{"components/review/Hero.tsx", "components/review/StepDetail.tsx"} {
		src := read(t, file)
		if !strings.Contains(src, "GLYPH[") {
			t.Errorf("%s shows a rating without its glyph", file)
		}
	}
	if src := read(t, "components/review/StepList.tsx"); !strings.Contains(src, "VERDICT[") {
		t.Error("StepList rows show a rating without its word; colour alone is unreadable to a CVD minority")
	}
}

// The verdict vocabulary is fixed copy (assets/design/README.md): the UI
// says what a rating means and never labels a control with a colour name.
func TestVerdictVocabularyIsTheHandoffs(t *testing.T) {
	impact := read(t, "components/Impact.tsx")
	for _, phrase := range []string{"Safe now", "Needs a call", "Held back"} {
		if !strings.Contains(impact, `"`+phrase+`"`) {
			t.Errorf("verdict label %q is gone from Impact.tsx", phrase)
		}
	}
}

// The glyphs must differ in silhouette, not just fill, so they survive
// greyscale and small sizes. Disc, triangle, square.
func TestGlyphsDifferInShape(t *testing.T) {
	impact := read(t, "components/Impact.tsx")

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

// ------------------------------------------------------------------- type

// Never below 11px. The handoff's floor, and the reason the old UI's 10px
// metadata was one of the things being replaced. Checked across every
// stylesheet, not just the token file, because the floor is exactly the kind
// of rule that erodes one "just this label" at a time.
func TestNoFontSizeBelowElevenPixels(t *testing.T) {
	styles, err := filepath.Glob(filepath.Join(uiSrc, "styles", "*.css"))
	if err != nil {
		t.Fatal(err)
	}
	styles = append(styles, filepath.Join(uiSrc, "theme.css"))

	re := regexp.MustCompile(`font-size:\s*([0-9.]+)(px|rem)`)
	for _, path := range styles {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			v, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				continue
			}
			px := v
			if m[2] == "rem" {
				px = v * 16
			}
			if px < 10.99 {
				t.Errorf("%s: font-size %s%s is below the 11px floor", filepath.Base(path), m[1], m[2])
			}
		}
	}
}

// Archivo is retired; the type system is IBM Plex Sans + IBM Plex Mono only.
func TestArchivoStaysRetired(t *testing.T) {
	for _, rel := range []string{"theme.css", "fonts.css"} {
		src := read(t, rel)
		if regexp.MustCompile(`(?i)font-family:[^;]*archivo`).MatchString(src) {
			t.Errorf("%s declares Archivo; the redesign's type system is Plex only", rel)
		}
	}
	if strings.Contains(read(t, "fonts.css"), "@fontsource-variable/archivo") {
		t.Error("fonts.css still loads the Archivo package")
	}
}

// ------------------------------------------------------------------ maths

// contrast returns the WCAG 2.x contrast ratio between two #rrggbb colours.
func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(hex string) float64 {
	hex = strings.TrimPrefix(strings.ToLower(hex), "#")
	if len(hex) != 6 {
		panic(fmt.Sprintf("not a #rrggbb colour: %q", hex))
	}
	chan1 := func(s string) float64 {
		v, err := strconv.ParseInt(s, 16, 32)
		if err != nil {
			panic(err)
		}
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	r, g, b := chan1(hex[0:2]), chan1(hex[2:4]), chan1(hex[4:6])
	return 0.2126*r + 0.7152*g + 0.0722*b
}
