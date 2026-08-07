package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// Colour is off unless the output is a terminal that wants it.
//
// The rule the UI established holds here too: colour means risk and nothing
// else. It is also never the only carrier — every rating prints its glyph and
// its word, so the output survives a pipe, a CI log, a monochrome terminal and
// a reader who cannot distinguish red from green.
var (
	colour = wantsColour()
	green  func(string) string
	yellow func(string) string
	red    func(string) string
	dim    func(string) string
	bold   func(string) string
)

func init() { resetPainters() }

// resetPainters rebinds the colour helpers to the current value of colour.
// Exists so a test can turn colour off and check that the glyph and the word
// still carry the rating on their own.
func resetPainters() {
	green = paint("\033[32m")
	yellow = paint("\033[33m")
	red = paint("\033[31m")
	dim = paint("\033[2m")
	bold = paint("\033[1m")
}

func wantsColour() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func paint(code string) func(string) string {
	return func(s string) string {
		if !colour {
			return s
		}
		return code + s + "\033[0m"
	}
}

// glyph and word for an impact rating.
//
// ● ▲ ■ rather than three coloured dots: deuteranopia collapses the red/green
// distinction, and the glyphs differ in shape as well as hue. The word is
// always printed too.
func impactMark(i model.ImpactRating) string {
	switch i {
	case model.ImpactGreen:
		return green("● Green ")
	case model.ImpactYellow:
		return yellow("▲ Yellow")
	case model.ImpactRed:
		return red("■ Red   ")
	default:
		return string(i)
	}
}

// Format selects the output encoding.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
	FormatYAML Format = "yaml"
)

// Encode writes v in the requested machine-readable format.
func Encode(w io.Writer, f Format, v any) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case FormatYAML:
		b, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	return fmt.Errorf("unknown format %q", f)
}

// reclaimSummary states what the reclaimable count is worth. A node count
// alone cannot say whether a plan matters — 15 nodes may be a rack of 96-core
// machines or a drawer of 2-core ones. Older servers do not send the capacity
// fields; a bare count is then the honest fallback, not a zero.
func reclaimSummary(env *PlanEnvelope) string {
	n := env.Plan.ReclaimedNodes()
	if env.CPUReclaimableMilli <= 0 {
		return fmt.Sprintf("%d reclaimable", n)
	}
	out := fmt.Sprintf("%d reclaimable (%s cores, %s",
		n, formatMilli(env.CPUReclaimableMilli), formatBytes(env.MemReclaimableBytes))
	if spot, od := env.ReclaimableByCapacity["spot"], env.ReclaimableByCapacity["on-demand"]; spot > 0 || od > 0 {
		out += fmt.Sprintf("; %d spot, %d on-demand", spot, od)
	}
	return out + ")"
}

// PrintPlan renders a plan as a table.
func PrintPlan(w io.Writer, env *PlanEnvelope, awaiting int) {
	p := env.Plan
	age := time.Since(p.GeneratedAt).Round(time.Second)

	fmt.Fprintf(w, "%s  %s\n", bold("plan "+p.ID), dim(fmt.Sprintf("%s old, %s", age, env.Strategy)))
	fmt.Fprintf(w, "%d nodes now, %d after, %s\n\n",
		p.NodesBefore, p.NodesAfter, bold(reclaimSummary(env)))

	if len(p.Steps) == 0 {
		fmt.Fprintln(w, "No steps. Nothing to consolidate — every node is either needed or undrainable.")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STEP\tIMPACT\tDRAINS\tPODS\tWHY")
	for _, s := range p.Steps {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\n",
			s.SequenceNumber, impactMark(s.Impact), s.TargetNode, len(s.Moves), truncate(s.Rationale, 58))
	}
	tw.Flush()

	// Lead with the action, as the UI does: the operator's next move is the
	// point of the page, not the inventory above it.
	if next := firstRunnable(p); next != nil {
		fmt.Fprintf(w, "\n%s\n  dencer run --steps %d\n",
			bold("Next:")+fmt.Sprintf(" drain %s (%s)", next.TargetNode, strings.TrimSpace(stripANSI(impactMark(next.Impact)))),
			next.SequenceNumber)
	}
	if n := countRed(p); n > 0 {
		fmt.Fprintf(w, "\n%s %d step(s) are Red and need an open MaintenanceWindow.\n", red("■"), n)
	}
	if awaiting > 0 {
		// Worth saying on this page rather than only under `reclamations`:
		// planning to drain more nodes while previously drained ones were
		// never removed is the situation an operator most needs pointed out.
		fmt.Fprintf(w, "\n%s %d previously drained node(s) are still awaiting reclamation.\n",
			yellow("▲"), awaiting)
		fmt.Fprintln(w, "  dencer reclamations")
	}
}

// firstRunnable is the first step that does not need a maintenance window.
func firstRunnable(p *model.Plan) *model.PlanStep {
	for i := range p.Steps {
		if p.Steps[i].Impact != model.ImpactRed {
			return &p.Steps[i]
		}
	}
	return nil
}

func countRed(p *model.Plan) int {
	n := 0
	for _, s := range p.Steps {
		if s.Impact == model.ImpactRed {
			n++
		}
	}
	return n
}

// PrintStep renders one step and why it is rated as it is.
func PrintStep(w io.Writer, env *StepEnvelope) {
	s := env.Step
	fmt.Fprintf(w, "%s  %s\n", bold(fmt.Sprintf("step %d", s.SequenceNumber)), impactMark(s.Impact))
	fmt.Fprintf(w, "drains %s, moving %d pod(s)\n\n", s.TargetNode, len(s.Moves))
	fmt.Fprintf(w, "%s\n", s.Rationale)

	if len(s.Reasons) > 0 {
		fmt.Fprintf(w, "\n%s\n", bold("Because"))
		for _, r := range s.Reasons {
			line := "  - " + r.Kind
			if r.Subject != "" {
				line += " " + r.Subject
			}
			if r.Detail != "" {
				line += ": " + r.Detail
			}
			fmt.Fprintln(w, line)
		}
	}

	if len(s.Moves) > 0 {
		fmt.Fprintf(w, "\n%s\n", bold("Moves"))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  POD\tFROM\tTO")
		for _, m := range s.Moves {
			fmt.Fprintf(tw, "  %s/%s\t%s\t%s\n", m.Namespace, m.Pod, m.FromNode, m.ToNode)
		}
		tw.Flush()
	}

	blocked := 0
	for _, pc := range env.Constraints {
		if !pc.Movable {
			blocked++
		}
	}
	if blocked > 0 {
		fmt.Fprintf(w, "\n%s %d of the pods on this node cannot move. Run 'dencer why <ns>/<pod>' for one.\n",
			yellow("▲"), blocked)
	}
}

// PrintPodConstraints explains one pod.
func PrintPodConstraints(w io.Writer, pc *constraints.PodConstraints) {
	verdict := green("● can move")
	if !pc.Movable {
		verdict = red("■ cannot move")
	}
	fmt.Fprintf(w, "%s  %s\n", bold(pc.Key()), verdict)
	if pc.NodeName != "" {
		fmt.Fprintf(w, "on %s\n", pc.NodeName)
	}

	// Constraint.Explanation is described in the analyzer as "the single
	// canonical human-readable description", and a test already asserts the
	// Kagent agent reproduces it byte-for-byte. The same rule applies here: if
	// the CLI paraphrased, the CLI, the UI and the agent could describe one
	// constraint three ways and an operator would have no way to tell which to
	// believe. So the explanation is printed as-is.
	blockers := pc.Blockers()
	if len(blockers) > 0 {
		fmt.Fprintf(w, "\n%s\n", bold("Blocked by"))
		for _, c := range blockers {
			fmt.Fprintf(w, "  %s %s\n", red("■"), constraintLine(c))
		}
	}

	var informational []constraints.Constraint
	for _, c := range pc.Constraints {
		if !c.Blocking {
			informational = append(informational, c)
		}
	}
	if len(informational) > 0 {
		fmt.Fprintf(w, "\n%s\n", bold("Also constrained by"))
		for _, c := range informational {
			fmt.Fprintf(w, "  %s %s\n", dim("·"), constraintLine(c))
		}
	}

	fmt.Fprintf(w, "\n%d node(s) could take it", len(pc.CandidateNodes))
	if pc.Movable && len(pc.CandidateNodes) == 0 {
		// The distinction the analyzer draws and a summary would lose: nothing
		// forbids this pod moving, there is simply nowhere to put it.
		fmt.Fprintf(w, " — %s: nothing forbids moving it, but nowhere has room", yellow("effectively stuck"))
	}
	fmt.Fprintln(w)
}

// PrintRun renders a run and its audit trail.
func PrintRun(w io.Writer, env *RunEnvelope) {
	r := env.Run
	fmt.Fprintf(w, "%s  %s\n", bold("run "+r.ID), statusMark(r.Status))
	fmt.Fprintf(w, "plan %s, steps %v, by %s\n", r.PlanID, r.Steps, r.Actor)
	if r.Summary != "" {
		fmt.Fprintf(w, "%s\n", r.Summary)
	}
	if len(env.Events) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", bold("Audit trail"))
	for _, ev := range env.Events {
		PrintEvent(w, ev)
	}
}

// PrintEvent renders one audit event.
func PrintEvent(w io.Writer, ev store.RunEvent) {
	mark := " "
	switch ev.Level {
	case store.EventBlocked:
		mark = yellow("▲")
	case store.EventError:
		mark = red("■")
	}
	where := ev.Node
	if ev.Pod != "" {
		where = ev.Pod
	}
	rule := ""
	if ev.Rule != "" {
		rule = " [" + ev.Rule + "]"
	}
	fmt.Fprintf(w, "  %s %s %-8s %-28s %s%s\n",
		mark, dim(ev.At.Format("15:04:05")), ev.Action, truncate(where, 28), ev.Message, dim(rule))
}

// constraintLine renders a constraint without rewording it.
func constraintLine(c constraints.Constraint) string {
	head := string(c.Kind)
	if c.Subject != "" {
		head += " " + c.Subject
	}
	if c.Explanation == "" {
		return head
	}
	return head + ": " + c.Explanation
}

func statusMark(s store.RunStatus) string {
	switch s {
	case store.RunSucceeded:
		return green("Succeeded")
	case store.RunFailed:
		return red("Failed")
	case store.RunBlocked:
		return yellow("Blocked")
	}
	return string(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// stripANSI removes colour codes, for when a coloured fragment is reused
// inside a sentence that measures its own width.
func stripANSI(s string) string {
	for {
		i := strings.Index(s, "\033[")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+1:]
	}
}

// PrintReclamations renders what actually became of drained nodes.
//
// The awaiting list comes first and is the point. Those are nodes this product
// told someone to drain, which nothing has removed — capacity that is
// unavailable and still being paid for. Everything else on this page is
// reassurance; that list is the bill.
func PrintReclamations(w io.Writer, env *ReclamationsEnvelope) {
	if !env.Tracking {
		fmt.Fprintln(w, "Reclamation tracking is not available on this backend.")
		return
	}
	s := env.Stats
	now := time.Now()

	if len(env.Awaiting) == 0 && s.Reclaimed == 0 && s.Returned == 0 {
		fmt.Fprintln(w, "No nodes have been drained yet.")
		return
	}

	if len(env.Awaiting) > 0 {
		fmt.Fprintf(w, "%s\n", bold("Awaiting reclamation"))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  NODE\tDRAINED\tRUN")
		for _, r := range env.Awaiting {
			age := r.Age(now)
			mark := "  "
			// A node that has sat for a day is not waiting for an autoscaler
			// that is about to act. Something is not coming, and the operator
			// is paying for a machine doing nothing.
			if age > 24*time.Hour {
				mark = yellow("▲ ")
			}
			fmt.Fprintf(tw, "%s%s\t%s ago\t%s\n", mark, r.Node, humanDuration(age), r.RunID)
		}
		tw.Flush()

		if stale := countOlderThan(env.Awaiting, 24*time.Hour, now); stale > 0 {
			fmt.Fprintf(w, "\n%s %d node(s) drained over a day ago and still present.\n",
				yellow("▲"), stale)
			fmt.Fprintln(w, "  Draining frees capacity; something else has to remove the machine.")
			fmt.Fprintln(w, "  If nothing is going to, uncordon them: kubectl uncordon <node>")
		}
		fmt.Fprintln(w)
	}

	if r := env.Reclaimer; r != nil && !r.ObservedWorking {
		if r.Detected != "" {
			fmt.Fprintf(w, "%s %s is present but has never been observed removing a node here.\n\n",
				yellow("▲"), r.Detected)
		} else {
			fmt.Fprintf(w, "%s No node removal has ever been observed here, and no autoscaler is visible.\n", yellow("▲"))
			fmt.Fprintln(w, "  (A managed control plane's autoscaler would be invisible to this check.)")
			fmt.Fprintln(w, "  Until something removes them, drained nodes stay allocated — and billed.")
			fmt.Fprintln(w)
		}
	}

	fmt.Fprintf(w, "%s (last %d days)\n", bold("Observed"), s.WindowDays)
	fmt.Fprintf(w, "  %s reclaimed", green(fmt.Sprintf("%d", s.Reclaimed)))
	if s.Reclaimed > 0 && s.MedianReclamationSeconds > 0 {
		fmt.Fprintf(w, ", median %s", humanDuration(time.Duration(s.MedianReclamationSeconds)*time.Second))
	}
	fmt.Fprintln(w)
	if s.Returned > 0 {
		fmt.Fprintf(w, "  %d returned to service instead\n", s.Returned)
	}

	// The ledger: the one savings figure in this product that is measured
	// rather than estimated, summed from capacity captured at drain time.
	if s.ReclaimedCPUMilli > 0 || s.ReclaimedMemBytes > 0 {
		fmt.Fprintf(w, "\n%s %s cores, %s actually returned\n",
			bold("Ledger:"), formatMilli(s.ReclaimedCPUMilli), formatBytes(s.ReclaimedMemBytes))
		if s.UncountedNodes > 0 {
			fmt.Fprintf(w, "  (+%d node(s) reclaimed before capacity was recorded; their savings are real but unmeasured)\n",
				s.UncountedNodes)
		}
	}

	// The rate, not a total: reclaiming a node saves money every hour from
	// now on, and the cumulative figure is small and unimpressive on day one
	// while the rate is the actual result.
	if p := s.Pricing; p != nil && p.PricedNodes > 0 {
		fmt.Fprintf(w, "  %s %s/month no longer spent, across %d node(s)\n",
			bold("Worth:"), money(p.Currency, p.PerMonth), p.PricedNodes)
		if p.UnpricedNodes > 0 {
			fmt.Fprintf(w, "  (%d node(s) unpriced — add their machine type to uiBackend.pricing)\n",
				p.UnpricedNodes)
		}
	}

	// Someone else's work, reported without being claimed. Silence here once
	// let a fleet shrink from six nodes to four while this command said "No
	// nodes have been drained yet".
	if s.ExternallyReclaimed > 0 {
		fmt.Fprintf(w, "\n  %d node(s) left the cluster without k8s-dencer draining them —\n", s.ExternallyReclaimed)
		fmt.Fprintf(w, "  an autoscaler, or someone with kubectl. Not counted as this tool's doing.\n")
		if p := s.Pricing; p != nil && p.ExternalPriced > 0 {
			fmt.Fprintf(w, "  (worth %s/month, which the cluster saved and this tool did not)\n",
				money(p.Currency, p.ExternalPerMonth))
		}
	}
}

// money formats a cost with the operator's own currency label. No symbol
// lookup: they said "USD" or "EUR" and echoing it back is safer than guessing
// which glyph they meant.
func money(currency string, v float64) string {
	if currency == "" {
		return fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("%.2f %s", v, currency)
}

func countOlderThan(rs []store.Reclamation, d time.Duration, now time.Time) int {
	n := 0
	for _, r := range rs {
		if r.Age(now) > d {
			n++
		}
	}
	return n
}

// humanDuration prefers coarse units. "3d" reads faster than "76h12m9s", and
// nobody chasing a stuck reclamation cares about the seconds.
func humanDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

func formatMilli(milli int64) string {
	if milli%1000 == 0 {
		return fmt.Sprintf("%d", milli/1000)
	}
	return fmt.Sprintf("%.1f", float64(milli)/1000)
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d %s", int64(v), units[exp])
	}
	return fmt.Sprintf("%.1f %s", v, units[exp])
}

// PrintPreflight renders per-node drainability — the answer to "will my
// node-pool rotation wedge?", given before anything is touched instead of
// discovered mid-upgrade via a stuck PDB.
//
// Blocked nodes come first and get the detail; they are the reason anyone
// runs a preflight. Drainable nodes are a one-line reassurance.
func PrintPreflight(w io.Writer, env *PreflightEnvelope) {
	fmt.Fprintf(w, "%s  %s\n\n",
		bold(fmt.Sprintf("%d of %d nodes will drain cleanly", env.Drainable, env.Total)),
		dim("as of "+humanDuration(time.Since(env.TakenAt))+" ago"))

	blocked := 0
	for _, n := range env.Nodes {
		if n.Drainable {
			continue
		}
		blocked++
		fmt.Fprintf(w, "%s %s  (%d pods)\n", red("■"), bold(n.Node), n.Pods)
		for _, b := range n.Blockers {
			fmt.Fprintf(w, "    %s  %s\n", b.Pod, dim("["+b.Kind+"]"))
			fmt.Fprintf(w, "      %s\n", b.Explanation)
		}
	}
	if blocked == 0 {
		fmt.Fprintln(w, "Every node can be drained. A rolling node replacement will not wedge on today's state.")
		return
	}
	fmt.Fprintf(w, "\nA rotation will stall at %d node(s) unless the blockers above are resolved first.\n", blocked)
	fmt.Fprintln(w, "State changes; re-run this immediately before upgrading.")
}

// PrintResilience renders the availability audit: what cannot survive a node
// loss. The same analysis consolidation runs on, read in the opposite mood —
// the PDB that blocks a voluntary drain is the PDB a dying node violates.
func PrintResilience(w io.Writer, env *ResilienceEnvelope) {
	if len(env.Findings) == 0 {
		fmt.Fprintf(w, "%s\n", bold("No availability findings."))
		fmt.Fprintf(w, "Every pod can be evicted and every PodDisruptionBudget has headroom (%d pods analysed).\n", env.Pods)
		return
	}

	fmt.Fprintf(w, "%s  %s\n\n",
		bold(fmt.Sprintf("%d availability finding(s)", len(env.Findings))),
		dim(fmt.Sprintf("across %d pods, as of %s ago", env.Pods, humanDuration(time.Since(env.TakenAt)))))

	kind := ""
	for _, f := range env.Findings {
		if f.Kind != kind {
			kind = f.Kind
			fmt.Fprintf(w, "%s\n", bold(kind))
		}
		where := f.Pod
		if f.Node != "" {
			where += dim(" on " + f.Node)
		}
		fmt.Fprintf(w, "  %s %s\n    %s\n", yellow("▲"), where, f.Explanation)
	}
	fmt.Fprintln(w, "\nEach finding names a pod a node loss would hurt — voluntarily drained or not.")
}

// PrintWhatif renders a loss simulation. The verdict leads; the homeless
// pods, when any, are the entire point of asking.
func PrintWhatif(w io.Writer, env *WhatifEnvelope) {
	fmt.Fprintf(w, "Simulated losing %d node(s): %s\n", len(env.Removed), strings.Join(env.Removed, ", "))
	fmt.Fprintf(w, "%d pod(s) displaced.\n\n", env.Displaced)
	if env.Fits {
		fmt.Fprintf(w, "%s\n", bold(green("Everything fits.")))
		fmt.Fprintln(w, "The constraint engine found every displaced pod a legal home on the surviving nodes.")
		fmt.Fprintln(w, dim("A fit here is the engine's answer, not a promise about the scheduler on the day."))
		return
	}
	fmt.Fprintf(w, "%s\n", bold(red(fmt.Sprintf("%d pod(s) would have nowhere legal to go:", len(env.Homeless)))))
	for _, h := range env.Homeless {
		fmt.Fprintf(w, "  %s %s\n", red("■"), h.Pod)
		for _, why := range h.Why {
			fmt.Fprintf(w, "      %s\n", why)
		}
	}
}

// PrintRightsizing renders requests versus observed usage. Consolidation
// packs requests, so oversized requests hold capacity no drain can free —
// the top of this list is where fixing a number frees real nodes.
func PrintRightsizing(w io.Writer, env *RightsizingEnvelope) {
	if !env.Available {
		fmt.Fprintln(w, "No usage data.")
		fmt.Fprintf(w, "  %s\n", env.Reason)
		return
	}
	fmt.Fprintf(w, "%s  %s\n\n",
		bold(fmt.Sprintf("Cluster requests %s cores, observed use %s cores",
			formatMilli(env.TotalRequestedMilli), formatMilli(env.TotalUsedMilli))),
		dim("as of "+humanDuration(time.Since(env.TakenAt))+" ago"))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKLOAD\tPODS\tCPU REQ\tCPU USED\tMEM REQ\tMEM USED")
	n := 0
	for _, r := range env.Workloads {
		if n++; n > 15 {
			break
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
			r.Workload, r.Pods,
			formatMilli(r.RequestedMilli), formatMilli(r.UsedMilli),
			formatBytes(r.RequestedBytes), formatBytes(r.UsedBytes))
	}
	tw.Flush()
	if len(env.Workloads) > 15 {
		fmt.Fprintf(w, "…and %d more (use -o json for all)\n", len(env.Workloads)-15)
	}
	fmt.Fprintln(w, dim("\nSorted by absolute CPU excess. Usage is a point-in-time sample, not a peak;"))
	fmt.Fprintln(w, dim("shrink requests against your own percentiles, not against this single reading."))
}

// PrintRecommendations renders fixes, most consequential first. Severity is
// impact-on-consolidation, not risk — words, not the rating glyphs.
func PrintRecommendations(w io.Writer, env *RecommendationsEnvelope) {
	if len(env.Recommendations) == 0 {
		fmt.Fprintln(w, bold("Nothing to recommend."))
		fmt.Fprintln(w, "Every workload has requests, replicas and a governed disruption budget.")
		return
	}
	fmt.Fprintf(w, "%s  %s\n\n",
		bold(fmt.Sprintf("%d recommendation(s)", len(env.Recommendations))),
		dim("as of "+humanDuration(time.Since(env.TakenAt))+" ago"))
	for _, r := range env.Recommendations {
		fmt.Fprintf(w, "%s %s  %s\n", strings.ToUpper(r.Severity), bold(r.Workload), dim("["+r.Kind+"]"))
		fmt.Fprintf(w, "  %s\n", r.Why)
		if r.Fix != "" {
			for _, line := range strings.Split(r.Fix, "\n") {
				fmt.Fprintf(w, "    %s\n", dim(line))
			}
		}
		fmt.Fprintln(w)
	}
}
