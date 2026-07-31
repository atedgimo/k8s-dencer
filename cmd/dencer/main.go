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
  version

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
  dencer reclamations
  dencer plan -o json | jq '.plan.steps[] | select(.impact=="Red")'
`

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
	case "version":
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
	if err := fs.Parse(args); err != nil {
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
	if err := fs.Parse(args); err != nil {
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
		active, err := c.ActiveRun(ctx)
		if err != nil {
			return err
		}
		if active == nil {
			if g.format != cli.FormatText {
				return cli.Encode(os.Stdout, g.format, map[string]any{"active": nil})
			}
			fmt.Println("No run in flight.")
			return nil
		}
		*runID = active.ID
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
