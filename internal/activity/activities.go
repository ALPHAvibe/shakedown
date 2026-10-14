package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/ALPHAvibe/shakedown/internal/config"
	"github.com/ALPHAvibe/shakedown/internal/graph"
	"github.com/ALPHAvibe/shakedown/internal/model"
	"github.com/ALPHAvibe/shakedown/internal/report"
	"github.com/ALPHAvibe/shakedown/internal/run"
	"github.com/ALPHAvibe/shakedown/internal/runner"
	"github.com/ALPHAvibe/shakedown/internal/supervisor"
)

// Activities are the non-deterministic steps of a run. The workflow only orchestrates them.
type Activities struct {
	Config config.Config
	Runner runner.Runner
}

func New(cfg config.Config) (*Activities, error) {
	r, err := runner.New(cfg)
	if err != nil {
		return nil, err
	}
	return &Activities{Config: cfg, Runner: r}, nil
}

func (a *Activities) Setup(ctx context.Context, in run.SetupInput) (run.Workspace, error) {
	stop := startHeartbeat(ctx)
	defer stop()
	return a.Runner.Setup(ctx, in.RunID, in.GraphDir)
}

func (a *Activities) Teardown(ctx context.Context, in run.TeardownInput) (run.TeardownResult, error) {
	stop := startHeartbeat(ctx)
	defer stop()
	if ctx.Err() != nil {
		return run.TeardownResult{}, ctx.Err()
	}
	var notes []string
	status := "pass"
	if in.Hook != nil {
		for _, command := range in.Hook.Run {
			rendered, err := graph.Render(command, graph.InputScope(in.Inputs), true)
			if err != nil {
				status = "error"
				notes = append(notes, err.Error())
				continue
			}
			res, err := a.Runner.Shell(ctx, in.Workspace, rendered, a.commandEnv(in.Workspace))
			if err != nil {
				return run.TeardownResult{}, err
			}
			if res.ExitCode != 0 {
				status = "error"
				notes = append(notes, fmt.Sprintf("%s exited %d", command, res.ExitCode))
			}
		}
	}
	if err := a.Runner.Teardown(ctx, in.Workspace); err != nil {
		status = "error"
		notes = append(notes, err.Error())
	}
	if status == "pass" && len(notes) == 0 {
		notes = append(notes, "cleanup finished")
	}
	return run.TeardownResult{Status: status, Summary: strings.Join(notes, "; ")}, nil
}

func (a *Activities) Execute(ctx context.Context, in run.ExecuteInput) (run.Result, error) {
	stop := startHeartbeat(ctx)
	defer stop()
	started := time.Now()
	var (
		res run.Result
		err error
	)
	if in.Node.PromptBody != "" {
		res, err = a.runPrompt(ctx, in, in.Node.PromptBody, nil)
	} else {
		res, err = a.runCommands(ctx, in, in.Node.Run)
		if err == nil && res.Failed() && in.Node.OnFailBody != "" {
			classified, cerr := a.runPrompt(ctx, in, in.Node.OnFailBody, &res)
			if cerr != nil {
				return run.Result{}, cerr
			}
			classified.Artifacts = append(res.Artifacts, classified.Artifacts...)
			res = classified
		}
	}
	if err != nil {
		return run.Result{}, err
	}
	res.ExecutionID = in.Node.ID
	res.Area = in.Node.Area
	if res.StartedAt.IsZero() {
		res.StartedAt = started
	}
	res.FinishedAt = time.Now()
	if res.Findings == nil {
		res.Findings = []string{}
	}
	if res.Artifacts == nil {
		res.Artifacts = []string{}
	}
	if res.Data == nil {
		res.Data = map[string]any{}
	}
	return res, nil
}

func (a *Activities) Decide(ctx context.Context, in run.DecideInput) (run.Decision, error) {
	stop := startHeartbeat(ctx)
	defer stop()
	if in.Result.Area == "" {
		in.Result.Area = in.Node.Area
	}
	chain := in.Chain
	if len(chain) == 0 {
		chain = a.Config.Supervisor.Chain
	}
	var layers []supervisor.Supervisor
	for _, name := range chain {
		switch name {
		case "rules":
			layers = append(layers, supervisor.Rules{})
		case "model":
			profile := a.profile(a.Config.Roles.Supervisor, in.Model)
			layers = append(layers, supervisor.Model{
				Client:        model.New(profile),
				MinConfidence: in.MinConfidence,
			})
		case "jev":
			layers = append(layers, supervisor.Jev{Config: a.Config.Supervisor.Jev})
		}
	}
	return supervisor.Chain{Layers: layers}.Decide(ctx, in)
}

func (a *Activities) Report(ctx context.Context, in run.ReportInput) (run.Report, error) {
	stop := startHeartbeat(ctx)
	defer stop()
	summary := report.SummaryFromResults(in)
	if text, err := a.summarize(ctx, in); err == nil && strings.TrimSpace(text) != "" {
		summary = strings.TrimSpace(text)
	}
	return report.Write(a.Config.Report.Dir, in, summary, time.Now())
}

func (a *Activities) runCommands(ctx context.Context, in run.ExecuteInput, commands []string) (run.Result, error) {
	scope := graph.InputScope(in.Inputs)
	var stdout, stderr string
	exit := 0
	var artifacts []string
	for i, command := range commands {
		rendered, err := graph.Render(command, scope, true)
		if err != nil {
			return errorResult(err.Error(), artifacts), nil
		}
		res, err := a.Runner.Shell(ctx, in.Workspace, rendered, a.commandEnv(in.Workspace))
		if err != nil {
			return run.Result{}, err
		}
		stdout, stderr = res.Stdout, res.Stderr
		exit = res.ExitCode
		if path, werr := a.writeLog(in, fmt.Sprintf("command-%d-stdout.txt", i+1), res.Stdout); werr == nil && strings.TrimSpace(res.Stdout) != "" {
			artifacts = append(artifacts, path)
		}
		if path, werr := a.writeLog(in, fmt.Sprintf("command-%d-stderr.txt", i+1), res.Stderr); werr == nil && strings.TrimSpace(res.Stderr) != "" {
			artifacts = append(artifacts, path)
		}
		if res.ExitCode != 0 {
			break
		}
	}
	fallback := "pass"
	if exit != 0 {
		fallback = "fail"
	}
	parsed := run.ParseOutputs(stdout, stderr, exit, fallback)
	parsed.Artifacts = append(artifacts, parsed.Artifacts...)
	return parsed, nil
}

func (a *Activities) summarize(ctx context.Context, in run.ReportInput) (string, error) {
	profile := a.profile(a.Config.Roles.Worker, in.Model)
	if profile.BaseURL == "" || profile.Model == "" {
		return "", fmt.Errorf("no model configured")
	}
	brief := make([]map[string]any, 0, len(in.Results))
	for _, res := range in.Results {
		brief = append(brief, map[string]any{
			"execution": res.ExecutionID,
			"status":    res.Status,
			"summary":   res.Summary,
			"findings":  res.Findings,
		})
	}
	payload, err := json.Marshal(map[string]any{
		"graph":     in.GraphName,
		"status":    in.Status,
		"reason":    in.Reason,
		"results":   brief,
		"decisions": in.Decisions,
	})
	if err != nil {
		return "", err
	}
	temp := 0.2
	resp, err := model.New(profile).Chat(ctx, model.ChatRequest{
		Messages: []model.Message{
			{Role: "system", Content: "Write two to four sentences summarizing this Shakedown run. Use only the JSON facts. Do not quote raw logs."},
			{Role: "user", Content: string(payload)},
		},
		Temperature: &temp,
		JSONMode:    "prompt",
	})
	if err != nil {
		return "", err
	}
	return redact(resp.Content, a.secretValues()), nil
}

func (a *Activities) profile(roleProfile string, spec run.ModelSpec) config.ModelProfile {
	name := roleProfile
	if spec.Profile != "" {
		if _, err := a.Config.Profile(spec.Profile); err == nil {
			name = spec.Profile
		}
	}
	p, err := a.Config.Profile(name)
	if err != nil {
		p = config.Default().WorkerProfile()
	}
	if spec.BaseURL != "" {
		p.BaseURL = spec.BaseURL
	}
	if spec.Model != "" {
		p.Model = spec.Model
	}
	if spec.APIKeyEnv != "" {
		p.APIKeyEnv = spec.APIKeyEnv
	}
	if spec.Temperature != nil {
		p.Temperature = *spec.Temperature
	}
	if spec.MaxTokens > 0 {
		p.MaxTokens = spec.MaxTokens
	}
	if spec.Timeout > 0 {
		p.Timeout = spec.Timeout
	}
	if spec.JSONMode != "" {
		p.JSONMode = spec.JSONMode
	}
	if spec.ToolProtocol != "" {
		p.ToolProtocol = spec.ToolProtocol
	}
	if spec.MaxToolTurns > 0 {
		p.MaxToolTurns = spec.MaxToolTurns
	}
	if spec.DecisionStyle != "" {
		p.DecisionStyle = spec.DecisionStyle
	}
	if spec.TokenLimitField != "" {
		p.TokenLimitField = spec.TokenLimitField
	}
	if len(spec.ExtraBody) > 0 {
		p.ExtraBody = spec.ExtraBody
	}
	if len(spec.ExtraHeaders) > 0 {
		p.ExtraHeaders = spec.ExtraHeaders
	}
	if spec.Logprobs {
		p.Logprobs = true
	}
	return p
}

func (a *Activities) commandEnv(ws run.Workspace) []string {
	if ws.Kind != "docker" {
		return os.Environ()
	}
	var env []string
	if path, ok := os.LookupEnv("PATH"); ok {
		env = append(env, "PATH="+path)
	}
	for _, name := range a.Config.Runner.SecretEnv {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func (a *Activities) writeLog(in run.ExecuteInput, name, body string) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", nil
	}
	dir := filepath.Join(a.Config.ArtifactsDir, sanitize(in.RunID), sanitize(in.Node.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (a *Activities) secretValues() []string {
	var vals []string
	for _, name := range a.Config.Runner.SecretEnv {
		if v := os.Getenv(name); len(v) >= 6 {
			vals = append(vals, v)
		}
	}
	for _, profile := range a.Config.Models {
		if key := profile.APIKeyValue(); len(key) >= 6 {
			vals = append(vals, key)
		}
	}
	return vals
}

func errorResult(summary string, artifacts []string) run.Result {
	if artifacts == nil {
		artifacts = []string{}
	}
	return run.Result{Status: "error", Summary: summary, Findings: []string{}, Artifacts: artifacts, Data: map[string]any{}}
}

func startHeartbeat(ctx context.Context) func() {
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	hb, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-hb.Done():
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, "running")
			}
		}
	}()
	return cancel
}

func sanitize(id string) string {
	id = strings.ReplaceAll(id, string(os.PathSeparator), "_")
	id = strings.ReplaceAll(id, "/", "_")
	if id == "" {
		return "run"
	}
	return id
}

func asResultError(err error) (run.Result, error) {
	var httpErr *model.HTTPError
	if errors.As(err, &httpErr) && !httpErr.Temporary() {
		return errorResult(err.Error(), nil), nil
	}
	return run.Result{}, err
}
