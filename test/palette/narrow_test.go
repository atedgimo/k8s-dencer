// The narrow-viewport layout, asserted rather than eyeballed once.
//
// Both tests here exist because of a defect they would have caught. The
// first: a media query whose selectors matched nothing at all. Six of the
// selectors in the first draft of narrow.css were plausible names for
// elements that are called something else, and CSS reports that by doing
// nothing — the rule sits in the file looking correct forever. There is no
// build error, no console warning, and the only symptom is a layout that
// stays broken at a width nobody re-checks.
//
// The second: `margin: var(--space-5) 0` in views.css, referring to a token
// that was never defined. An undefined custom property makes the whole
// declaration invalid at computed-value time, so the margin silently became
// zero. It had been in the file, doing nothing, since the redesign.
package palette_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func uiFiles(t *testing.T, dir string, exts ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join("..", "..", "ui", "src")
	err := filepath.Walk(filepath.Join(root, dir), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		for _, e := range exts {
			if strings.HasSuffix(p, e) {
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(root, p)
				out[rel] = string(b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// Every class narrow.css targets must be a class something actually renders.
//
// A rule that matches nothing is indistinguishable from a rule that works
// until someone opens the page at that width, which for a breakpoint is
// approximately never.
func TestNarrowLayoutTargetsRealClasses(t *testing.T) {
	narrowPath := filepath.Join("..", "..", "ui", "src", "styles", "narrow.css")
	narrow, err := os.ReadFile(narrowPath)
	if err != nil {
		t.Fatalf("read narrow.css: %v", err)
	}

	// Everything else the UI ships: the other stylesheets define classes, the
	// components render them. A class needs to appear in one or the other.
	var known strings.Builder
	for name, body := range uiFiles(t, ".", ".css", ".tsx", ".ts") {
		if strings.HasSuffix(name, "narrow.css") {
			continue
		}
		known.WriteString(body)
	}
	haystack := known.String()

	classRE := regexp.MustCompile(`\.([a-zA-Z][\w-]*)`)
	seen := map[string]bool{}
	for _, m := range classRE.FindAllStringSubmatch(string(narrow), -1) {
		cls := m[1]
		if seen[cls] {
			continue
		}
		seen[cls] = true

		// Defined as a selector in another stylesheet...
		defined := regexp.MustCompile(`\.` + regexp.QuoteMeta(cls) + `(?:[^\w-]|$)`).MatchString(haystack)
		// ...or rendered as a class name by a component.
		rendered := regexp.MustCompile(`["'\s]` + regexp.QuoteMeta(cls) + `["'\s]`).MatchString(haystack)
		if !defined && !rendered {
			t.Errorf("narrow.css targets .%s, which nothing defines or renders — "+
				"the rule matches no element and the breakpoint silently does nothing", cls)
		}
	}
}

// Every custom property the stylesheets USE must be one theme.css DEFINES.
//
// CSS treats a reference to an undefined property as invalid at
// computed-value time: the entire declaration is dropped, not just the one
// value. So a typo does not degrade a colour or a gap, it deletes the rule,
// and it does so without a single diagnostic anywhere in the build.
func TestEveryTokenUsedIsDefined(t *testing.T) {
	root := filepath.Join("..", "..", "ui", "src")
	themeCSS, err := os.ReadFile(filepath.Join(root, "theme.css"))
	if err != nil {
		t.Fatalf("read theme.css: %v", err)
	}

	defRE := regexp.MustCompile(`(?m)^\s*(--[\w-]+)\s*:`)
	defined := map[string]bool{}
	for _, m := range defRE.FindAllStringSubmatch(string(themeCSS), -1) {
		defined[m[1]] = true
	}
	// A handful of tokens are set on the element by the components rather
	// than declared in theme.css; they are still definitions.
	for name, body := range uiFiles(t, ".", ".tsx", ".ts", ".css") {
		if strings.HasSuffix(name, "theme.css") {
			continue
		}
		for _, m := range defRE.FindAllStringSubmatch(body, -1) {
			defined[m[1]] = true
		}
		// Inline style objects: style={{ "--foo": ... }}
		for _, m := range regexp.MustCompile(`["'](--[\w-]+)["']\s*:`).FindAllStringSubmatch(body, -1) {
			defined[m[1]] = true
		}
	}

	useRE := regexp.MustCompile(`var\(\s*(--[\w-]+)`)
	for name, body := range uiFiles(t, ".", ".css") {
		for _, m := range useRE.FindAllStringSubmatch(body, -1) {
			tok := m[1]
			if defined[tok] {
				continue
			}
			// A fallback makes the reference survivable — var(--x, 8px) is a
			// deliberate default, not a typo.
			if regexp.MustCompile(`var\(\s*` + regexp.QuoteMeta(tok) + `\s*,`).MatchString(body) {
				continue
			}
			t.Errorf("%s uses %s, which nothing defines — the whole declaration "+
				"is dropped at computed-value time, silently", name, tok)
		}
	}
}

// The breakpoints are a design decision, so changing them should be one too.
//
// Fixed here because the issue that prompted the work named a width: the
// maintainer read the UI on a phone and found a 224px rail crushed against a
// horizontally scrolling table. If someone widens the first tier past 1200
// the panes stop stacking on a laptop, which is the case that was reported.
func TestNarrowBreakpointsAreTheOnesWeChose(t *testing.T) {
	narrow, err := os.ReadFile(filepath.Join("..", "..", "ui", "src", "styles", "narrow.css"))
	if err != nil {
		t.Fatalf("read narrow.css: %v", err)
	}
	for _, want := range []string{
		"@media (max-width: 1200px)", // panes stack
		"@media (max-width: 1000px)", // rail becomes icons
		"@media (max-width: 720px)",  // rail becomes a strip; execute hidden
	} {
		if !strings.Contains(string(narrow), want) {
			t.Errorf("missing breakpoint %q", want)
		}
	}
}

// Below the smallest tier the UI must not offer to evict anything.
//
// The read path is the requirement — someone paged, checking whether the
// cluster is fine. Draining a node is not something to confirm with a thumb,
// and a control that is merely hard to hit is still a control that was hit.
func TestTheSmallestTierHidesEveryExecuteControl(t *testing.T) {
	narrow, err := os.ReadFile(filepath.Join("..", "..", "ui", "src", "styles", "narrow.css"))
	if err != nil {
		t.Fatalf("read narrow.css: %v", err)
	}
	_, tier, ok := strings.Cut(string(narrow), "@media (max-width: 720px)")
	if !ok {
		t.Fatal("no 720px tier")
	}
	for _, cls := range []string{
		".reviewfooter-actions", // Rehearse and Drain
		".steprow-box",          // the per-step checkbox
		".stepdetail-actions",   // add / skip in the detail pane
	} {
		if !strings.Contains(tier, cls) {
			t.Errorf("%s is still reachable below 720px — the read path is the "+
				"requirement, and everything that evicts a pod should be gone", cls)
		}
	}
}
