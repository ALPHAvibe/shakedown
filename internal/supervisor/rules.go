package supervisor

import (
	"context"
	"fmt"

	"github.com/ALPHAvibe/shakedown/internal/graph"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

// Supervisor chooses the next edge or abstains so a later layer can decide.
type Supervisor interface {
	Decide(ctx context.Context, in run.DecideInput) (run.Decision, error)
}

// Chain returns the first confident decision.
type Chain struct {
	Layers []Supervisor
}

func (c Chain) Decide(ctx context.Context, in run.DecideInput) (run.Decision, error) {
	var notes []string
	for _, layer := range c.Layers {
		d, err := layer.Decide(ctx, in)
		if err != nil {
			return run.Decision{}, err
		}
		if d.Confident() {
			if d.Execution == "" {
				d.Execution = in.Result.ExecutionID
			}
			return d, nil
		}
		if d.Reason != "" {
			backend := d.Backend
			if backend == "" {
				backend = "supervisor"
			}
			notes = append(notes, backend+": "+d.Reason)
		}
	}
	reason := "no supervisor matched"
	if len(notes) > 0 {
		reason = reason + "; " + joinNotes(notes)
	}
	return run.Decision{
		Decision:  run.DecisionStop,
		Reason:    reason,
		Backend:   "chain",
		Execution: in.Result.ExecutionID,
	}, nil
}

func joinNotes(notes []string) string {
	out := notes[0]
	for _, note := range notes[1:] {
		out += "; " + note
	}
	return out
}

const (
	matchOne       = "one"
	matchNone      = "none"
	matchAmbiguous = "ambiguous"
)

// MatchEdges applies ordered when conditions. One matching conditional wins.
// Several matching conditionals are ambiguous. A default edge is used only
// when no conditional matches.
func MatchEdges(edges []graph.Edge, scope map[string]any) (graph.Edge, string, error) {
	var defaults []graph.Edge
	var matched []graph.Edge
	for _, edge := range edges {
		if edge.When == "" {
			defaults = append(defaults, edge)
			continue
		}
		ok, err := graph.Eval(edge.When, scope)
		if err != nil {
			return graph.Edge{}, "", err
		}
		if ok {
			matched = append(matched, edge)
		}
	}
	switch len(matched) {
	case 1:
		return matched[0], matchOne, nil
	case 0:
		if len(defaults) > 0 {
			return defaults[0], matchOne, nil
		}
		return graph.Edge{}, matchNone, nil
	default:
		return graph.Edge{}, matchAmbiguous, nil
	}
}

// Rules is the deterministic supervisor layer.
type Rules struct{}

func (Rules) Decide(_ context.Context, in run.DecideInput) (run.Decision, error) {
	if in.Result.Area == "" {
		in.Result.Area = in.Node.Area
	}
	scope := in.Result.Scope(in.Inputs)
	if in.Result.Failed() {
		sameLimit := in.SameExecutionFailures
		if sameLimit <= 0 {
			sameLimit = 2
		}
		same := countFailures(in.History, in.Result, func(res run.Result) bool {
			return res.ExecutionID == in.Result.ExecutionID
		})
		if same >= sameLimit {
			return stopDecision(in, fmt.Sprintf("same execution failed %d times", same)), nil
		}
		if in.Node.Area != "" {
			areaLimit := in.AreaFailures
			if areaLimit <= 0 {
				areaLimit = 3
			}
			area := countFailures(in.History, in.Result, func(res run.Result) bool {
				return res.Area == in.Node.Area
			})
			if area >= areaLimit {
				return stopDecision(in, fmt.Sprintf("%d failures in area %s", area, in.Node.Area)), nil
			}
		}
	}
	for _, rule := range in.Rules {
		ok, err := ruleMatches(rule, in, scope)
		if err != nil {
			return run.Decision{}, err
		}
		if !ok {
			continue
		}
		reason := rule.Reason
		if reason == "" {
			reason = rule.Name
		}
		return run.Decision{
			Decision:  rule.Decision,
			Edge:      rule.Edge,
			Reason:    reason,
			Backend:   "rules",
			Execution: in.Result.ExecutionID,
		}, nil
	}
	edge, kind, err := MatchEdges(in.Node.Next, scope)
	if err != nil {
		return run.Decision{
			Decision:  run.DecisionAbstain,
			Backend:   "rules",
			Reason:    err.Error(),
			Execution: in.Result.ExecutionID,
		}, nil
	}
	switch kind {
	case matchOne:
		reason := "default edge"
		if edge.When != "" {
			reason = fmt.Sprintf("output matched %s", edge.When)
		}
		return run.Decision{
			Decision:  run.DecisionContinue,
			Edge:      edge.To,
			Reason:    reason,
			Backend:   "rules",
			Execution: in.Result.ExecutionID,
		}, nil
	case matchAmbiguous:
		return run.Decision{
			Decision:  run.DecisionAbstain,
			Backend:   "rules",
			Reason:    "more than one edge matched",
			Execution: in.Result.ExecutionID,
		}, nil
	default:
		return run.Decision{
			Decision:  run.DecisionAbstain,
			Backend:   "rules",
			Reason:    "no rule covered this result",
			Execution: in.Result.ExecutionID,
		}, nil
	}
}

func stopDecision(in run.DecideInput, reason string) run.Decision {
	return run.Decision{
		Decision:  run.DecisionStop,
		Reason:    reason,
		Backend:   "rules",
		Execution: in.Result.ExecutionID,
	}
}

func ruleMatches(rule graph.Rule, in run.DecideInput, scope map[string]any) (bool, error) {
	if rule.Execution != "" && rule.Execution != in.Result.ExecutionID {
		return false, nil
	}
	if rule.SameExecutionFailuresGTE > 0 {
		n := countFailures(in.History, in.Result, func(res run.Result) bool {
			return res.ExecutionID == in.Result.ExecutionID
		})
		if n < rule.SameExecutionFailuresGTE {
			return false, nil
		}
	}
	if rule.AreaFailuresGTE > 0 {
		area := rule.Area
		if area == "" {
			area = in.Node.Area
		}
		n := countFailures(in.History, in.Result, func(res run.Result) bool {
			return area != "" && res.Area == area
		})
		if n < rule.AreaFailuresGTE {
			return false, nil
		}
	}
	if rule.When != "" {
		return graph.Eval(rule.When, scope)
	}
	if rule.Execution != "" || rule.SameExecutionFailuresGTE > 0 || rule.AreaFailuresGTE > 0 {
		return true, nil
	}
	return false, nil
}

func countFailures(history []run.Result, current run.Result, match func(run.Result) bool) int {
	n := 0
	for _, res := range history {
		if res.Failed() && match(res) {
			n++
		}
	}
	if current.Failed() && match(current) {
		n++
	}
	return n
}
