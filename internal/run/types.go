package run

import (
	"time"

	"github.com/ALPHAvibe/shakedown/internal/graph"
)

const (
	SetupActivity    = "shakedown.Setup"
	ExecuteActivity  = "shakedown.Execute"
	DecideActivity   = "shakedown.Decide"
	ReportActivity   = "shakedown.Report"
	TeardownActivity = "shakedown.Teardown"

	ControlSignalName = "shakedown-control"
	QueryName         = "shakedown-status"

	DecisionContinue = "continue"
	DecisionStop     = "stop"
	DecisionAbstain  = "abstain"
)

// ModelSpec overrides the worker's model profile for one run. API keys are
// named by environment variable so they are not stored in workflow history.
type ModelSpec struct {
	Profile         string            `json:"profile,omitempty"`
	BaseURL         string            `json:"base_url,omitempty"`
	Model           string            `json:"model,omitempty"`
	APIKeyEnv       string            `json:"api_key_env,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	Timeout         time.Duration     `json:"timeout,omitempty"`
	JSONMode        string            `json:"json_mode,omitempty"`
	ToolProtocol    string            `json:"tool_protocol,omitempty"`
	MaxToolTurns    int               `json:"max_tool_turns,omitempty"`
	DecisionStyle   string            `json:"decision_style,omitempty"`
	TokenLimitField string            `json:"token_limit_field,omitempty"`
	ExtraBody       map[string]any    `json:"extra_body,omitempty"`
	ExtraHeaders    map[string]string `json:"extra_headers,omitempty"`
	Logprobs        bool              `json:"logprobs,omitempty"`
}

func (m ModelSpec) Empty() bool {
	return m.Profile == "" && m.BaseURL == "" && m.Model == "" && m.APIKeyEnv == "" &&
		m.Temperature == nil && m.MaxTokens == 0 && m.Timeout == 0 && m.JSONMode == "" &&
		m.ToolProtocol == "" && m.MaxToolTurns == 0 && m.DecisionStyle == "" &&
		m.TokenLimitField == "" && len(m.ExtraBody) == 0 && len(m.ExtraHeaders) == 0 && !m.Logprobs
}

// RunInput is the durable argument to ShakedownRun.
type RunInput struct {
	Graph                 graph.Graph            `json:"graph"`
	Inputs                map[string]graph.Value `json:"inputs"`
	MaxSteps              int                    `json:"max_steps"`
	SameExecutionFailures int                    `json:"same_execution_failures"`
	AreaFailures          int                    `json:"area_failures"`
	SupervisorChain       []string               `json:"supervisor_chain"`
	MinConfidence         float64                `json:"min_confidence"`
	WorkerModel           ModelSpec              `json:"worker_model"`
	SupervisorModel       ModelSpec              `json:"supervisor_model"`
	ReportSinks           []SinkSpec             `json:"report_sinks,omitempty"`
}

type SinkSpec struct {
	Kind       string `json:"kind"`
	WebhookEnv string `json:"webhook_env,omitempty"`
	TokenEnv   string `json:"token_env,omitempty"`
	Repo       string `json:"repo,omitempty"`
	PRInput    string `json:"pr_input,omitempty"`
}

type Workspace struct {
	Kind          string `json:"kind"`
	Dir           string `json:"dir"`
	HostDir       string `json:"host_dir"`
	ContainerName string `json:"container_name,omitempty"`
}

type ToolTrace struct {
	Name     string   `json:"name"`
	Args     []string `json:"args,omitempty"`
	ExitCode int      `json:"exit_code"`
	Output   string   `json:"output,omitempty"`
}

// Result is the structured output of one execution.
type Result struct {
	ExecutionID string         `json:"execution_id"`
	Area        string         `json:"area,omitempty"`
	Status      string         `json:"status"`
	Findings    []string       `json:"findings"`
	Artifacts   []string       `json:"artifacts"`
	Summary     string         `json:"summary,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
	ExitCode    int            `json:"exit_code,omitempty"`
	Error       string         `json:"error,omitempty"`
	ToolTrace   []ToolTrace    `json:"tool_trace,omitempty"`
	StartedAt   time.Time      `json:"started_at"`
	FinishedAt  time.Time      `json:"finished_at"`
}

func (r Result) Failed() bool {
	return r.Status == "fail" || r.Status == "error"
}

// Scope is the when-expression object for this result.
func (r Result) Scope(inputs map[string]graph.Value) map[string]any {
	data := map[string]any{}
	for k, v := range r.Data {
		data[k] = v
	}
	findings := r.Findings
	if findings == nil {
		findings = []string{}
	}
	artifacts := r.Artifacts
	if artifacts == nil {
		artifacts = []string{}
	}
	base := map[string]any{
		"status":    r.Status,
		"summary":   r.Summary,
		"findings":  toAny(findings),
		"artifacts": toAny(artifacts),
		"data":      data,
		"exit_code": r.ExitCode,
		"execution": r.ExecutionID,
	}
	return graph.Scope(inputs, base)
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// Decision is a supervisor answer. Abstain means a later layer should decide.
type Decision struct {
	Decision   string  `json:"decision"`
	Edge       string  `json:"edge,omitempty"`
	Reason     string  `json:"reason"`
	Backend    string  `json:"backend,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Execution  string  `json:"execution,omitempty"`
}

func (d Decision) Confident() bool {
	switch d.Decision {
	case DecisionStop:
		return true
	case DecisionContinue:
		return d.Edge != ""
	default:
		return false
	}
}

type ControlSignal struct {
	Action    string `json:"action"`
	Edge      string `json:"edge,omitempty"`
	Execution string `json:"execution,omitempty"`
}

type Status struct {
	RunID       string     `json:"run_id"`
	Graph       string     `json:"graph"`
	Phase       string     `json:"phase"`
	CurrentNode string     `json:"current_node,omitempty"`
	Results     []Result   `json:"results"`
	Decisions   []Decision `json:"decisions"`
	StopReason  string     `json:"stop_reason,omitempty"`
}

type SetupInput struct {
	RunID    string `json:"run_id"`
	GraphDir string `json:"graph_dir"`
}

type ExecuteInput struct {
	RunID     string                 `json:"run_id"`
	GraphDir  string                 `json:"graph_dir"`
	Node      graph.Execution        `json:"node"`
	Inputs    map[string]graph.Value `json:"inputs"`
	Prior     []Result               `json:"prior"`
	Workspace Workspace              `json:"workspace"`
	Model     ModelSpec              `json:"model"`
	SecretEnv []string               `json:"secret_env,omitempty"`
}

type DecideInput struct {
	Node                  graph.Execution        `json:"node"`
	Rules                 []graph.Rule           `json:"rules"`
	Result                Result                 `json:"result"`
	History               []Result               `json:"history"`
	Inputs                map[string]graph.Value `json:"inputs"`
	SameExecutionFailures int                    `json:"same_execution_failures"`
	AreaFailures          int                    `json:"area_failures"`
	Chain                 []string               `json:"chain"`
	Model                 ModelSpec              `json:"model"`
	MinConfidence         float64                `json:"min_confidence"`
}

type ReportInput struct {
	RunID                 string                 `json:"run_id"`
	GraphName             string                 `json:"graph_name"`
	Graph                 graph.Graph            `json:"graph"`
	Status                string                 `json:"status"`
	Reason                string                 `json:"reason,omitempty"`
	StartedAt             time.Time              `json:"started_at"`
	Results               []Result               `json:"results"`
	Decisions             []Decision             `json:"decisions"`
	Teardown              TeardownResult         `json:"teardown"`
	Model                 ModelSpec              `json:"model"`
	Sinks                 []SinkSpec             `json:"sinks,omitempty"`
	Inputs                map[string]graph.Value `json:"inputs"`
	SupervisorChain       []string               `json:"supervisor_chain,omitempty"`
	SameExecutionFailures int                    `json:"same_execution_failures,omitempty"`
	AreaFailures          int                    `json:"area_failures,omitempty"`
	MinConfidence         float64                `json:"min_confidence,omitempty"`
}

type TeardownInput struct {
	RunID     string                 `json:"run_id"`
	GraphDir  string                 `json:"graph_dir"`
	Workspace Workspace              `json:"workspace"`
	Hook      *graph.Hook            `json:"hook,omitempty"`
	Inputs    map[string]graph.Value `json:"inputs"`
	SecretEnv []string               `json:"secret_env,omitempty"`
}

type TeardownResult struct {
	Status  string `json:"status"`
	Summary string `json:"summary,omitempty"`
}

type Report struct {
	RunID                 string                 `json:"run_id"`
	Graph                 string                 `json:"graph"`
	Status                string                 `json:"status"`
	Reason                string                 `json:"reason,omitempty"`
	StartedAt             time.Time              `json:"started_at"`
	FinishedAt            time.Time              `json:"finished_at"`
	Summary               string                 `json:"summary,omitempty"`
	Results               []Result               `json:"results"`
	Decisions             []Decision             `json:"decisions"`
	Teardown              TeardownResult         `json:"teardown"`
	MarkdownPath          string                 `json:"markdown_path"`
	JSONPath              string                 `json:"json_path"`
	SinkErrors            []string               `json:"sink_errors,omitempty"`
	GraphSnapshot         graph.Graph            `json:"graph_snapshot"`
	Inputs                map[string]graph.Value `json:"inputs,omitempty"`
	SupervisorChain       []string               `json:"supervisor_chain,omitempty"`
	SameExecutionFailures int                    `json:"same_execution_failures,omitempty"`
	AreaFailures          int                    `json:"area_failures,omitempty"`
	MinConfidence         float64                `json:"min_confidence,omitempty"`
}
