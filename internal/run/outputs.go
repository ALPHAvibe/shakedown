package run

import (
	"encoding/json"
	"strings"

	"github.com/ALPHAvibe/shakedown/internal/model"
)

// ParseOutputs turns script stdout into the execution result. Non-JSON output
// keeps the fallback status derived from the exit code.
func ParseOutputs(stdout, stderr string, exit int, fallback string) Result {
	res := Result{
		Status:    fallback,
		Findings:  []string{},
		Artifacts: []string{},
		Data:      map[string]any{},
		ExitCode:  exit,
	}
	raw, err := model.ExtractJSON(stdout)
	if err != nil {
		text := strings.TrimSpace(stdout)
		errText := strings.TrimSpace(stderr)
		if text != "" {
			res.Summary = truncate(text, 500)
		}
		if fallback != "pass" && errText != "" {
			res.Findings = []string{truncate(errText, 500)}
		}
		if res.Summary == "" && errText != "" {
			res.Summary = truncate(errText, 500)
		}
		return res
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		res.Summary = truncate(strings.TrimSpace(stdout), 500)
		return res
	}
	if status, ok := obj["status"].(string); ok && status != "" {
		res.Status = status
	}
	if summary, ok := obj["summary"].(string); ok {
		res.Summary = summary
	}
	res.Findings = stringField(obj["findings"])
	res.Artifacts = stringField(obj["artifacts"])
	if res.Findings == nil {
		res.Findings = []string{}
	}
	if res.Artifacts == nil {
		res.Artifacts = []string{}
	}
	if data, ok := obj["data"].(map[string]any); ok {
		res.Data = data
	}
	if res.Summary == "" {
		if s, ok := obj["error"].(string); ok {
			res.Summary = s
		}
	}
	switch res.Status {
	case "pass", "fail", "error", "skipped":
	default:
		res.Status = fallback
	}
	return res
}

func stringField(v any) []string {
	switch t := v.(type) {
	case nil:
		return []string{}
	case string:
		if t == "" {
			return []string{}
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, stringifyItem(item))
		}
		return out
	case []string:
		return t
	default:
		return []string{stringifyItem(t)}
	}
}

func stringifyItem(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
