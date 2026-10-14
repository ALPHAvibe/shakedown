package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultAddress   = "127.0.0.1:7233"
	DefaultNamespace = "default"
	DefaultTaskQueue = "shakedown"
	DefaultUI        = "http://localhost:8233"
	DefaultTailLines = 200
	MaxTailLines     = 2000
)

// Config is process configuration. Model profiles point at any
// OpenAI-compatible endpoint: Ollama, llama.cpp, vLLM, LM Studio, OpenAI,
// OpenRouter, and others.
type Config struct {
	Temporal     TemporalConfig          `yaml:"temporal"`
	Models       map[string]ModelProfile `yaml:"models"`
	Roles        Roles                   `yaml:"roles"`
	Supervisor   SupervisorConfig        `yaml:"supervisor"`
	Runner       RunnerConfig            `yaml:"runner"`
	Report       ReportConfig            `yaml:"report"`
	Logs         LogsConfig              `yaml:"logs"`
	ArtifactsDir string                  `yaml:"artifacts_dir"`
}

type TemporalConfig struct {
	Address    string        `yaml:"address"`
	Namespace  string        `yaml:"namespace"`
	TaskQueue  string        `yaml:"task_queue"`
	UI         string        `yaml:"ui"`
	RunTimeout time.Duration `yaml:"run_timeout"`
}

type Roles struct {
	Worker     string `yaml:"worker"`
	Supervisor string `yaml:"supervisor"`
}

type ModelProfile struct {
	BaseURL         string            `yaml:"base_url" json:"base_url"`
	Model           string            `yaml:"model" json:"model"`
	APIKeyEnv       string            `yaml:"api_key_env" json:"api_key_env,omitempty"`
	APIKey          string            `yaml:"-" json:"-"`
	Temperature     float64           `yaml:"temperature" json:"temperature"`
	MaxTokens       int               `yaml:"max_tokens" json:"max_tokens"`
	Timeout         time.Duration     `yaml:"timeout" json:"timeout"`
	JSONMode        string            `yaml:"json_mode" json:"json_mode"`
	ToolProtocol    string            `yaml:"tool_protocol" json:"tool_protocol"`
	MaxToolTurns    int               `yaml:"max_tool_turns" json:"max_tool_turns"`
	DecisionStyle   string            `yaml:"decision_style" json:"decision_style"`
	TokenLimitField string            `yaml:"token_limit_field" json:"token_limit_field"`
	ExtraBody       map[string]any    `yaml:"extra_body" json:"extra_body,omitempty"`
	ExtraHeaders    map[string]string `yaml:"extra_headers" json:"extra_headers,omitempty"`
	Logprobs        bool              `yaml:"logprobs" json:"logprobs"`
}

type SupervisorConfig struct {
	Chain                 []string  `yaml:"chain"`
	SameExecutionFailures int       `yaml:"same_execution_failures"`
	AreaFailures          int       `yaml:"area_failures"`
	MaxSteps              int       `yaml:"max_steps"`
	MinConfidence         float64   `yaml:"min_confidence"`
	Jev                   JevConfig `yaml:"jev"`
}

type JevConfig struct {
	Enabled       bool          `yaml:"enabled"`
	BaseURL       string        `yaml:"base_url"`
	APIKeyEnv     string        `yaml:"api_key_env"`
	DecidePath    string        `yaml:"decide_path"`
	MinConfidence float64       `yaml:"min_confidence"`
	Timeout       time.Duration `yaml:"timeout"`
}

type RunnerConfig struct {
	Kind        string   `yaml:"kind"`
	DockerImage string   `yaml:"docker_image"`
	WorkDir     string   `yaml:"work_dir"`
	SecretEnv   []string `yaml:"secret_env"`
}

type ReportConfig struct {
	Dir   string       `yaml:"dir"`
	Sinks []SinkConfig `yaml:"sinks"`
}

// LogsConfig controls how much of a captured stream is shown to a model.
// The artifact file keeps the whole capture.
type LogsConfig struct {
	TailLines int `yaml:"tail_lines"`
}

type SinkConfig struct {
	Kind       string `yaml:"kind"`
	WebhookEnv string `yaml:"webhook_env"`
	TokenEnv   string `yaml:"token_env"`
	Repo       string `yaml:"repo"`
	PRInput    string `yaml:"pr_input"`
}

type fileConfig struct {
	Temporal     fileTemporal         `yaml:"temporal"`
	Models       map[string]fileModel `yaml:"models"`
	Roles        Roles                `yaml:"roles"`
	Supervisor   fileSupervisor       `yaml:"supervisor"`
	Runner       RunnerConfig         `yaml:"runner"`
	Report       ReportConfig         `yaml:"report"`
	Logs         LogsConfig           `yaml:"logs"`
	ArtifactsDir string               `yaml:"artifacts_dir"`
}

type fileTemporal struct {
	Address    string `yaml:"address"`
	Namespace  string `yaml:"namespace"`
	TaskQueue  string `yaml:"task_queue"`
	UI         string `yaml:"ui"`
	RunTimeout string `yaml:"run_timeout"`
}

type fileModel struct {
	BaseURL         string            `yaml:"base_url"`
	Model           string            `yaml:"model"`
	APIKeyEnv       string            `yaml:"api_key_env"`
	Temperature     *float64          `yaml:"temperature"`
	MaxTokens       int               `yaml:"max_tokens"`
	Timeout         string            `yaml:"timeout"`
	JSONMode        string            `yaml:"json_mode"`
	ToolProtocol    string            `yaml:"tool_protocol"`
	MaxToolTurns    int               `yaml:"max_tool_turns"`
	DecisionStyle   string            `yaml:"decision_style"`
	TokenLimitField string            `yaml:"token_limit_field"`
	ExtraBody       map[string]any    `yaml:"extra_body"`
	ExtraHeaders    map[string]string `yaml:"extra_headers"`
	Logprobs        bool              `yaml:"logprobs"`
}

type fileSupervisor struct {
	Chain                 []string `yaml:"chain"`
	SameExecutionFailures int      `yaml:"same_execution_failures"`
	AreaFailures          int      `yaml:"area_failures"`
	MaxSteps              int      `yaml:"max_steps"`
	MinConfidence         float64  `yaml:"min_confidence"`
	Jev                   fileJev  `yaml:"jev"`
}

type fileJev struct {
	Enabled       bool    `yaml:"enabled"`
	BaseURL       string  `yaml:"base_url"`
	APIKeyEnv     string  `yaml:"api_key_env"`
	DecidePath    string  `yaml:"decide_path"`
	MinConfidence float64 `yaml:"min_confidence"`
	Timeout       string  `yaml:"timeout"`
}

// Default returns a usable config with no model selected yet.
func Default() Config {
	return Config{
		Temporal: TemporalConfig{
			Address:    DefaultAddress,
			Namespace:  DefaultNamespace,
			TaskQueue:  DefaultTaskQueue,
			UI:         DefaultUI,
			RunTimeout: 24 * time.Hour,
		},
		Models: map[string]ModelProfile{
			"default": defaultProfile(),
		},
		Roles: Roles{Worker: "default", Supervisor: "default"},
		Supervisor: SupervisorConfig{
			Chain:                 []string{"rules", "model"},
			SameExecutionFailures: 2,
			AreaFailures:          3,
			MaxSteps:              100,
			Jev: JevConfig{
				APIKeyEnv:     "JEV_API_KEY",
				DecidePath:    "/decide",
				MinConfidence: 0.6,
				Timeout:       30 * time.Second,
			},
		},
		Runner:       RunnerConfig{Kind: "local"},
		Report:       ReportConfig{Dir: "reports"},
		Logs:         LogsConfig{TailLines: DefaultTailLines},
		ArtifactsDir: "artifacts",
	}
}

func defaultProfile() ModelProfile {
	return ModelProfile{
		Temperature:     0.2,
		MaxTokens:       4096,
		Timeout:         10 * time.Minute,
		JSONMode:        "prompt",
		ToolProtocol:    "auto",
		MaxToolTurns:    8,
		DecisionStyle:   "json",
		TokenLimitField: "max_tokens",
	}
}

// Load reads path when it exists, then applies environment overrides.
// A missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		if env := os.Getenv("SHAKEDOWN_CONFIG"); env != "" {
			path = env
		} else if _, err := os.Stat("shakedown.yaml"); err == nil {
			path = "shakedown.yaml"
		}
	}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}
		if err := applyFile(&cfg, raw); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	applyEnv(&cfg)
	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyFile(cfg *Config, raw []byte) error {
	var file fileConfig
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return err
	}
	if file.Temporal.Address != "" {
		cfg.Temporal.Address = file.Temporal.Address
	}
	if file.Temporal.Namespace != "" {
		cfg.Temporal.Namespace = file.Temporal.Namespace
	}
	if file.Temporal.TaskQueue != "" {
		cfg.Temporal.TaskQueue = file.Temporal.TaskQueue
	}
	if file.Temporal.UI != "" {
		cfg.Temporal.UI = file.Temporal.UI
	}
	if file.Temporal.RunTimeout != "" {
		d, err := time.ParseDuration(file.Temporal.RunTimeout)
		if err != nil {
			return fmt.Errorf("temporal.run_timeout: %w", err)
		}
		cfg.Temporal.RunTimeout = d
	}
	if len(file.Models) > 0 {
		cfg.Models = map[string]ModelProfile{}
		for name, fm := range file.Models {
			profile, err := convertModel(name, fm)
			if err != nil {
				return err
			}
			cfg.Models[name] = profile
		}
	}
	if file.Roles.Worker != "" {
		cfg.Roles.Worker = file.Roles.Worker
	}
	if file.Roles.Supervisor != "" {
		cfg.Roles.Supervisor = file.Roles.Supervisor
	}
	if len(file.Supervisor.Chain) > 0 {
		cfg.Supervisor.Chain = file.Supervisor.Chain
	}
	if file.Supervisor.SameExecutionFailures > 0 {
		cfg.Supervisor.SameExecutionFailures = file.Supervisor.SameExecutionFailures
	}
	if file.Supervisor.AreaFailures > 0 {
		cfg.Supervisor.AreaFailures = file.Supervisor.AreaFailures
	}
	if file.Supervisor.MaxSteps > 0 {
		cfg.Supervisor.MaxSteps = file.Supervisor.MaxSteps
	}
	cfg.Supervisor.MinConfidence = file.Supervisor.MinConfidence
	if file.Supervisor.Jev.BaseURL != "" || file.Supervisor.Jev.Enabled || file.Supervisor.Jev.APIKeyEnv != "" {
		j := cfg.Supervisor.Jev
		j.Enabled = file.Supervisor.Jev.Enabled
		if file.Supervisor.Jev.BaseURL != "" {
			j.BaseURL = file.Supervisor.Jev.BaseURL
		}
		if file.Supervisor.Jev.APIKeyEnv != "" {
			j.APIKeyEnv = file.Supervisor.Jev.APIKeyEnv
		}
		if file.Supervisor.Jev.DecidePath != "" {
			j.DecidePath = file.Supervisor.Jev.DecidePath
		}
		if file.Supervisor.Jev.MinConfidence > 0 {
			j.MinConfidence = file.Supervisor.Jev.MinConfidence
		}
		if file.Supervisor.Jev.Timeout != "" {
			d, err := time.ParseDuration(file.Supervisor.Jev.Timeout)
			if err != nil {
				return fmt.Errorf("supervisor.jev.timeout: %w", err)
			}
			j.Timeout = d
		}
		cfg.Supervisor.Jev = j
	}
	if file.Runner.Kind != "" {
		cfg.Runner.Kind = file.Runner.Kind
	}
	if file.Runner.DockerImage != "" {
		cfg.Runner.DockerImage = file.Runner.DockerImage
	}
	if file.Runner.WorkDir != "" {
		cfg.Runner.WorkDir = file.Runner.WorkDir
	}
	if file.Runner.SecretEnv != nil {
		cfg.Runner.SecretEnv = file.Runner.SecretEnv
	}
	if file.Report.Dir != "" {
		cfg.Report.Dir = file.Report.Dir
	}
	if file.Report.Sinks != nil {
		cfg.Report.Sinks = file.Report.Sinks
	}
	if file.ArtifactsDir != "" {
		cfg.ArtifactsDir = file.ArtifactsDir
	}
	if file.Logs.TailLines > 0 {
		cfg.Logs.TailLines = file.Logs.TailLines
	}
	return nil
}

func convertModel(name string, fm fileModel) (ModelProfile, error) {
	p := defaultProfile()
	p.BaseURL = fm.BaseURL
	p.Model = fm.Model
	p.APIKeyEnv = fm.APIKeyEnv
	if fm.Temperature != nil {
		p.Temperature = *fm.Temperature
	}
	if fm.MaxTokens > 0 {
		p.MaxTokens = fm.MaxTokens
	}
	if fm.Timeout != "" {
		d, err := time.ParseDuration(fm.Timeout)
		if err != nil {
			return ModelProfile{}, fmt.Errorf("model %s timeout: %w", name, err)
		}
		p.Timeout = d
	}
	if fm.JSONMode != "" {
		p.JSONMode = fm.JSONMode
	}
	if fm.ToolProtocol != "" {
		p.ToolProtocol = fm.ToolProtocol
	}
	if fm.MaxToolTurns > 0 {
		p.MaxToolTurns = fm.MaxToolTurns
	}
	if fm.DecisionStyle != "" {
		p.DecisionStyle = fm.DecisionStyle
	}
	if fm.TokenLimitField != "" {
		p.TokenLimitField = fm.TokenLimitField
	}
	p.ExtraBody = fm.ExtraBody
	p.ExtraHeaders = fm.ExtraHeaders
	p.Logprobs = fm.Logprobs
	return p, nil
}

func applyEnv(cfg *Config) {
	set := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	set(&cfg.Temporal.Address, "SHAKEDOWN_TEMPORAL_ADDRESS")
	set(&cfg.Temporal.Namespace, "SHAKEDOWN_TEMPORAL_NAMESPACE")
	set(&cfg.Temporal.TaskQueue, "SHAKEDOWN_TASK_QUEUE")
	set(&cfg.Temporal.UI, "SHAKEDOWN_TEMPORAL_UI")

	workerName := or(cfg.Roles.Worker, "default")
	supervisorName := or(cfg.Roles.Supervisor, "default")
	worker := cfg.Models[workerName]
	supervisor := cfg.Models[supervisorName]
	workerSet := false
	supervisorSet := false

	if v := os.Getenv("SHAKEDOWN_BASE_URL"); v != "" {
		worker.BaseURL = v
		supervisor.BaseURL = v
		workerSet, supervisorSet = true, true
	}
	if v := os.Getenv("SHAKEDOWN_MODEL"); v != "" {
		worker.Model = v
		supervisor.Model = v
		workerSet, supervisorSet = true, true
	}
	if v := os.Getenv("SHAKEDOWN_API_KEY_ENV"); v != "" {
		worker.APIKeyEnv = v
		supervisor.APIKeyEnv = v
		workerSet, supervisorSet = true, true
	}
	if v := os.Getenv("SHAKEDOWN_JSON_MODE"); v != "" {
		worker.JSONMode = v
		supervisor.JSONMode = v
		workerSet, supervisorSet = true, true
	}
	if v := os.Getenv("SHAKEDOWN_WORKER_BASE_URL"); v != "" {
		worker.BaseURL = v
		workerSet = true
	}
	if v := os.Getenv("SHAKEDOWN_SUPERVISOR_BASE_URL"); v != "" {
		supervisor.BaseURL = v
		supervisorSet = true
	}
	if v := os.Getenv("SHAKEDOWN_WORKER_MODEL"); v != "" {
		worker.Model = v
		workerSet = true
	}
	if v := os.Getenv("SHAKEDOWN_SUPERVISOR_MODEL"); v != "" {
		supervisor.Model = v
		supervisorSet = true
	}
	if v := os.Getenv("SHAKEDOWN_API_KEY"); v != "" {
		worker.APIKey = v
		supervisor.APIKey = v
		workerSet, supervisorSet = true, true
	}
	if cfg.Roles.Worker == "" {
		cfg.Roles.Worker = "default"
	}
	if cfg.Roles.Supervisor == "" {
		cfg.Roles.Supervisor = "default"
	}
	if workerSet {
		fillProfile(&worker)
	}
	if supervisorSet {
		fillProfile(&supervisor)
	}
	if workerSet || supervisorSet {
		if cfg.Roles.Worker == cfg.Roles.Supervisor && !sameProfile(worker, supervisor) {
			cfg.Roles.Supervisor = "supervisor-override"
			cfg.Models[cfg.Roles.Worker] = worker
			cfg.Models[cfg.Roles.Supervisor] = supervisor
			return
		}
		if workerSet {
			cfg.Models[cfg.Roles.Worker] = worker
		}
		if supervisorSet {
			cfg.Models[cfg.Roles.Supervisor] = supervisor
		}
	}
}

func fillProfile(p *ModelProfile) {
	base := defaultProfile()
	if p.MaxTokens == 0 {
		p.MaxTokens = base.MaxTokens
	}
	if p.Timeout == 0 {
		p.Timeout = base.Timeout
	}
	if p.JSONMode == "" {
		p.JSONMode = base.JSONMode
	}
	if p.ToolProtocol == "" {
		p.ToolProtocol = base.ToolProtocol
	}
	if p.MaxToolTurns == 0 {
		p.MaxToolTurns = base.MaxToolTurns
	}
	if p.DecisionStyle == "" {
		p.DecisionStyle = base.DecisionStyle
	}
	if p.TokenLimitField == "" {
		p.TokenLimitField = base.TokenLimitField
	}
}

func (cfg *Config) normalize() error {
	if cfg.Models == nil {
		cfg.Models = map[string]ModelProfile{}
	}
	if _, ok := cfg.Models["default"]; !ok {
		cfg.Models["default"] = defaultProfile()
	}
	if cfg.Roles.Worker == "" {
		cfg.Roles.Worker = "default"
	}
	if cfg.Roles.Supervisor == "" {
		cfg.Roles.Supervisor = "default"
	}
	if _, ok := cfg.Models[cfg.Roles.Worker]; !ok {
		return fmt.Errorf("roles.worker %q does not match a model profile", cfg.Roles.Worker)
	}
	if _, ok := cfg.Models[cfg.Roles.Supervisor]; !ok {
		return fmt.Errorf("roles.supervisor %q does not match a model profile", cfg.Roles.Supervisor)
	}
	switch cfg.Runner.Kind {
	case "local", "docker":
	default:
		return fmt.Errorf("runner.kind must be local or docker")
	}
	if cfg.Runner.Kind == "docker" && cfg.Runner.DockerImage == "" {
		return fmt.Errorf("runner.docker_image is required when runner.kind is docker")
	}
	for _, layer := range cfg.Supervisor.Chain {
		switch layer {
		case "rules", "model", "jev":
		default:
			return fmt.Errorf("unknown supervisor layer %q", layer)
		}
	}
	for name, p := range cfg.Models {
		switch p.JSONMode {
		case "prompt", "json_object", "json_schema", "grammar":
		default:
			return fmt.Errorf("model %s json_mode must be prompt, json_object, json_schema, or grammar", name)
		}
		switch p.ToolProtocol {
		case "auto", "openai", "text":
		default:
			return fmt.Errorf("model %s tool_protocol must be auto, openai, or text", name)
		}
		switch p.DecisionStyle {
		case "json", "yesno":
		default:
			return fmt.Errorf("model %s decision_style must be json or yesno", name)
		}
	}
	if cfg.Supervisor.Jev.DecidePath == "" {
		cfg.Supervisor.Jev.DecidePath = "/decide"
	}
	if cfg.Report.Dir == "" {
		cfg.Report.Dir = "reports"
	}
	if cfg.ArtifactsDir == "" {
		cfg.ArtifactsDir = "artifacts"
	}
	if cfg.Logs.TailLines == 0 {
		cfg.Logs.TailLines = DefaultTailLines
	}
	if cfg.Logs.TailLines < 0 || cfg.Logs.TailLines > MaxTailLines {
		return fmt.Errorf("logs.tail_lines must be between 1 and %d", MaxTailLines)
	}
	return nil
}

// Profile returns a copy of a named model profile.
func (cfg Config) Profile(name string) (ModelProfile, error) {
	p, ok := cfg.Models[name]
	if !ok {
		return ModelProfile{}, fmt.Errorf("unknown model profile %q", name)
	}
	return p, nil
}

// WorkerProfile is the model that drives prompt executions.
func (cfg Config) WorkerProfile() ModelProfile { return cfg.Models[cfg.Roles.Worker] }

// SupervisorProfile is the model that breaks ties in the supervisor chain.
func (cfg Config) SupervisorProfile() ModelProfile { return cfg.Models[cfg.Roles.Supervisor] }

// APIKey resolves the profile key from an explicit value or the named env var.
func (p ModelProfile) APIKeyValue() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	if p.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(p.APIKeyEnv)
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func sameProfile(a, b ModelProfile) bool {
	return a.BaseURL == b.BaseURL && a.Model == b.Model && a.APIKeyEnv == b.APIKeyEnv &&
		a.APIKey == b.APIKey && a.JSONMode == b.JSONMode && a.ToolProtocol == b.ToolProtocol &&
		a.DecisionStyle == b.DecisionStyle
}

// Redacted returns a copy safe to print.
func (cfg Config) Redacted() Config {
	out := cfg
	out.Models = map[string]ModelProfile{}
	for name, p := range cfg.Models {
		if p.APIKey != "" {
			p.APIKey = "set"
		}
		out.Models[name] = p
	}
	return out
}

// WorkflowUILink is the Temporal Web link for a run.
func (cfg Config) WorkflowUILink(workflowID string) string {
	ui := strings.TrimRight(cfg.Temporal.UI, "/")
	ns := cfg.Temporal.Namespace
	if ns == "" {
		ns = DefaultNamespace
	}
	return ui + "/namespaces/" + ns + "/workflows/" + workflowID
}
