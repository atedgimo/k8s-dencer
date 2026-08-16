// Package docs_test keeps the documentation navigable.
//
// Two failures this catches, both of which had already happened:
//
//   - benchmarks.md linked to internal/store/sqlite/sqlite.go for months after
//     the two stores were unified into sqlstore. Nothing breaks, nothing warns;
//     a reader following the one link that would have shown them the code gets
//     a 404 instead.
//   - the project README carried its own copy of the documentation table,
//     which drifted until it was missing three files that existed. Two indexes
//     is worse than one, because the wrong one is always the one you read.
package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// [text](path) and [text](path#anchor), skipping absolute URLs.
var linkRE = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)

func repoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// markdownFiles returns the pages a reader actually navigates: the project
// README and everything under docs/. Deliberately not every .md in the tree —
// assets/ and hack/ carry their own local notes with their own conventions.
func markdownFiles(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	files := []string{filepath.Join(root, "README.md")}
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			files = append(files, filepath.Join(root, "docs", e.Name()))
		}
	}
	return files
}

// Every relative link must point at something that exists.
func TestEveryLinkResolves(t *testing.T) {
	root := repoRoot(t)
	for _, f := range markdownFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range linkRE.FindAllStringSubmatch(string(body), -1) {
			link := m[2]
			if strings.HasPrefix(link, "http://") ||
				strings.HasPrefix(link, "https://") ||
				strings.HasPrefix(link, "mailto:") ||
				strings.HasPrefix(link, "#") {
				continue
			}
			// Strip an anchor; the file is what has to exist.
			if i := strings.IndexByte(link, '#'); i >= 0 {
				link = link[:i]
			}
			if link == "" {
				continue
			}
			target := filepath.Join(filepath.Dir(f), link)
			if _, err := os.Stat(target); err != nil {
				rel, _ := filepath.Rel(root, f)
				t.Errorf("%s: [%s](%s) points at nothing", rel, m[1], m[2])
			}
		}
	}
}

// Every page under docs/ must be reachable from the documentation index.
//
// A doc nobody links is a doc nobody reads, and it rots without anyone
// noticing — which is a worse outcome than not having written it.
func TestEveryDocIsInTheIndex(t *testing.T) {
	root := repoRoot(t)
	index, err := os.ReadFile(filepath.Join(root, "docs", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".md") || name == "README.md" {
			continue
		}
		if !strings.Contains(string(index), "("+name+")") &&
			!strings.Contains(string(index), "("+name+"#") {
			t.Errorf("docs/%s is not linked from docs/README.md — an unlinked doc "+
				"is one nobody reads and nobody notices going stale", name)
		}
	}
}

// The project README must point at the index rather than duplicate it.
//
// It used to carry its own table of every document. That table drifted: by the
// time anyone looked it was missing gcp-setup.md, findings.md and releases.md,
// and a reader who trusted it never learned those existed.
func TestTheReadmeDefersToTheIndex(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(body)

	if !strings.Contains(readme, "docs/README.md") {
		t.Error("the README does not link the documentation index")
	}

	// Count distinct docs/*.md links in the README. A short signpost is the
	// intent; a full second catalogue is the failure mode.
	linked := map[string]bool{}
	for _, m := range linkRE.FindAllStringSubmatch(readme, -1) {
		if strings.HasPrefix(m[2], "docs/") && strings.HasSuffix(m[2], ".md") {
			linked[m[2]] = true
		}
	}
	entries, _ := os.ReadDir(filepath.Join(root, "docs"))
	total := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") && e.Name() != "README.md" {
			total++
		}
	}
	// Signposting most of them is fine and useful. Listing every single one,
	// index included, means the README has become the index again.
	if len(linked) > total {
		t.Errorf("the README links %d docs of %d — it has grown back into a "+
			"second index; keep it a signpost and let docs/README.md be the catalogue",
			len(linked), total)
	}
}
