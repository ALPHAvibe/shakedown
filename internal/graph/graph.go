package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultTimeout = 30 * time.Minute

// Graph is one declarative run. Prompt bodies are inlined at load time so a
// started run keeps the instructions it was given.
type Graph struct {
	Name       string           `json:"name" yaml:"name"`
	Dir        string           `json:"dir" yaml:"-"`
	Inputs     map[string]Input `json:"inputs" yaml:"inputs"`
	Executions []Execution      `json:"executions" yaml:"executions"`
	Rules      []Rule           `json:"rules,omitempty" yaml:"rules"`
	Teardown   *Hook            `json:"teardown,omitempty" yaml:"teardown"`
}

type Input struct {
	Type     string `json:"type" yaml:"type"`
	Default  *Value `json:"default,omitempty" yaml:"-"`
	Required bool   `json:"required" yaml:"required"`
}

type Execution struct {
	ID         string         `json:"id" yaml:"id"`
	Run        []string       `json:"run,omitempty" yaml:"-"`
	Prompt     string         `json:"prompt,omitempty" yaml:"prompt"`
	PromptBody string         `json:"prompt_body,omitempty" yaml:"-"`
	OnFail     string         `json:"on_fail,omitempty" yaml:"on_fail"`
	OnFailBody string         `json:"on_fail_body,omitempty" yaml:"-"`
	Tools      []string       `json:"tools,omitempty" yaml:"tools"`
	Outputs    map[string]any `json:"outputs,omitempty" yaml:"outputs"`
	Required   bool           `json:"required,omitempty" yaml:"required"`
	Timeout    time.Duration  `json:"timeout" yaml:"-"`
	Next       []Edge         `json:"next,omitempty" yaml:"next"`
	Area       string         `json:"area,omitempty" yaml:"area"`
	Start      bool           `json:"start,omitempty" yaml:"start"`
	Terminal   bool           `json:"terminal,omitempty" yaml:"terminal"`
}

type Edge struct {
	To   string `json:"to" yaml:"to"`
	When string `json:"when,omitempty" yaml:"when"`
}

type Rule struct {
	Name                     string `json:"name" yaml:"name"`
	Execution                string `json:"execution,omitempty" yaml:"execution"`
	When                     string `json:"when,omitempty" yaml:"when"`
	Decision                 string `json:"decision" yaml:"decision"`
	Edge                     string `json:"edge,omitempty" yaml:"edge"`
	Reason                   string `json:"reason,omitempty" yaml:"reason"`
	SameExecutionFailuresGTE int    `json:"same_execution_failures_gte,omitempty" yaml:"same_execution_failures_gte"`
	AreaFailuresGTE          int    `json:"area_failures_gte,omitempty" yaml:"area_failures_gte"`
	Area                     string `json:"area,omitempty" yaml:"area"`
}

type Hook struct {
	Run     []string      `json:"run" yaml:"-"`
	Timeout time.Duration `json:"timeout" yaml:"-"`
}

// Value is a typed run input that survives Temporal's JSON converter.
type Value struct {
	Kind string  `json:"kind"`
	Str  string  `json:"str,omitempty"`
	Num  float64 `json:"num,omitempty"`
	Bool bool    `json:"bool,omitempty"`
}

func (v Value) String() string {
	switch v.Kind {
	case "boolean":
		return strconv.FormatBool(v.Bool)
	case "integer":
		return strconv.FormatInt(int64(v.Num), 10)
	case "number":
		if v.Num == float64(int64(v.Num)) {
			return strconv.FormatInt(int64(v.Num), 10)
		}
		return strconv.FormatFloat(v.Num, 'f', -1, 64)
	default:
		return v.Str
	}
}

func (v Value) Any() any {
	switch v.Kind {
	case "boolean":
		return v.Bool
	case "integer", "number":
		return v.Num
	default:
		return v.Str
	}
}

type fileGraph struct {
	Name       string               `yaml:"name"`
	Inputs     map[string]fileInput `yaml:"inputs"`
	Executions []fileExecution      `yaml:"executions"`
	Rules      []Rule               `yaml:"rules"`
	Teardown   *fileHook            `yaml:"teardown"`
}

type fileInput struct {
	Type     string `yaml:"type"`
	Default  any    `yaml:"default"`
	Required *bool  `yaml:"required"`
}

type fileExecution struct {
	ID       string         `yaml:"id"`
	Run      runField       `yaml:"run"`
	Prompt   string         `yaml:"prompt"`
	OnFail   string         `yaml:"on_fail"`
	Tools    []string       `yaml:"tools"`
	Outputs  map[string]any `yaml:"outputs"`
	Required bool           `yaml:"required"`
	Timeout  string         `yaml:"timeout"`
	Next     []Edge         `yaml:"next"`
	Area     string         `yaml:"area"`
	Start    bool           `yaml:"start"`
	Terminal bool           `yaml:"terminal"`
}

type fileHook struct {
	Run     runField `yaml:"run"`
	Timeout string   `yaml:"timeout"`
}

type runField []string

func (r *runField) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		if strings.TrimSpace(s) == "" {
			*r = nil
			return nil
		}
		*r = []string{s}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*r = list
		return nil
	case 0:
		*r = nil
		return nil
	default:
		return fmt.Errorf("run must be a string or a list of strings")
	}
}

// Load reads a graph file and inlines prompt bodies from the graph directory.
func Load(path string) (Graph, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Graph{}, err
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return Graph{}, err
	}
	var file fileGraph
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return Graph{}, fmt.Errorf("parse %s: %w", abs, err)
	}
	g := Graph{
		Name:   file.Name,
		Dir:    filepath.Dir(abs),
		Inputs: map[string]Input{},
		Rules:  file.Rules,
	}
	if g.Name == "" {
		g.Name = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	}
	for name, in := range file.Inputs {
		spec, err := convertInput(name, in)
		if err != nil {
			return Graph{}, err
		}
		g.Inputs[name] = spec
	}
	for _, ex := range file.Executions {
		node, err := convertExecution(g.Dir, ex)
		if err != nil {
			return Graph{}, err
		}
		g.Executions = append(g.Executions, node)
	}
	if file.Teardown != nil {
		timeout := 10 * time.Minute
		if file.Teardown.Timeout != "" {
			timeout, err = time.ParseDuration(file.Teardown.Timeout)
			if err != nil {
				return Graph{}, fmt.Errorf("teardown timeout: %w", err)
			}
		}
		g.Teardown = &Hook{Run: append([]string{}, file.Teardown.Run...), Timeout: timeout}
	}
	if err := g.Validate(); err != nil {
		return Graph{}, err
	}
	return g, nil
}

func convertInput(name string, in fileInput) (Input, error) {
	typ := in.Type
	if typ == "" {
		typ = "string"
	}
	switch typ {
	case "string", "integer", "number", "boolean":
	default:
		return Input{}, fmt.Errorf("input %s: unknown type %q", name, typ)
	}
	spec := Input{Type: typ}
	if in.Required != nil {
		spec.Required = *in.Required
	} else {
		spec.Required = in.Default == nil
	}
	if in.Default != nil {
		val, err := coerce(typ, in.Default, fmt.Sprintf("input %s default", name))
		if err != nil {
			return Input{}, err
		}
		spec.Default = &val
	}
	return spec, nil
}

func convertExecution(dir string, ex fileExecution) (Execution, error) {
	if ex.ID == "" {
		return Execution{}, fmt.Errorf("execution is missing an id")
	}
	node := Execution{
		ID:       ex.ID,
		Run:      append([]string{}, ex.Run...),
		Prompt:   ex.Prompt,
		OnFail:   ex.OnFail,
		Tools:    append([]string{}, ex.Tools...),
		Outputs:  ex.Outputs,
		Required: ex.Required,
		Next:     append([]Edge{}, ex.Next...),
		Area:     ex.Area,
		Start:    ex.Start,
		Terminal: ex.Terminal,
		Timeout:  DefaultTimeout,
	}
	if ex.Timeout != "" {
		d, err := time.ParseDuration(ex.Timeout)
		if err != nil {
			return Execution{}, fmt.Errorf("execution %s timeout: %w", ex.ID, err)
		}
		if d <= 0 {
			return Execution{}, fmt.Errorf("execution %s timeout must be positive", ex.ID)
		}
		node.Timeout = d
	}
	var err error
	if ex.Prompt != "" {
		node.PromptBody, err = readPrompt(dir, ex.Prompt)
		if err != nil {
			return Execution{}, fmt.Errorf("execution %s: %w", ex.ID, err)
		}
	}
	if ex.OnFail != "" {
		node.OnFailBody, err = readPrompt(dir, ex.OnFail)
		if err != nil {
			return Execution{}, fmt.Errorf("execution %s: %w", ex.ID, err)
		}
	}
	return node, nil
}

func readPrompt(dir, rel string) (string, error) {
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, rel)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Validate checks ids, edges, prompts, and when expressions.
func (g Graph) Validate() error {
	if len(g.Executions) == 0 {
		return fmt.Errorf("graph %s has no executions", g.Name)
	}
	ids := map[string]Execution{}
	starts := 0
	for _, ex := range g.Executions {
		if _, ok := ids[ex.ID]; ok {
			return fmt.Errorf("duplicate execution id %q", ex.ID)
		}
		ids[ex.ID] = ex
		if ex.Start {
			starts++
		}
		if ex.Prompt != "" && len(ex.Run) > 0 {
			return fmt.Errorf("execution %s sets both run and prompt; prompt replaces run", ex.ID)
		}
		if ex.Prompt == "" && len(ex.Run) == 0 {
			return fmt.Errorf("execution %s needs a run command or a prompt", ex.ID)
		}
		if len(ex.Tools) > 0 && ex.Prompt == "" && ex.OnFail == "" {
			return fmt.Errorf("execution %s lists tools without a prompt or on_fail", ex.ID)
		}
		for _, edge := range ex.Next {
			if edge.To == "" {
				return fmt.Errorf("execution %s has an edge with an empty target", ex.ID)
			}
			if edge.When != "" {
				if err := parseOnly(edge.When); err != nil {
					return fmt.Errorf("execution %s when %q: %w", ex.ID, edge.When, err)
				}
			}
		}
	}
	if starts > 1 {
		return fmt.Errorf("graph %s marks more than one start execution", g.Name)
	}
	for _, ex := range g.Executions {
		for _, edge := range ex.Next {
			if _, ok := ids[edge.To]; !ok {
				return fmt.Errorf("execution %s edge targets unknown id %q", ex.ID, edge.To)
			}
		}
	}
	for _, rule := range g.Rules {
		if rule.Decision != "stop" && rule.Decision != "continue" {
			return fmt.Errorf("rule %q decision must be continue or stop", rule.Name)
		}
		if rule.Decision == "continue" && rule.Edge == "" {
			return fmt.Errorf("rule %q continues without an edge", rule.Name)
		}
		if rule.Execution != "" {
			if _, ok := ids[rule.Execution]; !ok {
				return fmt.Errorf("rule %q references unknown execution %q", rule.Name, rule.Execution)
			}
		}
		if rule.When != "" {
			if err := parseOnly(rule.When); err != nil {
				return fmt.Errorf("rule %q when %q: %w", rule.Name, rule.When, err)
			}
		}
		if rule.Decision == "continue" {
			if _, ok := ids[rule.Edge]; !ok {
				return fmt.Errorf("rule %q targets unknown edge %q", rule.Name, rule.Edge)
			}
		}
	}
	return nil
}

// StartID is the execution the run begins at.
func (g Graph) StartID() string {
	for _, ex := range g.Executions {
		if ex.Start {
			return ex.ID
		}
	}
	return g.Executions[0].ID
}

// Node returns the execution with id.
func (g Graph) Node(id string) (Execution, bool) {
	for _, ex := range g.Executions {
		if ex.ID == id {
			return ex, true
		}
	}
	return Execution{}, false
}

// IsTerminal reports whether the run should stop after this execution.
func (ex Execution) IsTerminal() bool {
	return ex.Terminal || len(ex.Next) == 0
}

// ResolveInputs applies defaults and checks CLI or schedule overrides.
// Overrides are strings from flags; typed defaults are taken as already typed.
func ResolveInputs(specs map[string]Input, overrides map[string]string) (map[string]Value, error) {
	if specs == nil {
		specs = map[string]Input{}
	}
	out := map[string]Value{}
	for name := range overrides {
		if _, ok := specs[name]; !ok && len(specs) > 0 {
			return nil, fmt.Errorf("unknown input %q", name)
		}
	}
	if len(specs) == 0 {
		for name, raw := range overrides {
			out[name] = Value{Kind: "string", Str: raw}
		}
		return out, nil
	}
	for name, spec := range specs {
		raw, ok := overrides[name]
		if !ok {
			if spec.Default != nil {
				out[name] = *spec.Default
				continue
			}
			if spec.Required {
				return nil, fmt.Errorf("missing required input %q", name)
			}
			out[name] = zeroValue(spec.Type)
			continue
		}
		val, err := coerce(spec.Type, raw, "input "+name)
		if err != nil {
			return nil, err
		}
		out[name] = val
	}
	return out, nil
}

func zeroValue(typ string) Value {
	switch typ {
	case "boolean":
		return Value{Kind: "boolean"}
	case "integer":
		return Value{Kind: "integer"}
	case "number":
		return Value{Kind: "number"}
	default:
		return Value{Kind: "string"}
	}
}

func coerce(typ string, raw any, what string) (Value, error) {
	switch typ {
	case "string":
		return Value{Kind: "string", Str: fmt.Sprint(raw)}, nil
	case "boolean":
		b, err := parseBool(raw)
		if err != nil {
			return Value{}, fmt.Errorf("%s: %w", what, err)
		}
		return Value{Kind: "boolean", Bool: b}, nil
	case "integer":
		n, err := parseNumber(raw)
		if err != nil {
			return Value{}, fmt.Errorf("%s: %w", what, err)
		}
		if n != float64(int64(n)) {
			return Value{}, fmt.Errorf("%s: want an integer", what)
		}
		return Value{Kind: "integer", Num: n}, nil
	case "number":
		n, err := parseNumber(raw)
		if err != nil {
			return Value{}, fmt.Errorf("%s: %w", what, err)
		}
		return Value{Kind: "number", Num: n}, nil
	default:
		return Value{}, fmt.Errorf("%s: unknown type %q", what, typ)
	}
}

func parseBool(raw any) (bool, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			return true, nil
		case "false", "0", "no":
			return false, nil
		default:
			return false, fmt.Errorf("invalid boolean %q", v)
		}
	default:
		return false, fmt.Errorf("invalid boolean %v", raw)
	}
}

func parseNumber(raw any) (float64, error) {
	switch v := raw.(type) {
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(v), 64)
	default:
		if f, ok := asFloat(v); ok {
			return f, nil
		}
		return 0, fmt.Errorf("invalid number %v", raw)
	}
}

// InputScope is the template scope for commands before an execution result exists.
func InputScope(inputs map[string]Value) map[string]any {
	return Scope(inputs, nil)
}
