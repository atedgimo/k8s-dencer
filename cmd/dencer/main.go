// Command dencer is the command-line client for k8s-dencer.
//
// Also installable as a kubectl plugin: name it kubectl-dencer on PATH and
// `kubectl dencer plan` works, which is why every global flag mirrors
// kubectl's spelling.
//
// No third-party CLI framework. The command set is small and flat, the flag
// parsing is one stdlib FlagSet per subcommand, and the dependency tree of a
// tool that can drain nodes is worth keeping short.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/cli"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

var version = "dev"

const usage = `dencer — inspect and run Kubernetes node-consolidation plans.

Usage:
  dencer <command> [flags]

Commands:
  plan                  the current plan, one line per step
  explain <step>        why a step is rated as it is, and what it moves
  why <ns>/<pod>        why a pod can or cannot move
  run --steps 1,3-5     execute selected steps and watch them
  converge              closed loop: re-plan after every drain, inside bounds
  status                the run in flight, or the last one
  reclamations          what actually became of the nodes you drained
  preflight             will every node drain? run before a node-pool rotation
  audit                 what cannot survive a node loss, and why
  recommend             what is missing — PDBs, replicas, requests — with fixes
  rightsizing           requests vs observed usage, per workload
  whatif --without-nodes a,b | --without-zone z
                        simulate losing nodes or a zone: does everything still fit?
  drain <node>          guarded drain of one node: the rails, not bare kubectl
  version               the version of this binary (also --version)

Global flags:
  --server URL          backend base URL. Default: port-forward via kubeconfig
  --token TOKEN         bearer token. Default: $DENCER_TOKEN, then kubeconfig
  -n, --namespace NS    release namespace (default k8s-dencer)
  --release NAME        Helm release name (default k8s-dencer)
  --kubeconfig PATH     kubeconfig to use
  --context NAME        kubeconfig context to use
  -o, --output FORMAT   text (default), json or yaml
  --timeout DURATION    per-request timeout (default 30s)

Examples:
  dencer plan
  dencer explain 3
  dencer why shop/web-7d9f-abcde
  dencer run --steps 1,2 --dry-run
  dencer converge --max-nodes 5 --max-impact Green
  dencer whatif --without-zone eu-west-1a
  dencer drain worker-3 --dry-run
  dencer reclamations
  dencer plan -o json | jq '.plan.steps[] | select(.impact=="Red")'
`

// reorderArgs moves positional arguments after the flags, so that
// `dencer drain node-3 --dry-run` parses the same as
// `dencer drain --dry-run node-3`.
//
// Go's flag package stops parsing at the first non-flag argument, so the
// obvious ordering — the one this tool's own error message demonstrates,
// "e.g. dencer drain worker-3" — fails the moment any flag is added, and
// --context is not optional for anyone with more than one cluster. It fails
// with "which node?" while the node is right there in the command line.
//
// Which flags consume the next argument is a question only the FlagSet can
// answer, so it is asked rather than guessed: boolean flags stand alone,
// everything else takes a value.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			// Everything after it is positional by definition.
			positional = append(positional, args[i+1:]...)
			return joinArgs(flags, positional)
		case len(a) > 1 && a[0] == '-':
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if strings.ContainsRune(name, '=') {
				continue // --flag=value carries its own value
			}
			f := fs.Lookup(name)
			if f == nil {
				continue // unknown flag: let Parse report it properly
			}
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		default:
			positional = append(positional, a)
		}
	}
	return joinArgs(flags, positional)
}

// joinArgs puts the positionals behind an explicit `--`, so Parse treats them
// as arguments whatever they look like — including a node someone has managed
// to name with a leading dash.
func joinArgs(flags, positional []string) []string {
	if len(positional) == 0 {
		return flags
	}
	return append(append(flags, "--"), positional...)
}

type globals struct {
	cfg    cli.Config
	format cli.Format
	// parsed validates flags that FlagSet cannot type-check for us. Called
	// after Parse, before anything talks to the network.
	parsed func() error
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		return nil
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	// --version is not kubectl's spelling, but it is the reflex of everyone
	// who has ever met another CLI, and answering "unknown command" to it is
	// a poor first impression from a tool that drains nodes.
	case "version", "--version":
		fmt.Printf("dencer %s\n", version)
		return nil
	}

	// Ctrl-C cancels the request, not the run. A drain in flight belongs to
	// the executor and keeps going; the watcher says so on the way out.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "plan":
		return cmdPlan(ctx, args)
	case "explain":
		return cmdExplain(ctx, args)
	case "why":
		return cmdWhy(ctx, args)
	case "converge":
		return cmdConverge(ctx, os.Args[2:])
	case "run":
		return cmdRun(ctx, args)
	case "status":
		return cmdStatus(ctx, args)
	case "preflight":
		return cmdPreflight(ctx, os.Args[2:])
	case "audit", "resilience":
		return cmdAudit(ctx, os.Args[2:])
	case "recommend", "recommendations":
		return cmdRecommend(ctx, os.Args[2:])
	case "rightsizing", "rightsize":
		return cmdRightsizing(ctx, os.Args[2:])
	case "whatif":
		return cmdWhatif(ctx, os.Args[2:])
	case "drain":
		return cmdDrain(ctx, os.Args[2:])
	case "reclamations", "reclaim":
		return cmdReclamations(ctx, args)
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}
}

// bind registers the flags every subcommand accepts.
func bind(fs *flag.FlagSet) *globals {
	g := &globals{format: cli.FormatText}
	var format string
	fs.StringVar(&g.cfg.Server, "server", "", "backend base URL")
	fs.StringVar(&g.cfg.Token, "token", "", "bearer token")
	fs.StringVar(&g.cfg.Namespace, "namespace", "k8s-dencer", "release namespace")
	fs.StringVar(&g.cfg.Namespace, "n", "k8s-dencer", "release namespace (shorthand)")
	fs.StringVar(&g.cfg.Release, "release", "k8s-dencer", "Helm release name")
	fs.StringVar(&g.cfg.Kubeconfig, "kubeconfig", "", "kubeconfig path")
	fs.StringVar(&g.cfg.Context, "context", "", "kubeconfig context")
	fs.StringVar(&format, "output", "text", "text, json or yaml")
	fs.StringVar(&format, "o", "text", "output format (shorthand)")
	fs.DurationVar(&g.cfg.Timeout, "timeout", 30*time.Second, "per-request timeout")
	fs.BoolVar(&g.cfg.Insecure, "insecure-skip-tls-verify", false, "skip API server certificate verification")
	fs.Usage = func() { fmt.Print(usage) }

	g.parsed = func() error {
		switch format {
		case "text", "json", "yaml":
			g.format = cli.Format(format)
			return nil
		}
		return fmt.Errorf("unknown output format %q; want text, json or yaml", format)
	}
	return g
}

func connect(ctx context.Context, g *globals) (*cli.Client, error) {
	if err := g.parsed(); err != nil {
		return nil, err
	}
	return cli.Connect(ctx, g.cfg)
}

func cmdPlan(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	g := bind(fs)
	id := fs.String("id", "latest", "plan id, or 'latest'")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()

	env, err := c.Plan(ctx, *id)
	if err != nil {
		return err
	}
	if g.format != cli.FormatText {
		return cli.Encode(os.Stdout, g.format, env)
	}
	// Best-effort: a backend without tracking, or an older one, must not stop
	// the plan printing.
	awaiting := 0
	if rec, err := c.Reclamations(ctx); err == nil && rec.Tracking {
		awaiting = rec.Stats.Awaiting
	}
	cli.PrintPlan(os.Stdout, env, awaiting)
	return nil
}

func cmdExplain(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("explain", flag.ExitOnError)
	g := bind(fs)
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("which step? e.g. dencer explain 3")
	}
	seq, err := cli.ParseSteps(fs.Arg(0))
	if err != nil || len(seq) != 1 {
		return fmt.Errorf("%q is not a single step number", fs.Arg(0))
	}

	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()

	plan, err := c.Plan(ctx, "latest")
	if err != nil {
		return err
	}
	env, err := c.Step(ctx, plan.Plan.ID, seq[0])
	if err != nil {
		return err
	}
	if g.format != cli.FormatText {
		return cli.Encode(os.Stdout, g.format, env)
	}
	cli.PrintStep(os.Stdout, env)
	return nil
}

func cmdWhy(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("why", flag.ExitOnError)
	g := bind(fs)
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("which pod? e.g. dencer why shop/web-7d9f-abcde")
	}
	ns, pod, ok := strings.Cut(fs.Arg(0), "/")
	if !ok || ns == "" || pod == "" {
		return fmt.Errorf("want namespace/pod, got %q", fs.Arg(0))
	}

	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()

	plan, err := c.Plan(ctx, "latest")
	if err != nil {
		return err
	}
	pc, err := c.PodConstraints(ctx, plan.Plan.ID, ns, pod)
	if err != nil {
		return err
	}
	if g.format != cli.FormatText {
		return cli.Encode(os.Stdout, g.format, pc)
	}
	cli.PrintPodConstraints(os.Stdout, pc)
	return nil
}

func cmdConverge(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("converge", flag.ExitOnError)
	g := bind(fs)
	maxNodes := fs.Int("max-nodes", 0, "most nodes this run may drain (required)")
	maxImpact := fs.String("max-impact", "Green", "highest impact executed without a human: Green or Yellow")
	dryRun := fs.Bool("dry-run", false, "rehearse one round without cordoning or evicting")
	watch := fs.Bool("watch", true, "follow the run until it finishes")
	yes := fs.Bool("yes", false, "skip the policy confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *maxNodes < 1 {
		return errors.New("how many nodes may this run drain? e.g. dencer converge --max-nodes 5")
	}
	if *maxImpact != "Green" && *maxImpact != "Yellow" {
		return errors.New("--max-impact must be Green or Yellow; Red always requires a maintenance window")
	}

	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()

	// Context for the consent prompt; the run does not execute this plan.
	plan, _ := c.Plan(ctx, "latest")

	if !*dryRun && !*yes {
		confirmed, err := cli.ConfirmConverge(os.Stdout, os.Stdin, plan, *maxNodes, *maxImpact)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Nothing was run.")
			return nil
		}
	}

	runID, err := c.Converge(ctx, *maxNodes, *maxImpact, *dryRun)
	if err != nil {
		return err
	}
	fmt.Printf("converge run %s queued (up to %d nodes, ceiling %s)", runID, *maxNodes, *maxImpact)
	if *dryRun {
		fmt.Print(" (dry run: one round is rehearsed, nothing is touched)")
	}
	fmt.Println()

	if !*watch {
		fmt.Printf("Follow it with: dencer status --run %s\n", runID)
		return nil
	}

	final, err := c.Wait(ctx, runID, func(ev store.RunEvent) {
		cli.PrintEvent(os.Stdout, ev)
	})
	if err != nil {
		return err
	}

	fmt.Println()
	switch final.Status {
	case store.RunSucceeded:
		fmt.Printf("Succeeded. %s\n", final.Summary)
		return nil
	case store.RunBlocked:
		return fmt.Errorf("blocked by the Safety Guard: %s", final.Summary)
	default:
		return fmt.Errorf("run %s: %s", final.Status, final.Summary)
	}
}

func cmdWhatif(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("whatif", flag.ExitOnError)
	g := bind(fs)
	nodes := fs.String("without-nodes", "", "comma-separated nodes to simulate losing")
	zone := fs.String("without-zone", "", "topology zone to simulate losing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *nodes == "" && *zone == "" {
		return errors.New("remove something: --without-nodes a,b or --without-zone z")
	}
	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()

	env, err := c.Whatif(ctx, splitCommas(*nodes), *zone)
	if err != nil {
		return err
	}
	if g.format != cli.FormatText {
		return cli.Encode(os.Stdout, g.format, env)
	}
	cli.PrintWhatif(os.Stdout, env)
	if !env.Fits {
		return errors.New("the simulated cluster cannot hold its workloads")
	}
	return nil
}

func splitCommas(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cmdRightsizing(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rightsizing", flag.ExitOnError)
	g := bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()
	env, err := c.Rightsizing(ctx)
	if err != nil {
		return err
	}
	if g.format != cli.FormatText {
		return cli.Encode(os.Stdout, g.format, env)
	}
	cli.PrintRightsizing(os.Stdout, env)
	return nil
}

func cmdRecommend(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("recommend", flag.ExitOnError)
	g := bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()
	env, err := c.Recommend(ctx)
	if err != nil {
		return err
	}
	if g.format != cli.FormatText {
		return cli.Encode(os.Stdout, g.format, env)
	}
	cli.PrintRecommendations(os.Stdout, env)
	return nil
}

func cmdAudit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	g := bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()
	env, err := c.Resilience(ctx)
	if err != nil {
		return err
	}
	if g.format != cli.FormatText {
		return cli.Encode(os.Stdout, g.format, env)
	}
	cli.PrintResilience(os.Stdout, env)
	return nil
}

func cmdDrain(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("drain", flag.ExitOnError)
	g := bind(fs)
	dryRun := fs.Bool("dry-run", false, "run every guard check and emit the same events without touching the node")
	watch := fs.Bool("watch", true, "follow the drain until it finishes")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("which node? e.g. dencer drain worker-3")
	}
	node := fs.Arg(0)

	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()

	if !*dryRun && !*yes {
		fmt.Printf("Drain %s through the guard chain? Evicted pods are not restored if this aborts. [y/N] ", node)
		var answer string
		_, _ = fmt.Scanln(&answer)
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Nothing was drained.")
			return nil
		}
	}

	runID, err := c.Drain(ctx, node, *dryRun)
	if err != nil {
		return err
	}
	suffix := ""
	if *dryRun {
		suffix = " (dry run)"
	}
	fmt.Printf("drain of %s queued as run %s%s\n", node, runID, suffix)

	if !*watch {
		fmt.Printf("Follow it with: dencer status --run %s\n", runID)
		return nil
	}
	final, err := c.Wait(ctx, runID, func(ev store.RunEvent) {
		cli.PrintEvent(os.Stdout, ev)
	})
	if err != nil {
		return err
	}
	fmt.Println()
	switch final.Status {
	case store.RunSucceeded:
		fmt.Printf("Succeeded. %s\n", final.Summary)
		return nil
	case store.RunBlocked:
		return fmt.Errorf("blocked by the Safety Guard: %s", final.Summary)
	default:
		return fmt.Errorf("run %s: %s", final.Status, final.Summary)
	}
}

func cmdPreflight(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ExitOnError)
	g := bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()

	env, err := c.Preflight(ctx)
	if err != nil {
		return err
	}
	if g.format != cli.FormatText {
		return cli.Encode(os.Stdout, g.format, env)
	}
	cli.PrintPreflight(os.Stdout, env)
	return nil
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	g := bind(fs)
	steps := fs.String("steps", "", "steps to run, e.g. 1,3-5")
	dryRun := fs.Bool("dry-run", false, "run every guard check and emit the same events without cordoning or evicting")
	watch := fs.Bool("watch", true, "follow the run until it finishes")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *steps == "" {
		return errors.New("which steps? e.g. dencer run --steps 1,3-5")
	}
	want, err := cli.ParseSteps(*steps)
	if err != nil {
		return err
	}

	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()

	plan, err := c.Plan(ctx, "latest")
	if err != nil {
		return err
	}

	// Show what is about to happen before doing it. This tool evicts pods, and
	// "dencer run --steps 1-9" typed in the wrong terminal should not be a
	// silent success.
	if !*dryRun && !*yes {
		confirmed, err := cli.Confirm(os.Stdout, os.Stdin, plan, want)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Nothing was run.")
			return nil
		}
	}

	runID, err := c.Execute(ctx, plan.Plan.ID, want, *dryRun)
	if err != nil {
		return err
	}
	fmt.Printf("run %s queued for steps %v", runID, want)
	if *dryRun {
		fmt.Print(" (dry run: nothing will be cordoned or evicted)")
	}
	fmt.Println()

	if !*watch {
		fmt.Printf("Follow it with: dencer status --run %s\n", runID)
		return nil
	}

	final, err := c.Wait(ctx, runID, func(ev store.RunEvent) {
		cli.PrintEvent(os.Stdout, ev)
	})
	if err != nil {
		return err
	}

	fmt.Println()
	switch final.Status {
	case store.RunSucceeded:
		fmt.Printf("Succeeded. %s\n", final.Summary)
		return nil
	case store.RunBlocked:
		// A guard refusal is the product working, not an error in the usual
		// sense — but it still must not exit 0, or a CI pipeline would treat a
		// refused consolidation as a completed one.
		return fmt.Errorf("blocked by the Safety Guard: %s", final.Summary)
	default:
		return fmt.Errorf("run %s: %s", final.Status, final.Summary)
	}
}

func cmdStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	g := bind(fs)
	runID := fs.String("run", "", "a specific run id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()

	if *runID == "" {
		active, latest, err := c.RunStatus(ctx)
		if err != nil {
			return err
		}
		switch {
		case active != nil:
			*runID = active.ID
		case latest != nil:
			// Nothing in flight, so show the last one and say so. The
			// alternative — "No run in flight." — is true and useless to
			// someone whose drain was halted thirty seconds ago.
			if g.format == cli.FormatText {
				fmt.Println("No run in flight. The most recent one:")
				fmt.Println()
			}
			*runID = latest.ID
		default:
			if g.format != cli.FormatText {
				return cli.Encode(os.Stdout, g.format, map[string]any{"active": nil, "latest": nil})
			}
			fmt.Println("No run in flight, and none has ever run.")
			return nil
		}
	}

	env, err := c.Run(ctx, *runID)
	if err != nil {
		return err
	}
	if g.format != cli.FormatText {
		return cli.Encode(os.Stdout, g.format, env)
	}
	cli.PrintRun(os.Stdout, env)
	return nil
}

func cmdReclamations(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reclamations", flag.ExitOnError)
	g := bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := connect(ctx, g)
	if err != nil {
		return err
	}
	defer c.Close()

	env, err := c.Reclamations(ctx)
	if err != nil {
		return err
	}
	if g.format != cli.FormatText {
		return cli.Encode(os.Stdout, g.format, env)
	}
	cli.PrintReclamations(os.Stdout, env)
	return nil
}
