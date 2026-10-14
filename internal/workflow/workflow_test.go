package workflow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/ALPHAvibe/shakedown/internal/graph"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

func TestRequiredFailureStopsBeforeNext(t *testing.T) {
	var decides atomic.Int32
	env := newEnv(t, &decides, func(id string) string {
		if id == "b" {
			return "fail"
		}
		return "pass"
	})
	g := graph.Graph{
		Name: "demo",
		Dir:  t.TempDir(),
		Executions: []graph.Execution{
			{ID: "a", Run: []string{"true"}, Timeout: time.Minute, Next: []graph.Edge{{To: "b", When: `status == "pass"`}}},
			{ID: "b", Run: []string{"true"}, Timeout: time.Minute, Required: true},
		},
	}
	env.ExecuteWorkflow(ShakedownRun, run.RunInput{Graph: g, MaxSteps: 10, SameExecutionFailures: 2, AreaFailures: 3})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var report run.Report
	if err := env.GetWorkflowResult(&report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "stopped" || report.Reason != "required execution failed" {
		t.Fatalf("status %s reason %s", report.Status, report.Reason)
	}
	if len(report.Results) != 2 {
		t.Fatalf("results %d", len(report.Results))
	}
	if decides.Load() != 1 {
		t.Fatalf("decide calls %d", decides.Load())
	}
	foundRequired := false
	for _, d := range report.Decisions {
		if d.Backend == "required" {
			foundRequired = true
		}
	}
	if !foundRequired {
		t.Fatalf("decisions %+v", report.Decisions)
	}
}

func TestMaxStepsStopsACycle(t *testing.T) {
	var decides atomic.Int32
	var executes atomic.Int32
	env := newEnv(t, &decides, func(id string) string {
		executes.Add(1)
		return "pass"
	})
	g := graph.Graph{
		Name: "loop",
		Dir:  t.TempDir(),
		Executions: []graph.Execution{
			{ID: "a", Run: []string{"true"}, Timeout: time.Minute, Next: []graph.Edge{{To: "a"}}},
		},
	}
	env.ExecuteWorkflow(ShakedownRun, run.RunInput{Graph: g, MaxSteps: 3})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var report run.Report
	if err := env.GetWorkflowResult(&report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "stopped" {
		t.Fatalf("status %s reason %s", report.Status, report.Reason)
	}
	if executes.Load() != 3 {
		t.Fatalf("executes %d", executes.Load())
	}
}

func TestPickSignalPrefersStop(t *testing.T) {
	sig, ok := pickSignal([]run.ControlSignal{{Action: "skip", Edge: "b"}, {Action: "stop"}})
	if !ok || sig.Action != "stop" {
		t.Fatalf("%+v %v", sig, ok)
	}
}

func newEnv(t *testing.T, decides *atomic.Int32, status func(string) string) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(ShakedownRun)
	env.RegisterActivityWithOptions(func(ctx context.Context, in run.SetupInput) (run.Workspace, error) {
		return run.Workspace{Kind: "local", Dir: in.GraphDir, HostDir: in.GraphDir}, nil
	}, activity.RegisterOptions{Name: run.SetupActivity})
	env.RegisterActivityWithOptions(func(ctx context.Context, in run.ExecuteInput) (run.Result, error) {
		return run.Result{
			ExecutionID: in.Node.ID,
			Status:      status(in.Node.ID),
			Findings:    []string{},
			Artifacts:   []string{},
		}, nil
	}, activity.RegisterOptions{Name: run.ExecuteActivity})
	env.RegisterActivityWithOptions(func(ctx context.Context, in run.DecideInput) (run.Decision, error) {
		decides.Add(1)
		edge := ""
		if len(in.Node.Next) > 0 {
			edge = in.Node.Next[0].To
		}
		return run.Decision{Decision: run.DecisionContinue, Edge: edge, Reason: "test", Backend: "rules", Execution: in.Node.ID}, nil
	}, activity.RegisterOptions{Name: run.DecideActivity})
	env.RegisterActivityWithOptions(func(ctx context.Context, in run.ReportInput) (run.Report, error) {
		return run.Report{Status: in.Status, Reason: in.Reason, Results: in.Results, Decisions: in.Decisions, Teardown: in.Teardown}, nil
	}, activity.RegisterOptions{Name: run.ReportActivity})
	env.RegisterActivityWithOptions(func(ctx context.Context, in run.TeardownInput) (run.TeardownResult, error) {
		return run.TeardownResult{Status: "pass", Summary: "cleanup finished"}, nil
	}, activity.RegisterOptions{Name: run.TeardownActivity})
	return env
}
