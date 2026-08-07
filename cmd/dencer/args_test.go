package main

import (
	"flag"
	"io"
	"strings"
	"testing"
	"time"
)

// A FlagSet shaped like the real ones: a mix of string, bool and duration
// flags, which is what makes "does this flag eat the next argument" a real
// question rather than a guess.
func drainLikeFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("drain", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("context", "", "")
	fs.String("namespace", "", "")
	fs.Bool("dry-run", false, "")
	fs.Bool("yes", false, "")
	fs.Duration("timeout", 30*time.Second, "")
	return fs
}

func TestReorderArgsAcceptsThePositionalAnywhere(t *testing.T) {
	// Every one of these means the same thing to a person. Before this,
	// only the last parsed — the node had to come after every flag, with
	// nothing following it.
	cases := [][]string{
		{"worker-3"},
		{"worker-3", "--yes"},
		{"worker-3", "--context", "gke-play", "--dry-run"},
		{"--context", "gke-play", "worker-3", "--timeout", "60s"},
		{"--dry-run", "worker-3"},
		{"--context=gke-play", "worker-3", "--yes"},
		{"--context", "gke-play", "--timeout", "60s", "--yes", "worker-3"},
	}

	for _, args := range cases {
		fs := drainLikeFlags()
		if err := fs.Parse(reorderArgs(fs, args)); err != nil {
			t.Errorf("%v: parse: %v", args, err)
			continue
		}
		if fs.NArg() != 1 {
			t.Errorf("%v: NArg = %d, want 1 (positional swallowed by the flags)", args, fs.NArg())
			continue
		}
		if got := fs.Arg(0); got != "worker-3" {
			t.Errorf("%v: node = %q, want worker-3", args, got)
		}
	}
}

func TestReorderArgsKeepsFlagValuesWithTheirFlags(t *testing.T) {
	fs := drainLikeFlags()
	if err := fs.Parse(reorderArgs(fs, []string{"worker-3", "--context", "gke-play", "--timeout", "90s"})); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := fs.Lookup("context").Value.String(); got != "gke-play" {
		t.Errorf("context = %q, want gke-play — a flag's value was treated as positional", got)
	}
	if got := fs.Lookup("timeout").Value.String(); got != "1m30s" {
		t.Errorf("timeout = %q, want 1m30s", got)
	}
	if fs.Arg(0) != "worker-3" {
		t.Errorf("node = %q, want worker-3", fs.Arg(0))
	}
}

// A bool flag stands alone. Treating it as value-taking would eat the node.
func TestReorderArgsDoesNotLetBoolFlagsEatTheArgument(t *testing.T) {
	fs := drainLikeFlags()
	if err := fs.Parse(reorderArgs(fs, []string{"--dry-run", "worker-3"})); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fs.Arg(0) != "worker-3" {
		t.Fatalf("node = %q, want worker-3: --dry-run consumed it", fs.Arg(0))
	}
	if fs.Lookup("dry-run").Value.String() != "true" {
		t.Error("--dry-run did not take effect")
	}
}

// Everything after `--` is positional, by long-standing convention.
func TestReorderArgsHonoursTheDoubleDash(t *testing.T) {
	fs := drainLikeFlags()
	if err := fs.Parse(reorderArgs(fs, []string{"--yes", "--", "--weirdly-named-node"})); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := fs.Arg(0); got != "--weirdly-named-node" {
		t.Errorf("arg = %q, want --weirdly-named-node", got)
	}
}

// An unknown flag must still produce Parse's own error, not be silently
// rearranged into a positional argument.
func TestReorderArgsLeavesUnknownFlagsToParse(t *testing.T) {
	fs := drainLikeFlags()
	err := fs.Parse(reorderArgs(fs, []string{"--nope", "worker-3"}))
	if err == nil {
		t.Fatal("expected an error for an unknown flag")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error should name the unknown flag, got: %v", err)
	}
}
