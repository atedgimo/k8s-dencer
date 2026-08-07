package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Confirm shows what is about to be evicted and asks.
//
// This is the single most safety-relevant piece of CLI code — the last thing
// standing between "dencer run typed in the wrong terminal" and evictions —
// and it lived in cmd/dencer at zero test coverage while the pretty-printers
// around it were tested. Moved here, with the reader and writer as
// parameters, so a test can script the conversation.
//
// Anything that is not an explicit yes is a no. EOF — a closed stdin, a
// ctrl-d, a pipeline that forgot --yes — is a no, not an error: refusing to
// act is this function's safe default, never a failure mode that needs
// handling above it.
func Confirm(out io.Writer, in io.Reader, plan *PlanEnvelope, want []int) (bool, error) {
	fmt.Fprintf(out, "About to run %d step(s) against plan %s:\n\n", len(want), plan.Plan.ID)
	moves := 0
	red := 0
	for _, s := range plan.Plan.Steps {
		for _, n := range want {
			if s.SequenceNumber != n {
				continue
			}
			fmt.Fprintf(out, "  step %d  %-6s  drain %s (%d pods)\n",
				s.SequenceNumber, s.Impact, s.TargetNode, len(s.Moves))
			moves += len(s.Moves)
			if s.Impact == "Red" {
				red++
			}
		}
	}
	fmt.Fprintf(out, "\n%d pod(s) will be evicted through the eviction API.\n", moves)
	if red > 0 {
		fmt.Fprintf(out, "%d step(s) are Red and will be refused unless a MaintenanceWindow is open.\n", red)
	}
	fmt.Fprint(out, "\nContinue? [y/N] ")

	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		// EOF before any input: no one is there to consent.
		return false, nil
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

// ConfirmConverge is the consent prompt for a closed-loop run, and it is
// deliberately not Confirm with different text. A steps run approves a
// concrete list an operator can picture; a converge run approves a POLICY —
// the executor will re-plan after every drain and pick its own targets — and
// the prompt must make that difference impossible to miss. What is shown is
// the current plan's outlook as context, explicitly labelled as non-binding.
func ConfirmConverge(out io.Writer, in io.Reader, plan *PlanEnvelope, maxNodes int, maxImpact string) (bool, error) {
	fmt.Fprintf(out, "You are approving a policy, not a list of steps.\n\n")
	fmt.Fprintf(out, "The executor will repeatedly: observe the cluster, plan ONE drain against\n")
	fmt.Fprintf(out, "live state, run the full Safety Guard, drain, wait for recovery — until no\n")
	fmt.Fprintf(out, "worthwhile step remains or a bound below is reached.\n\n")
	fmt.Fprintf(out, "  bound: at most %d node(s) drained\n", maxNodes)
	fmt.Fprintf(out, "  bound: nothing rated above %s is executed\n", maxImpact)
	fmt.Fprintf(out, "  rail:  every round must free a node, or the run stops\n")
	fmt.Fprintf(out, "  rail:  no node is drained twice in one run\n")
	if plan != nil && plan.Plan != nil {
		fmt.Fprintf(out, "\nFor context only (targets are re-chosen live): the current plan frees %d node(s).\n",
			plan.Plan.ReclaimedNodes())
	}
	fmt.Fprint(out, "\nApprove this policy? [y/N] ")

	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		return false, nil
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

// Ask is a plain yes/no prompt for actions that need no envelope explaining.
//
// Same doctrine as the two above: anything that is not an explicit yes is a
// no, and EOF — a closed stdin, a pipeline that forgot --yes — is a no rather
// than an error. Refusing to act on silence is the safe direction.
func Ask(out io.Writer, in io.Reader, question string) (bool, error) {
	fmt.Fprintf(out, "%s [y/N] ", question)
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		return false, nil
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}
