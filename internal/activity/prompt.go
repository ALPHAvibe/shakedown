package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ALPHAvibe/shakedown/internal/graph"
	"github.com/ALPHAvibe/shakedown/internal/model"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

type allowedTool struct {
	Name    string
	Rel     string
	Host    string
	Runtime string
}

func (a *Activities) runPrompt(ctx context.Context, in run.ExecuteInput, prompt string, failure *run.Result, logText func() string) (run.Result, error) {
	profile := a.profile(a.Config.Roles.Worker, in.Model)
	client := model.New(profile)
	tools, err := allowTools(in.GraphDir, in.Workspace, in.Node.Tools)
	if err != nil {
		return errorResult(err.Error(), nil), nil
	}
	body, err := graph.Render(prompt, graph.InputScope(in.Inputs), false)
	if err != nil {
		return errorResult(err.Error(), nil), nil
	}
	secrets := a.secretValues()
	body = redact(body, secrets)
	protocol := profile.ToolProtocol
	if protocol == "" {
		protocol = "auto"
	}
	schema := outputSchema(in.Node.Outputs)
	messages := []model.Message{
		{Role: "system", Content: workerSystem(protocol, tools)},
		{Role: "user", Content: workerUser(in, body, failure, schema)},
	}
	turns := profile.MaxToolTurns
	if turns <= 0 {
		turns = 8
	}
	sendTools := protocol != "text"
	rejected := 0
	var traces []run.ToolTrace
	for turn := 0; turn < turns; turn++ {
		if ctx.Err() != nil {
			return run.Result{}, ctx.Err()
		}
		if logText != nil {
			messages = setLogMessage(messages, logText())
		}
		req := model.ChatRequest{
			Messages:   messages,
			Schema:     schema,
			SchemaName: "shakedown_output",
		}
		if sendTools {
			req.Tools = toolDefs(tools)
		}
		resp, err := client.Chat(ctx, req)
		if err != nil && sendTools && protocol == "auto" && isToolRejection(err) {
			sendTools = false
			resp, err = client.Chat(ctx, model.ChatRequest{Messages: messages, Schema: schema, SchemaName: "shakedown_output"})
		}
		if err != nil {
			return asResultError(err)
		}
		resp.Content = redact(resp.Content, secrets)
		calls := resp.ToolCalls
		if len(calls) == 0 && (protocol == "text" || protocol == "auto") {
			if name, args, ok := textTool(resp.Content); ok {
				calls = []model.ToolCall{{
					Name:      name,
					Args:      args,
					ID:        fmt.Sprintf("call-%d", turn+1),
					Arguments: mustArgs(args),
				}}
			}
		}
		if len(calls) == 0 {
			parsed := run.ParseOutputs(resp.Content, "", 0, "error")
			if strings.TrimSpace(resp.Content) == "" {
				parsed.Summary = "model returned an empty response"
			}
			parsed.ToolTrace = traces
			return parsed, nil
		}
		if protocol == "text" || !sendTools {
			messages = append(messages, model.Message{Role: "assistant", Content: resp.Content})
		} else {
			messages = append(messages, model.Message{Role: "assistant", Content: resp.Content, ToolCalls: calls})
		}
		for _, call := range calls {
			tool, ok := findTool(tools, call.Name)
			output := ""
			exit := 0
			if !ok {
				rejected++
				output = "rejected: tool is not allowlisted for this execution"
				exit = 1
			} else {
				res, err := a.Runner.Exec(ctx, in.Workspace, tool.Runtime, call.Args, a.commandEnv(in.Workspace))
				if err != nil {
					return run.Result{}, err
				}
				exit = res.ExitCode
				output = strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
				output = redact(output, secrets)
			}
			output = truncateStr(output, 8000)
			traces = append(traces, run.ToolTrace{Name: call.Name, Args: call.Args, ExitCode: exit, Output: truncateStr(output, 500)})
			content := fmt.Sprintf("Tool %s exit %d\n%s", call.Name, exit, output)
			if protocol == "text" || !sendTools {
				messages = append(messages, model.Message{Role: "user", Content: content})
			} else {
				messages = append(messages, model.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Name:       call.Name,
					Content:    content,
				})
			}
		}
		if rejected >= 3 {
			return run.Result{
				Status:    "error",
				Summary:   "model called tools outside the allowlist",
				Findings:  []string{},
				Artifacts: []string{},
				ToolTrace: traces,
				Data:      map[string]any{},
			}, nil
		}
	}
	return run.Result{
		Status:    "error",
		Summary:   fmt.Sprintf("model did not finish within %d tool turns", turns),
		Findings:  []string{},
		Artifacts: []string{},
		ToolTrace: traces,
		Data:      map[string]any{},
	}, nil
}

func allowTools(graphDir string, ws run.Workspace, rels []string) ([]allowedTool, error) {
	used := map[string]bool{}
	var tools []allowedTool
	root := filepath.Clean(graphDir)
	for _, rel := range rels {
		if filepath.IsAbs(rel) {
			return nil, fmt.Errorf("tool %s must be a relative path", rel)
		}
		host := filepath.Clean(filepath.Join(root, rel))
		relTo, err := filepath.Rel(root, host)
		if err != nil || relTo == ".." || strings.HasPrefix(relTo, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("tool %s escapes the graph directory", rel)
		}
		name := uniqueToolName(rel, used)
		runtime := filepath.Join(ws.Dir, filepath.FromSlash(rel))
		tools = append(tools, allowedTool{Name: name, Rel: rel, Host: host, Runtime: runtime})
	}
	return tools, nil
}

func uniqueToolName(rel string, used map[string]bool) string {
	base := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" {
		name = "tool"
	}
	orig := name
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s_%d", orig, i)
	}
	used[name] = true
	return name
}

func findTool(tools []allowedTool, name string) (allowedTool, bool) {
	for _, tool := range tools {
		if tool.Name == name || tool.Rel == name {
			return tool, true
		}
	}
	return allowedTool{}, false
}

func toolDefs(tools []allowedTool) []model.Tool {
	out := make([]model.Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, model.Tool{
			Name:        tool.Name,
			Description: "Run " + tool.Rel,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"args": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Arguments passed to the script",
					},
				},
			},
		})
	}
	return out
}

func workerSystem(protocol string, tools []allowedTool) string {
	var b strings.Builder
	b.WriteString("You are the Shakedown worker. Follow the execution instructions. ")
	b.WriteString("When you are finished, reply with one JSON object and no markdown. ")
	b.WriteString("The object must include status (pass, fail, or error) and summary. ")
	b.WriteString("Do not invent credentials or repeat secrets. ")
	if protocol == "text" || protocol == "auto" {
		b.WriteString("To call a tool before you finish, reply with {\"tool\":\"<name>\",\"args\":[\"...\"]} and nothing else. ")
	}
	if len(tools) > 0 {
		b.WriteString("Allowlisted tools: ")
		for i, tool := range tools {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(tool.Name)
		}
		b.WriteString(".")
	}
	return b.String()
}

func workerUser(in run.ExecuteInput, body string, failure *run.Result, schema map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Execution: %s\n\nInstructions:\n%s\n", in.Node.ID, body)
	if len(in.Inputs) > 0 {
		names := make([]string, 0, len(in.Inputs))
		for name := range in.Inputs {
			names = append(names, name)
		}
		sort.Strings(names)
		b.WriteString("\nInputs:\n")
		for _, name := range names {
			fmt.Fprintf(&b, "- %s: %s\n", name, in.Inputs[name].String())
		}
	}
	if failure != nil {
		fmt.Fprintf(&b, "\nThe script run failed with status %s.\nSummary: %s\n", failure.Status, failure.Summary)
		if len(failure.Findings) > 0 {
			fmt.Fprintf(&b, "Findings: %s\n", strings.Join(failure.Findings, "; "))
		}
		b.WriteString("Classify the failure in the JSON summary and data fields.\n")
	}
	if len(in.Prior) > 0 {
		b.WriteString("\nPrior results:\n")
		start := 0
		if len(in.Prior) > 20 {
			start = len(in.Prior) - 20
		}
		for _, res := range in.Prior[start:] {
			fmt.Fprintf(&b, "- %s %s %s\n", res.ExecutionID, res.Status, truncateStr(res.Summary, 200))
		}
	}
	encoded, _ := json.Marshal(schema)
	fmt.Fprintf(&b, "\nOutput schema:\n%s\n", encoded)
	return b.String()
}

func outputSchema(custom map[string]any) map[string]any {
	if len(custom) > 0 {
		return custom
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status":    map[string]any{"type": "string", "enum": []any{"pass", "fail", "error"}},
			"findings":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"artifacts": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"summary":   map[string]any{"type": "string"},
			"data":      map[string]any{"type": "object"},
		},
		"required": []any{"status", "summary"},
	}
}

func textTool(content string) (string, []string, bool) {
	raw, err := model.ExtractJSON(content)
	if err != nil {
		return "", nil, false
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return "", nil, false
	}
	if status, _ := obj["status"].(string); status == "pass" || status == "fail" || status == "error" {
		return "", nil, false
	}
	name, _ := obj["tool"].(string)
	if name == "" {
		name, _ = obj["name"].(string)
	}
	if name == "" {
		return "", nil, false
	}
	if args, ok := obj["args"]; ok {
		return name, stringArgs(args), true
	}
	if args, ok := obj["arguments"]; ok {
		if list := stringArgs(args); len(list) > 0 || args != nil {
			if m, ok := args.(map[string]any); ok {
				if inner, ok := m["args"]; ok {
					return name, stringArgs(inner), true
				}
			}
			return name, list, true
		}
	}
	return name, nil, true
}

func stringArgs(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, fmt.Sprint(item))
		}
		return out
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return strings.Fields(t)
	default:
		return nil
	}
}

func mustArgs(args []string) string {
	raw, _ := json.Marshal(map[string]any{"args": args})
	return string(raw)
}

func isToolRejection(err error) bool {
	var httpErr *model.HTTPError
	return errors.As(err, &httpErr) && httpErr.Status == 400
}

func redact(s string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
