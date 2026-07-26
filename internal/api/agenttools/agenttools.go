// Package agenttools exposes the plan store to the Kagent agent over MCP.
//
// Read-only by construction, and doubly so: Phase 1 has no executor, and even
// when one exists these tools must not gain a "drain this node" verb. The
// architecture doc is explicit that the agent explains plans and never makes
// or executes them — the planner is deterministic precisely so that plans are
// auditable, and a tool that let the model act would undo that.
//
// Every answer is composed from strings the constraint analyzer and impact
// classifier already produced. The tools quote; they never re-derive. That is
// what keeps the agent's explanation and the UI's constraint inspector from
// drifting into two versions of the truth.
package agenttools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// Server builds the MCP tool surface over a plan store.
type Server struct {
	store   store.Store
	log     *slog.Logger
	version string
}

// New returns a tool server.
func New(s store.Store, log *slog.Logger, version string) *Server {
	return &Server{store: s, log: log, version: version}
}

// Handler returns an http.Handler serving MCP over Streamable HTTP.
//
// Stateless: each request carries everything needed, so Kagent can reconnect
// or scale without session affinity. There is no server-to-client request in
// this surface — the agent asks, we answer — so nothing is lost by it.
func (s *Server) Handler() *mcp.StreamableHTTPHandler {
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.mcpServer() },
		&mcp.StreamableHTTPOptions{
			Stateless: true,
			// Plain JSON rather than SSE: every tool here returns one small
			// result immediately, so a stream buys nothing and gives proxies
			// another thing to buffer.
			JSONResponse: true,
			Logger:       s.log,
		},
	)
}

func (s *Server) mcpServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "k8s-dencer",
		Version: s.version,
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_plan_steps",
		Description: "List the steps in the current node-consolidation plan, with each " +
			"step's impact rating (Green, Yellow or Red) and the node it drains. " +
			"Use this to answer questions about the plan as a whole, such as how " +
			"many nodes it reclaims or how many steps are Red.",
	}, s.listPlanSteps)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "explain_step",
		Description: "Explain one step of the plan: why it is rated Green, Yellow or Red, " +
			"which pods it moves and where, and the constraints affecting those pods. " +
			"Use this whenever asked why a specific step has the rating it does.",
	}, s.explainStep)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_node_constraints",
		Description: "Describe a node: how full it is, which pods it holds, whether the " +
			"plan drains it, and the scheduling constraints on its pods. Use this " +
			"for questions about a specific node.",
	}, s.getNodeConstraints)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "why_not_drained",
		Description: "Explain why a node is NOT being drained by the plan — which pod " +
			"cannot be evicted or relocated, and the specific constraint responsible. " +
			"Use this for questions of the form 'why isn't node X being drained?'.",
	}, s.whyNotDrained)

	return srv
}

// ---------------------------------------------------------------- list_plan_steps

// ListStepsInput takes no arguments; the current plan is always the subject.
type ListStepsInput struct{}

// StepSummary is one row of the plan.
type StepSummary struct {
	Step       int    `json:"step" jsonschema:"the step's sequence number, 1-based"`
	TargetNode string `json:"targetNode" jsonschema:"the node this step drains"`
	Impact     string `json:"impact" jsonschema:"Green, Yellow or Red"`
	PodsMoved  int    `json:"podsMoved"`
	// WindowOnly marks steps policy confines to a maintenance window.
	WindowOnly bool `json:"maintenanceWindowOnly"`
}

// ListStepsOutput is the plan overview.
type ListStepsOutput struct {
	PlanID      string        `json:"planId"`
	GeneratedAt string        `json:"generatedAt"`
	NodesBefore int           `json:"nodesOccupiedBefore"`
	NodesAfter  int           `json:"nodesOccupiedAfter"`
	Reclaimed   int           `json:"nodesReclaimed"`
	Green       int           `json:"greenSteps"`
	Yellow      int           `json:"yellowSteps"`
	Red         int           `json:"redSteps"`
	Steps       []StepSummary `json:"steps"`
	ReadOnly    string        `json:"readOnly"`
}

func (s *Server) listPlanSteps(ctx context.Context, _ *mcp.CallToolRequest, _ ListStepsInput) (*mcp.CallToolResult, ListStepsOutput, error) {
	rec, err := s.store.Latest(ctx)
	if err != nil {
		return s.fail(err)
	}

	ratings := rec.Plan.CountByRating()
	out := ListStepsOutput{
		PlanID:      rec.Plan.ID,
		GeneratedAt: rec.Plan.GeneratedAt.Format("2006-01-02 15:04:05 MST"),
		NodesBefore: rec.Plan.NodesBefore,
		NodesAfter:  rec.Plan.NodesAfter,
		Reclaimed:   rec.Plan.ReclaimedNodes(),
		Green:       ratings[model.ImpactGreen],
		Yellow:      ratings[model.ImpactYellow],
		Red:         ratings[model.ImpactRed],
		ReadOnly:    readOnlyNote,
	}
	for _, step := range rec.Plan.Steps {
		out.Steps = append(out.Steps, StepSummary{
			Step:       step.SequenceNumber,
			TargetNode: step.TargetNode,
			Impact:     string(step.Impact),
			PodsMoved:  len(step.Moves),
			WindowOnly: step.RequiresMaintenanceWindow(),
		})
	}
	return nil, out, nil
}

// ---------------------------------------------------------------- explain_step

// ExplainStepInput identifies a step by its number.
type ExplainStepInput struct {
	Step int `json:"step" jsonschema:"the step's sequence number, as shown by list_plan_steps"`
}

// MoveDetail is one pod relocation.
type MoveDetail struct {
	Pod      string `json:"pod"`
	FromNode string `json:"fromNode"`
	ToNode   string `json:"toNode"`
}

// ConstraintDetail is one constraint, in the analyzer's own words.
type ConstraintDetail struct {
	Pod  string `json:"pod"`
	Kind string `json:"kind"`
	// Blocking marks a constraint that prevents movement outright.
	Blocking bool `json:"blocking"`
	// Explanation is the analyzer's canonical text. Quote it; do not rewrite it.
	Explanation string `json:"explanation"`
}

// ExplainStepOutput is everything needed to answer "why is step N rated that?".
type ExplainStepOutput struct {
	PlanID      string             `json:"planId"`
	Step        int                `json:"step"`
	TargetNode  string             `json:"targetNode"`
	Impact      string             `json:"impact"`
	WindowOnly  bool               `json:"maintenanceWindowOnly"`
	Rationale   string             `json:"rationale" jsonschema:"the canonical explanation for this rating; quote it rather than paraphrasing"`
	Reasons     []string           `json:"contributingFactors"`
	Moves       []MoveDetail       `json:"moves"`
	Constraints []ConstraintDetail `json:"podConstraints"`
	ReadOnly    string             `json:"readOnly"`
}

func (s *Server) explainStep(ctx context.Context, _ *mcp.CallToolRequest, in ExplainStepInput) (*mcp.CallToolResult, ExplainStepOutput, error) {
	rec, err := s.store.Latest(ctx)
	if err != nil {
		return s.failStep(err)
	}

	var step *model.PlanStep
	for i := range rec.Plan.Steps {
		if rec.Plan.Steps[i].SequenceNumber == in.Step {
			step = &rec.Plan.Steps[i]
			break
		}
	}
	if step == nil {
		return errorResult[ExplainStepOutput](fmt.Sprintf(
			"Plan %s has no step %d. It has %d steps, numbered 1 to %d.",
			rec.Plan.ID, in.Step, len(rec.Plan.Steps), len(rec.Plan.Steps)))
	}

	out := ExplainStepOutput{
		PlanID:     rec.Plan.ID,
		Step:       step.SequenceNumber,
		TargetNode: step.TargetNode,
		Impact:     string(step.Impact),
		WindowOnly: step.RequiresMaintenanceWindow(),
		Rationale:  step.Rationale,
		ReadOnly:   readOnlyNote,
	}
	for _, r := range step.Reasons {
		out.Reasons = append(out.Reasons, r.Detail)
	}
	for _, m := range step.Moves {
		out.Moves = append(out.Moves, MoveDetail{
			Pod: m.Namespace + "/" + m.Pod, FromNode: m.FromNode, ToNode: m.ToNode,
		})
		if rec.Analysis == nil {
			continue
		}
		if pc, ok := rec.Analysis.ForPod(m.Namespace + "/" + m.Pod); ok {
			out.Constraints = append(out.Constraints, flatten(pc)...)
		}
	}
	return nil, out, nil
}

// ---------------------------------------------------------------- get_node_constraints

// NodeInput identifies a node by name.
type NodeInput struct {
	Node string `json:"node" jsonschema:"the node's name, for example kwok-node-7"`
}

// NodeOutput describes a node and its pods.
type NodeOutput struct {
	PlanID       string             `json:"planId"`
	Node         string             `json:"node"`
	Zone         string             `json:"zone,omitempty"`
	Ready        bool               `json:"ready"`
	Cordoned     bool               `json:"cordoned"`
	CPURequested string             `json:"cpuRequested"`
	Utilization  string             `json:"utilization"`
	PodCount     int                `json:"podCount"`
	DrainedBy    int                `json:"drainedByStep,omitempty" jsonschema:"the step that drains this node, or 0 if the plan leaves it in place"`
	Impact       string             `json:"drainImpact,omitempty"`
	Constraints  []ConstraintDetail `json:"podConstraints"`
	ReadOnly     string             `json:"readOnly"`
}

func (s *Server) getNodeConstraints(ctx context.Context, _ *mcp.CallToolRequest, in NodeInput) (*mcp.CallToolResult, NodeOutput, error) {
	rec, err := s.store.Latest(ctx)
	if err != nil {
		return s.failNode(err)
	}
	node, ok := rec.Snapshot.NodeByName(in.Node)
	if !ok {
		return errorResult[NodeOutput](fmt.Sprintf(
			"No node named %q in this plan's snapshot. %s", in.Node, nodeHint(rec.Snapshot)))
	}

	requested := rec.Snapshot.RequestedOnNode(node.Name)
	out := NodeOutput{
		PlanID:       rec.Plan.ID,
		Node:         node.Name,
		Zone:         node.Zone(),
		Ready:        node.Ready,
		Cordoned:     node.Unschedulable,
		CPURequested: fmt.Sprintf("%dm of %dm", requested.MilliCPU, node.Allocatable.MilliCPU),
		Utilization:  fmt.Sprintf("%.0f%%", requested.DominantRatio(node.Allocatable)*100),
		PodCount:     len(rec.Snapshot.PodsOnNode(node.Name)),
		ReadOnly:     readOnlyNote,
	}
	for _, step := range rec.Plan.Steps {
		if step.TargetNode == node.Name {
			out.DrainedBy = step.SequenceNumber
			out.Impact = string(step.Impact)
			break
		}
	}
	if rec.Analysis != nil {
		for _, pc := range rec.Analysis.ForNode(node.Name) {
			out.Constraints = append(out.Constraints, flatten(pc)...)
		}
	}
	return nil, out, nil
}

// ---------------------------------------------------------------- why_not_drained

// WhyNotOutput answers the question directly rather than dumping state.
type WhyNotOutput struct {
	PlanID string `json:"planId"`
	Node   string `json:"node"`
	// Answer is a complete sentence suitable for quoting back to the operator.
	Answer    string             `json:"answer"`
	IsDrained bool               `json:"isDrainedByPlan"`
	DrainedBy int                `json:"drainedByStep,omitempty"`
	Blockers  []ConstraintDetail `json:"blockingConstraints"`
	StuckPods []string           `json:"podsWithNowhereToGo"`
	ReadOnly  string             `json:"readOnly"`
}

func (s *Server) whyNotDrained(ctx context.Context, _ *mcp.CallToolRequest, in NodeInput) (*mcp.CallToolResult, WhyNotOutput, error) {
	rec, err := s.store.Latest(ctx)
	if err != nil {
		return s.failWhyNot(err)
	}
	node, ok := rec.Snapshot.NodeByName(in.Node)
	if !ok {
		return errorResult[WhyNotOutput](fmt.Sprintf(
			"No node named %q in this plan's snapshot. %s", in.Node, nodeHint(rec.Snapshot)))
	}

	out := WhyNotOutput{PlanID: rec.Plan.ID, Node: node.Name, ReadOnly: readOnlyNote}

	for _, step := range rec.Plan.Steps {
		if step.TargetNode == node.Name {
			out.IsDrained = true
			out.DrainedBy = step.SequenceNumber
			out.Answer = fmt.Sprintf(
				"%s IS being drained, by step %d (rated %s). %s",
				node.Name, step.SequenceNumber, step.Impact, step.Rationale)
			return nil, out, nil
		}
	}

	// Not a drain target. Work out which of the several possible reasons applies,
	// most specific first — "it holds an unevictable pod" is a far more useful
	// answer than "it is not in the plan".
	if rec.Analysis != nil {
		drainable, blockers := rec.Analysis.NodeDrainable(node.Name)
		for _, b := range blockers {
			out.Blockers = append(out.Blockers, ConstraintDetail{
				Pod: b.Subject, Kind: string(b.Kind), Blocking: b.Blocking, Explanation: b.Explanation,
			})
		}
		for _, pc := range rec.Analysis.ForNode(node.Name) {
			if pc.Movable && len(pc.CandidateNodes) == 0 {
				out.StuckPods = append(out.StuckPods, pc.Key())
			}
		}
		if !drainable && len(out.Blockers) > 0 {
			out.Answer = fmt.Sprintf(
				"%s cannot be drained because %d constraint(s) block it. %s",
				node.Name, len(out.Blockers), out.Blockers[0].Explanation)
			return nil, out, nil
		}
	}

	if node.Unschedulable {
		out.Answer = fmt.Sprintf(
			"%s is cordoned, so the planner excludes it as a drain target — its pods "+
				"cannot be replaced there and nothing new may schedule onto it.", node.Name)
		return nil, out, nil
	}
	if !node.Ready {
		out.Answer = fmt.Sprintf(
			"%s is not Ready. The planner skips unready nodes: their pods are already "+
				"the scheduler's problem, not a consolidation decision.", node.Name)
		return nil, out, nil
	}
	if _, isControlPlane := controlPlaneLabel(node); isControlPlane {
		out.Answer = fmt.Sprintf(
			"%s is a control-plane node and is excluded by policy. Draining the API "+
				"server out from under the cluster is not consolidation.", node.Name)
		return nil, out, nil
	}
	if rec.Snapshot.RequestedOnNode(node.Name).IsZero() {
		out.Answer = fmt.Sprintf(
			"%s holds no workload, so there is nothing to drain — it is already "+
				"reclaimable.", node.Name)
		return nil, out, nil
	}

	out.Answer = fmt.Sprintf(
		"%s is not drained by this plan. It is most likely serving as a destination "+
			"for pods moved off other nodes, or the planner could not find room for its "+
			"pods elsewhere. Use get_node_constraints for the detail.", node.Name)
	return nil, out, nil
}

// ---------------------------------------------------------------- helpers

const readOnlyNote = "k8s-dencer only plans and explains. It cannot drain, cordon or evict, " +
	"and has no permission to do so."

func flatten(pc constraints.PodConstraints) []ConstraintDetail {
	out := make([]ConstraintDetail, 0, len(pc.Constraints))
	for _, c := range pc.Constraints {
		out = append(out, ConstraintDetail{
			Pod:         pc.Key(),
			Kind:        string(c.Kind),
			Blocking:    c.Blocking,
			Explanation: c.Explanation,
		})
	}
	return out
}

func controlPlaneLabel(n model.Node) (string, bool) {
	for _, key := range []string{
		"node-role.kubernetes.io/control-plane",
		"node-role.kubernetes.io/master",
	} {
		if _, ok := n.Labels[key]; ok {
			return key, true
		}
	}
	return "", false
}

// nodeHint lists a few real node names, so a model that guessed a name can
// correct itself instead of guessing again.
func nodeHint(snap *model.ClusterSnapshot) string {
	if snap == nil || len(snap.Nodes) == 0 {
		return "The snapshot contains no nodes."
	}
	names := make([]string, 0, len(snap.Nodes))
	for _, n := range snap.Nodes {
		names = append(names, n.Name)
	}
	sort.Strings(names)
	if len(names) > 5 {
		return fmt.Sprintf("Known nodes include: %s (and %d more).",
			strings.Join(names[:5], ", "), len(names)-5)
	}
	return "Known nodes: " + strings.Join(names, ", ") + "."
}

// errorResult returns a tool-level error rather than a protocol error, so the
// model sees the explanation and can correct itself instead of the call simply
// failing.
func errorResult[T any](msg string) (*mcp.CallToolResult, T, error) {
	var zero T
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}, zero, nil
}

func (s *Server) fail(err error) (*mcp.CallToolResult, ListStepsOutput, error) {
	return errorResult[ListStepsOutput](s.storeMessage(err))
}
func (s *Server) failStep(err error) (*mcp.CallToolResult, ExplainStepOutput, error) {
	return errorResult[ExplainStepOutput](s.storeMessage(err))
}
func (s *Server) failNode(err error) (*mcp.CallToolResult, NodeOutput, error) {
	return errorResult[NodeOutput](s.storeMessage(err))
}
func (s *Server) failWhyNot(err error) (*mcp.CallToolResult, WhyNotOutput, error) {
	return errorResult[WhyNotOutput](s.storeMessage(err))
}

func (s *Server) storeMessage(err error) string {
	if errors.Is(err, store.ErrNotFound) {
		return "No consolidation plan has been produced yet. The planner publishes one " +
			"once it has read the cluster, usually within a minute of starting."
	}
	s.log.Error("agent tool failed", "error", err)
	return "The plan store could not be read. This is a k8s-dencer fault, not a " +
		"problem with the question."
}
