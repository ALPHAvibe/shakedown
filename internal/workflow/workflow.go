package workflow

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/ALPHAvibe/shakedown/internal/graph"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

// ShakedownRun walks a graph. It never calls a model itself; every script,
// model call, decision, and report is an activity.
func ShakedownRun(ctx workflow.Context, in run.RunInput) (run.Report, error) {
	info := workflow.GetInfo(ctx)
	started := workflow.Now(ctx)
	state := run.Status{
		RunID:     info.WorkflowExecution.ID,
		Graph:     in.Graph.Name,
		Phase:     "running",
		Results:   []run.Result{},
		Decisions: []run.Decision{},
	}
	if err := workflow.SetQueryHandler(ctx, run.QueryName, func() (run.Status, error) {
		return state, nil
	}); err != nil {
		return run.Report{}, err
	}
	logger := workflow.GetLogger(ctx)
	maxSteps := in.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 100
	}

	var tornDown bool
	var teardown run.TeardownResult
	var ws run.Workspace
	cleanup := func() {
		if tornDown {
			return
		}
		dctx, cancel := workflow.NewDisconnectedContext(ctx)
		defer cancel()
		timeout := 10 * time.Minute
		if in.Graph.Teardown != nil && in.Graph.Teardown.Timeout > 0 {
			timeout = in.Graph.Teardown.Timeout
		}
		err := workflow.ExecuteActivity(activityCtx(dctx, timeout), run.TeardownActivity, run.TeardownInput{
			RunID:     state.RunID,
			GraphDir:  in.Graph.Dir,
			Workspace: ws,
			Hook:      in.Graph.Teardown,
			Inputs:    in.Inputs,
		}).Get(dctx, &teardown)
		if err != nil {
			teardown = run.TeardownResult{Status: "error", Summary: err.Error()}
		}
		tornDown = true
	}
	defer cleanup()

	if err := workflow.ExecuteActivity(activityCtx(ctx, 5*time.Minute), run.SetupActivity, run.SetupInput{
		RunID:    state.RunID,
		GraphDir: in.Graph.Dir,
	}).Get(ctx, &ws); err != nil {
		state.Phase = "failed"
		state.StopReason = err.Error()
		cleanup()
		return finishReport(ctx, in, state, started, teardown)
	}

	node, ok := in.Graph.Node(in.Graph.StartID())
	if !ok {
		state.Phase = "failed"
		state.StopReason = "start execution is missing"
		cleanup()
		return finishReport(ctx, in, state, started, teardown)
	}

	steps := 0
	for {
		if sig, ok := pickSignal(drainSignals(ctx)); ok && sig.Action == "stop" {
			state.Phase = "stopped"
			state.StopReason = "stopped by signal"
			break
		} else if ok && sig.Action == "rerun" {
			if next, found := in.Graph.Node(sig.Execution); found {
				node = next
				logger.Info("rerun requested", "execution", node.ID)
			}
		} else if ok && sig.Action == "skip" {
			now := workflow.Now(ctx)
			state.Results = append(state.Results, run.Result{
				ExecutionID: node.ID,
				Area:        node.Area,
				Status:      "skipped",
				Summary:     "skipped by signal",
				Findings:    []string{},
				Artifacts:   []string{},
				StartedAt:   now,
				FinishedAt:  now,
			})
			edge := sig.Edge
			if edge == "" && len(node.Next) > 0 {
				edge = node.Next[0].To
			}
			if edge == "" {
				state.Phase = "stopped"
				state.StopReason = "skipped with no edge to follow"
				break
			}
			next, found := in.Graph.Node(edge)
			if !found {
				state.Phase = "failed"
				state.StopReason = "skip edge not found: " + edge
				break
			}
			node = next
			continue
		}

		steps++
		if steps > maxSteps {
			state.Phase = "stopped"
			state.StopReason = fmt.Sprintf("stopped after %d steps", maxSteps)
			break
		}

		state.CurrentNode = node.ID
		state.Phase = "running"
		logger.Info("executing", "execution", node.ID)
		var res run.Result
		err := workflow.ExecuteActivity(activityCtx(ctx, node.Timeout), run.ExecuteActivity, run.ExecuteInput{
			RunID:     state.RunID,
			GraphDir:  in.Graph.Dir,
			Node:      node,
			Inputs:    in.Inputs,
			Prior:     append([]run.Result{}, state.Results...),
			Workspace: ws,
			Model:     in.WorkerModel,
		}).Get(ctx, &res)
		if err != nil {
			state.Phase = "failed"
			state.StopReason = err.Error()
			state.Results = append(state.Results, run.Result{
				ExecutionID: node.ID,
				Area:        node.Area,
				Status:      "error",
				Summary:     err.Error(),
				Findings:    []string{},
				Artifacts:   []string{},
			})
			break
		}
		if res.Area == "" {
			res.Area = node.Area
		}
		state.Results = append(state.Results, res)

		if node.Required && res.Status != "pass" {
			d := run.Decision{
				Decision:  run.DecisionStop,
				Reason:    "required execution failed",
				Backend:   "required",
				Execution: node.ID,
			}
			state.Decisions = append(state.Decisions, d)
			state.Phase = "stopped"
			state.StopReason = d.Reason
			break
		}
		if node.IsTerminal() {
			state.Phase = "completed"
			if res.Failed() && state.StopReason == "" {
				state.StopReason = res.Summary
			}
			break
		}

		history := append([]run.Result{}, state.Results[:len(state.Results)-1]...)
		var decision run.Decision
		err = workflow.ExecuteActivity(activityCtx(ctx, 10*time.Minute), run.DecideActivity, run.DecideInput{
			Node:                  node,
			Rules:                 in.Graph.Rules,
			Result:                res,
			History:               history,
			Inputs:                in.Inputs,
			SameExecutionFailures: in.SameExecutionFailures,
			AreaFailures:          in.AreaFailures,
			Chain:                 in.SupervisorChain,
			Model:                 in.SupervisorModel,
			MinConfidence:         in.MinConfidence,
		}).Get(ctx, &decision)
		if err != nil {
			decision = run.Decision{
				Decision:  run.DecisionStop,
				Reason:    "supervisor failed: " + err.Error(),
				Backend:   "error",
				Execution: node.ID,
			}
		}
		if decision.Execution == "" {
			decision.Execution = node.ID
		}
		state.Decisions = append(state.Decisions, decision)
		if decision.Decision != run.DecisionContinue {
			state.Phase = "stopped"
			state.StopReason = decision.Reason
			break
		}
		next, found := in.Graph.Node(decision.Edge)
		if !found {
			state.Phase = "failed"
			state.StopReason = "supervisor chose unknown edge " + decision.Edge
			break
		}
		node = next
	}

	cleanup()
	state.CurrentNode = ""
	return finishReport(ctx, in, state, started, teardown)
}

func finishReport(ctx workflow.Context, in run.RunInput, state run.Status, started time.Time, teardown run.TeardownResult) (run.Report, error) {
	var report run.Report
	err := workflow.ExecuteActivity(activityCtx(ctx, 10*time.Minute), run.ReportActivity, run.ReportInput{
		RunID:                 state.RunID,
		GraphName:             in.Graph.Name,
		Graph:                 in.Graph,
		Status:                state.Phase,
		Reason:                state.StopReason,
		StartedAt:             started,
		Results:               state.Results,
		Decisions:             state.Decisions,
		Teardown:              teardown,
		Model:                 in.WorkerModel,
		Sinks:                 in.ReportSinks,
		Inputs:                in.Inputs,
		SupervisorChain:       in.SupervisorChain,
		SameExecutionFailures: in.SameExecutionFailures,
		AreaFailures:          in.AreaFailures,
		MinConfidence:         in.MinConfidence,
	}).Get(ctx, &report)
	if err != nil {
		return run.Report{
			RunID:     state.RunID,
			Graph:     in.Graph.Name,
			Status:    state.Phase,
			Reason:    state.StopReason,
			Results:   state.Results,
			Decisions: state.Decisions,
			Teardown:  teardown,
		}, err
	}
	return report, nil
}

func drainSignals(ctx workflow.Context) []run.ControlSignal {
	ch := workflow.GetSignalChannel(ctx, run.ControlSignalName)
	var sigs []run.ControlSignal
	for {
		var sig run.ControlSignal
		if !ch.ReceiveAsync(&sig) {
			break
		}
		sigs = append(sigs, sig)
	}
	return sigs
}

func pickSignal(sigs []run.ControlSignal) (run.ControlSignal, bool) {
	var chosen run.ControlSignal
	found := false
	for _, sig := range sigs {
		if sig.Action == "stop" {
			return sig, true
		}
		chosen = sig
		found = true
	}
	return chosen, found
}

func activityCtx(ctx workflow.Context, timeout time.Duration) workflow.Context {
	if timeout <= 0 {
		timeout = graph.DefaultTimeout
	}
	heartbeat := 30 * time.Second
	if heartbeat >= timeout {
		heartbeat = timeout / 2
	}
	if heartbeat < time.Second {
		heartbeat = time.Second
	}
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: timeout,
		HeartbeatTimeout:    heartbeat,
		WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})
}
