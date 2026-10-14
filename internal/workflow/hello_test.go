package workflow

import (
	"testing"

	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/ALPHAvibe/shakedown/internal/activity"
	"github.com/ALPHAvibe/shakedown/internal/config"
	"github.com/ALPHAvibe/shakedown/internal/graph"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

func TestHelloGraphEndToEnd(t *testing.T) {
	cfg := config.Default()
	cfg.Supervisor.Chain = []string{"rules"}
	cfg.Report.Dir = t.TempDir()
	cfg.ArtifactsDir = t.TempDir()
	acts, err := activity.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g, err := graph.Load("../../examples/hello/graph.yaml")
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := graph.ResolveInputs(g.Inputs, map[string]string{"name": "ada"})
	if err != nil {
		t.Fatal(err)
	}

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(ShakedownRun)
	env.RegisterActivityWithOptions(acts.Setup, sdkactivity.RegisterOptions{Name: run.SetupActivity})
	env.RegisterActivityWithOptions(acts.Execute, sdkactivity.RegisterOptions{Name: run.ExecuteActivity})
	env.RegisterActivityWithOptions(acts.Decide, sdkactivity.RegisterOptions{Name: run.DecideActivity})
	env.RegisterActivityWithOptions(acts.Report, sdkactivity.RegisterOptions{Name: run.ReportActivity})
	env.RegisterActivityWithOptions(acts.Teardown, sdkactivity.RegisterOptions{Name: run.TeardownActivity})

	env.ExecuteWorkflow(ShakedownRun, run.RunInput{
		Graph:                 g,
		Inputs:                inputs,
		MaxSteps:              10,
		SameExecutionFailures: 2,
		AreaFailures:          3,
		SupervisorChain:       []string{"rules"},
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var report run.Report
	if err := env.GetWorkflowResult(&report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" {
		t.Fatalf("status %s reason %s", report.Status, report.Reason)
	}
	if len(report.Results) != 1 || report.Results[0].Status != "pass" {
		t.Fatalf("results %+v", report.Results)
	}
	if report.Results[0].Summary != "hello ada" {
		t.Fatalf("summary %q", report.Results[0].Summary)
	}
	if report.MarkdownPath == "" {
		t.Fatal("missing report path")
	}
}
