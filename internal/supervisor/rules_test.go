package supervisor

import (
	"context"
	"testing"

	"github.com/ALPHAvibe/shakedown/internal/graph"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

func passResult(id, area string) run.Result {
	return run.Result{ExecutionID: id, Area: area, Status: "pass", Findings: []string{}, Artifacts: []string{}}
}

func failResult(id, area string) run.Result {
	return run.Result{ExecutionID: id, Area: area, Status: "fail", Summary: "broke", Findings: []string{}, Artifacts: []string{}}
}

func TestRulesFollowMatchingEdge(t *testing.T) {
	in := run.DecideInput{
		Node: graph.Execution{ID: "run-migrations", Next: []graph.Edge{
			{To: "api-smoke", When: `status == "pass"`},
			{To: "migration-retry", When: `status == "fail"`},
		}},
		Result:                passResult("run-migrations", "migrations"),
		SameExecutionFailures: 2,
		AreaFailures:          3,
	}
	d, err := Rules{}.Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != run.DecisionContinue || d.Edge != "api-smoke" || d.Backend != "rules" {
		t.Fatalf("%+v", d)
	}
}

func TestRulesStopAfterRepeatedFailure(t *testing.T) {
	in := run.DecideInput{
		Node: graph.Execution{ID: "run-migrations", Area: "migrations", Next: []graph.Edge{
			{To: "migration-retry", When: `status == "fail"`},
		}},
		Result:                failResult("run-migrations", "migrations"),
		History:               []run.Result{failResult("run-migrations", "migrations")},
		SameExecutionFailures: 2,
		AreaFailures:          3,
	}
	d, err := Rules{}.Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != run.DecisionStop {
		t.Fatalf("%+v", d)
	}
}

func TestRulesStopAfterAreaFailures(t *testing.T) {
	in := run.DecideInput{
		Node:   graph.Execution{ID: "migration-retry", Area: "migrations", Next: []graph.Edge{{To: "run-migrations"}}},
		Result: failResult("migration-retry", "migrations"),
		History: []run.Result{
			failResult("run-migrations", "migrations"),
			failResult("run-migrations", "migrations"),
		},
		SameExecutionFailures: 2,
		AreaFailures:          3,
	}
	d, err := Rules{}.Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != run.DecisionStop || d.Backend != "rules" {
		t.Fatalf("%+v", d)
	}
}

func TestRulesAbstainWhenAmbiguous(t *testing.T) {
	in := run.DecideInput{
		Node: graph.Execution{ID: "explore", Next: []graph.Edge{
			{To: "left", When: `status == "pass"`},
			{To: "right", When: `status == "pass"`},
		}},
		Result: passResult("explore", ""),
	}
	d, err := Rules{}.Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Confident() {
		t.Fatalf("expected abstain, got %+v", d)
	}
}

func TestChainUsesFirstConfidentLayer(t *testing.T) {
	chain := Chain{Layers: []Supervisor{
		fixed{run.Decision{Decision: run.DecisionAbstain, Backend: "rules", Reason: "no rule"}},
		fixed{run.Decision{Decision: run.DecisionContinue, Edge: "retry", Reason: "model", Backend: "model"}},
		fixed{run.Decision{Decision: run.DecisionStop, Reason: "should not run", Backend: "jev"}},
	}}
	d, err := chain.Decide(context.Background(), run.DecideInput{Result: passResult("a", "")})
	if err != nil {
		t.Fatal(err)
	}
	if d.Backend != "model" || d.Edge != "retry" {
		t.Fatalf("%+v", d)
	}
}

func TestCustomRuleStopsProvisioning(t *testing.T) {
	in := run.DecideInput{
		Node: graph.Execution{ID: "provision-sandbox", Next: []graph.Edge{{To: "run-migrations"}}},
		Rules: []graph.Rule{{
			Name: "sandbox provisioning failed", Execution: "provision-sandbox",
			When: `status == "fail" || status == "error"`, Decision: "stop",
			Reason: "nothing downstream can run",
		}},
		Result:                failResult("provision-sandbox", ""),
		SameExecutionFailures: 5,
		AreaFailures:          5,
	}
	d, err := Rules{}.Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != run.DecisionStop || d.Reason != "nothing downstream can run" {
		t.Fatalf("%+v", d)
	}
}

type fixed struct{ d run.Decision }

func (f fixed) Decide(context.Context, run.DecideInput) (run.Decision, error) { return f.d, nil }
