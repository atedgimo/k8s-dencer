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
