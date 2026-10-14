package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"gopkg.in/yaml.v3"

	"github.com/ALPHAvibe/shakedown/internal/activity"
	"github.com/ALPHAvibe/shakedown/internal/config"
	"github.com/ALPHAvibe/shakedown/internal/graph"
	"github.com/ALPHAvibe/shakedown/internal/run"
	wf "github.com/ALPHAvibe/shakedown/internal/workflow"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "worker":
		err = cmdWorker(os.Args[2:])
	case "check":
		err = cmdCheck(os.Args[2:])
	case "config":
		err = cmdConfig(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "stop":
		err = cmdStop(os.Args[2:])
	case "signal":
		err = cmdSignal(os.Args[2:])
	case "schedule":
		err = cmdSchedule(os.Args[2:])
	case "replay":
		err = cmdReplay(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "shakedown:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Shakedown runs a graph of scripts and model prompts on Temporal.

Usage:
  shakedown run <graph.yaml> [--input key=value] [--model name] [--base-url url]
  shakedown worker [--model name] [--base-url url]
  shakedown check <graph.yaml>
  shakedown config
  shakedown status <workflow-id>
  shakedown stop <workflow-id>
  shakedown signal <workflow-id> stop|skip|rerun [--edge id] [--execution id]
  shakedown schedule sync [schedules.yaml]
  shakedown replay <report.json>

Model flags apply to whatever command you pass them to. run stores them on that
run. worker changes the model the local worker uses. Point --base-url at any
OpenAI-compatible server (Ollama, llama.cpp, vLLM, OpenAI, OpenRouter).

Environment:
  SHAKEDOWN_CONFIG, SHAKEDOWN_MODEL, SHAKEDOWN_BASE_URL, SHAKEDOWN_API_KEY
  SHAKEDOWN_API_KEY_ENV, SHAKEDOWN_WORKER_MODEL, SHAKEDOWN_SUPERVISOR_MODEL
  SHAKEDOWN_JSON_MODE, SHAKEDOWN_TEMPORAL_ADDRESS, SHAKEDOWN_TASK_QUEUE
`)
}

type modelFlags struct {
	config          string
	model           string
	baseURL         string
	apiKeyEnv       string
	jsonMode        string
	toolProtocol    string
	decisionStyle   string
	workerModel     string
	supervisorModel string
	profile         string
}

func (f *modelFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.config, "config", "", "config file (default shakedown.yaml or $SHAKEDOWN_CONFIG)")
	fs.StringVar(&f.model, "model", "", "model name for the worker and the supervisor")
	fs.StringVar(&f.baseURL, "base-url", "", "OpenAI-compatible base URL, including /v1")
	fs.StringVar(&f.apiKeyEnv, "api-key-env", "", "env var the worker reads for the API key")
	fs.StringVar(&f.jsonMode, "json-mode", "", "prompt, json_object, json_schema, or grammar")
	fs.StringVar(&f.toolProtocol, "tool-protocol", "", "auto, openai, or text")
	fs.StringVar(&f.decisionStyle, "decision-style", "", "json or yesno")
	fs.StringVar(&f.workerModel, "worker-model", "", "model name for prompt executions")
	fs.StringVar(&f.supervisorModel, "supervisor-model", "", "model name for ambiguous supervisor decisions")
	fs.StringVar(&f.profile, "profile", "", "named model profile from the config file")
}

func (f modelFlags) specs() (worker, supervisor run.ModelSpec) {
	worker = run.ModelSpec{
		Profile: f.profile, BaseURL: f.baseURL, Model: f.model, APIKeyEnv: f.apiKeyEnv,
		JSONMode: f.jsonMode, ToolProtocol: f.toolProtocol,
	}
	supervisor = run.ModelSpec{
		Profile: f.profile, BaseURL: f.baseURL, Model: f.model, APIKeyEnv: f.apiKeyEnv,
		JSONMode: f.jsonMode, DecisionStyle: f.decisionStyle,
	}
	if f.workerModel != "" {
		worker.Model = f.workerModel
	}
	if f.supervisorModel != "" {
		supervisor.Model = f.supervisorModel
	}
	return worker, supervisor
}

func (f modelFlags) apply(cfg *config.Config) {
	workerSpec, supervisorSpec := f.specs()
	if workerSpec.Empty() && supervisorSpec.Empty() {
		return
	}
	worker := overlay(cfg.WorkerProfile(), workerSpec)
	supervisor := overlay(cfg.SupervisorProfile(), supervisorSpec)
	if cfg.Roles.Worker == cfg.Roles.Supervisor && (worker.Model != supervisor.Model || worker.BaseURL != supervisor.BaseURL) {
		cfg.Models[cfg.Roles.Worker] = worker
		cfg.Roles.Supervisor = "supervisor-override"
		cfg.Models[cfg.Roles.Supervisor] = supervisor
		return
	}
	cfg.Models[cfg.Roles.Worker] = worker
	cfg.Models[cfg.Roles.Supervisor] = supervisor
}

func overlay(p config.ModelProfile, spec run.ModelSpec) config.ModelProfile {
	if spec.BaseURL != "" {
		p.BaseURL = spec.BaseURL
	}
	if spec.Model != "" {
		p.Model = spec.Model
	}
	if spec.APIKeyEnv != "" {
		p.APIKeyEnv = spec.APIKeyEnv
	}
	if spec.JSONMode != "" {
		p.JSONMode = spec.JSONMode
	}
	if spec.ToolProtocol != "" {
		p.ToolProtocol = spec.ToolProtocol
	}
	if spec.DecisionStyle != "" {
		p.DecisionStyle = spec.DecisionStyle
	}
	return p
}

func loadConfig(path string, flags modelFlags) (config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, err
	}
	flags.apply(&cfg)
	return cfg, nil
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func parseInputs(pairs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("input %q must be key=value", pair)
		}
		out[key] = value
	}
	return out, nil
}

func prepareRun(cfg config.Config, graphPath string, inputs map[string]string, worker, supervisor run.ModelSpec) (run.RunInput, error) {
	g, err := graph.Load(graphPath)
	if err != nil {
		return run.RunInput{}, err
	}
	vals, err := graph.ResolveInputs(g.Inputs, inputs)
	if err != nil {
		return run.RunInput{}, err
	}
	sinks := make([]run.SinkSpec, 0, len(cfg.Report.Sinks))
	for _, sink := range cfg.Report.Sinks {
		sinks = append(sinks, run.SinkSpec{
			Kind: sink.Kind, WebhookEnv: sink.WebhookEnv, TokenEnv: sink.TokenEnv,
			Repo: sink.Repo, PRInput: sink.PRInput,
		})
	}
	return run.RunInput{
		Graph:                 g,
		Inputs:                vals,
		MaxSteps:              cfg.Supervisor.MaxSteps,
		SameExecutionFailures: cfg.Supervisor.SameExecutionFailures,
		AreaFailures:          cfg.Supervisor.AreaFailures,
		SupervisorChain:       append([]string{}, cfg.Supervisor.Chain...),
		MinConfidence:         cfg.Supervisor.MinConfidence,
		WorkerModel:           worker,
		SupervisorModel:       supervisor,
		ReportSinks:           sinks,
	}, nil
}

func dial(cfg config.Config) (client.Client, error) {
	return client.Dial(client.Options{HostPort: cfg.Temporal.Address, Namespace: cfg.Temporal.Namespace})
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var models modelFlags
	models.bind(fs)
	var inputs stringList
	fs.Var(&inputs, "input", "graph input as key=value (repeatable)")
	id := fs.String("id", "", "workflow id")
	detach := fs.Bool("detach", false, "return after starting the run")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shakedown run <graph.yaml>")
	}
	cfg, err := loadConfig(models.config, models)
	if err != nil {
		return err
	}
	parsed, err := parseInputs(inputs)
	if err != nil {
		return err
	}
	workerSpec, supervisorSpec := models.specs()
	input, err := prepareRun(cfg, fs.Arg(0), parsed, workerSpec, supervisorSpec)
	if err != nil {
		return err
	}
	workflowID := *id
	if workflowID == "" {
		workflowID = fmt.Sprintf("shakedown-%s-%d", sanitizeID(input.Graph.Name), time.Now().Unix())
	}
	c, err := dial(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	we, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                cfg.Temporal.TaskQueue,
		WorkflowExecutionTimeout: cfg.Temporal.RunTimeout,
	}, "ShakedownRun", input)
	if err != nil {
		return err
	}
	fmt.Println(cfg.WorkflowUILink(we.GetID()))
	fmt.Printf("workflow %s run %s\n", we.GetID(), we.GetRunID())
	if *detach {
		return nil
	}
	var report run.Report
	if err := we.Get(context.Background(), &report); err != nil {
		return err
	}
	fmt.Printf("status %s\n", report.Status)
	if report.MarkdownPath != "" {
		fmt.Printf("report %s\n", report.MarkdownPath)
	}
	if report.Reason != "" {
		fmt.Printf("reason %s\n", report.Reason)
	}
	return nil
}

func cmdWorker(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	var models modelFlags
	models.bind(fs)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(models.config, models)
	if err != nil {
		return err
	}
	c, err := dial(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	acts, err := activity.New(cfg)
	if err != nil {
		return err
	}
	w := worker.New(c, cfg.Temporal.TaskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(wf.ShakedownRun, workflow.RegisterOptions{Name: "ShakedownRun"})
	w.RegisterActivityWithOptions(acts.Setup, activityRegister(run.SetupActivity))
	w.RegisterActivityWithOptions(acts.Execute, activityRegister(run.ExecuteActivity))
	w.RegisterActivityWithOptions(acts.Decide, activityRegister(run.DecideActivity))
	w.RegisterActivityWithOptions(acts.Report, activityRegister(run.ReportActivity))
	w.RegisterActivityWithOptions(acts.Teardown, activityRegister(run.TeardownActivity))
	workerProfile := cfg.WorkerProfile()
	supervisorProfile := cfg.SupervisorProfile()
	fmt.Printf("shakedown worker on %s queue %s\n", cfg.Temporal.Address, cfg.Temporal.TaskQueue)
	fmt.Printf("worker model %s @ %s\n", emptyDash(workerProfile.Model), emptyDash(workerProfile.BaseURL))
	fmt.Printf("supervisor model %s @ %s\n", emptyDash(supervisorProfile.Model), emptyDash(supervisorProfile.BaseURL))
	fmt.Printf("supervisor chain %s\n", strings.Join(cfg.Supervisor.Chain, ","))
	return w.Run(worker.InterruptCh())
}

func activityRegister(name string) sdkactivity.RegisterOptions {
	return sdkactivity.RegisterOptions{Name: name}
}

func cmdCheck(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: shakedown check <graph.yaml>")
	}
	g, err := graph.Load(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("%s: %d executions, start %s\n", g.Name, len(g.Executions), g.StartID())
	return nil
}

func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	var models modelFlags
	models.bind(fs)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(models.config, models)
	if err != nil {
		return err
	}
	raw, err := yaml.Marshal(cfg.Redacted())
	if err != nil {
		return err
	}
	fmt.Print(string(raw))
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shakedown status <workflow-id>")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	c, err := dial(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	resp, err := c.QueryWorkflow(context.Background(), fs.Arg(0), "", run.QueryName)
	if err != nil {
		return err
	}
	var status run.Status
	if err := resp.Get(&status); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}

func cmdStop(args []string) error {
	return signal(args, run.ControlSignal{Action: "stop"}, true)
}

func cmdSignal(args []string) error {
	fs := flag.NewFlagSet("signal", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file")
	edge := fs.String("edge", "", "edge to follow when skipping")
	execution := fs.String("execution", "", "execution to rerun")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: shakedown signal <workflow-id> stop|skip|rerun")
	}
	action := fs.Arg(1)
	switch action {
	case "stop", "skip", "rerun":
	default:
		return fmt.Errorf("signal action must be stop, skip, or rerun")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	return sendSignal(cfg, fs.Arg(0), run.ControlSignal{Action: action, Edge: *edge, Execution: *execution})
}

func signal(args []string, sig run.ControlSignal, takeID bool) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !takeID || fs.NArg() != 1 {
		return fmt.Errorf("usage: shakedown stop <workflow-id>")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	return sendSignal(cfg, fs.Arg(0), sig)
}

func sendSignal(cfg config.Config, id string, sig run.ControlSignal) error {
	c, err := dial(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.SignalWorkflow(context.Background(), id, "", run.ControlSignalName, sig); err != nil {
		return err
	}
	fmt.Printf("signaled %s %s\n", id, sig.Action)
	return nil
}

func cmdSchedule(args []string) error {
	if len(args) == 0 || args[0] != "sync" {
		return fmt.Errorf("usage: shakedown schedule sync [schedules.yaml]")
	}
	fs := flag.NewFlagSet("schedule sync", flag.ContinueOnError)
	var models modelFlags
	models.bind(fs)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	path := "schedules.yaml"
	if fs.NArg() == 1 {
		path = fs.Arg(0)
	} else if fs.NArg() > 1 {
		return fmt.Errorf("usage: shakedown schedule sync [schedules.yaml]")
	}
	cfg, err := loadConfig(models.config, models)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var file struct {
		Schedules []struct {
			ID     string         `yaml:"id"`
			Graph  string         `yaml:"graph"`
			Cron   string         `yaml:"cron"`
			Inputs map[string]any `yaml:"inputs"`
			Note   string         `yaml:"note"`
		} `yaml:"schedules"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return err
	}
	c, err := dial(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	workerSpec, supervisorSpec := models.specs()
	for _, entry := range file.Schedules {
		if entry.ID == "" || entry.Graph == "" || entry.Cron == "" {
			return fmt.Errorf("each schedule needs id, graph, and cron")
		}
		input, err := prepareRun(cfg, entry.Graph, stringifyInputs(entry.Inputs), workerSpec, supervisorSpec)
		if err != nil {
			return fmt.Errorf("schedule %s: %w", entry.ID, err)
		}
		action := &client.ScheduleWorkflowAction{
			ID:                       entry.ID,
			Workflow:                 "ShakedownRun",
			Args:                     []any{input},
			TaskQueue:                cfg.Temporal.TaskQueue,
			WorkflowExecutionTimeout: cfg.Temporal.RunTimeout,
		}
		handle := c.ScheduleClient().GetHandle(context.Background(), entry.ID)
		_, descErr := handle.Describe(context.Background())
		if isNotFound(descErr) {
			_, err = c.ScheduleClient().Create(context.Background(), client.ScheduleOptions{
				ID:      entry.ID,
				Spec:    client.ScheduleSpec{CronExpressions: []string{entry.Cron}},
				Action:  action,
				Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
				Note:    entry.Note,
			})
		} else if descErr != nil {
			return descErr
		} else {
			err = handle.Update(context.Background(), client.ScheduleUpdateOptions{
				DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
					sch := in.Description.Schedule
					sch.Spec = &client.ScheduleSpec{CronExpressions: []string{entry.Cron}}
					sch.Action = action
					if entry.Note != "" && sch.State != nil {
						sch.State.Note = entry.Note
					}
					return &client.ScheduleUpdate{Schedule: &sch}, nil
				},
			})
		}
		if err != nil {
			return fmt.Errorf("schedule %s: %w", entry.ID, err)
		}
		fmt.Printf("synced %s (%s)\n", entry.ID, entry.Cron)
	}
	return nil
}

func cmdReplay(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	var models modelFlags
	models.bind(fs)
	chain := fs.String("chain", "", "comma-separated supervisor layers to replay (rules,model,jev)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shakedown replay <report.json>")
	}
	cfg, err := loadConfig(models.config, models)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	var report run.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return err
	}
	if *chain != "" {
		report.SupervisorChain = splitChain(*chain)
	}
	acts, err := activity.New(cfg)
	if err != nil {
		return err
	}
	_, supervisorSpec := models.specs()
	fmt.Printf("replaying %s with chain %s\n", report.RunID, strings.Join(report.SupervisorChain, ","))
	for i, res := range report.Results {
		node, ok := report.GraphSnapshot.Node(res.ExecutionID)
		if !ok || node.IsTerminal() || res.Status == "skipped" {
			continue
		}
		decision, err := acts.Decide(context.Background(), run.DecideInput{
			Node:                  node,
			Rules:                 report.GraphSnapshot.Rules,
			Result:                res,
			History:               report.Results[:i],
			Inputs:                report.Inputs,
			SameExecutionFailures: report.SameExecutionFailures,
			AreaFailures:          report.AreaFailures,
			Chain:                 report.SupervisorChain,
			Model:                 supervisorSpec,
			MinConfidence:         report.MinConfidence,
		})
		if err != nil {
			return fmt.Errorf("replay %s: %w", res.ExecutionID, err)
		}
		old := findDecision(report.Decisions, res.ExecutionID)
		fmt.Printf("%s stored=%s/%s replay=%s/%s (%s)\n", res.ExecutionID, old.Decision, old.Edge, decision.Decision, decision.Edge, decision.Reason)
	}
	return nil
}

func splitChain(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func findDecision(decisions []run.Decision, execution string) run.Decision {
	var found run.Decision
	for _, d := range decisions {
		if d.Execution == execution {
			found = d
		}
	}
	return found
}

func stringifyInputs(in map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func isNotFound(err error) bool {
	var nf *serviceerror.NotFound
	return errors.As(err, &nf)
}

func sanitizeID(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "run"
	}
	return b.String()
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
