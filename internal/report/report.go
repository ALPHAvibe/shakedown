package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ALPHAvibe/shakedown/internal/graph"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

// Render builds the Markdown report from structured results. Raw logs stay in
// artifact files; the report lists status, findings, decisions, and links.
func Render(in run.ReportInput, summary string, finished time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Shakedown report: %s\n\n", in.GraphName)
	fmt.Fprintf(&b, "- Run: `%s`\n", in.RunID)
	fmt.Fprintf(&b, "- Status: %s\n", in.Status)
	if in.Reason != "" {
		fmt.Fprintf(&b, "- Reason: %s\n", in.Reason)
	}
	if !in.StartedAt.IsZero() {
		fmt.Fprintf(&b, "- Started: %s\n", in.StartedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "- Finished: %s\n", finished.Format(time.RFC3339))
	if summary != "" {
		fmt.Fprintf(&b, "\n## Summary\n\n%s\n", summary)
	}
	b.WriteString("\n## Path\n\n")
	if len(in.Results) == 0 {
		b.WriteString("No executions ran.\n")
	}
	for i, res := range in.Results {
		line := fmt.Sprintf("%d. `%s` — %s", i+1, res.ExecutionID, res.Status)
		if res.Summary != "" {
			line += " — " + oneLine(res.Summary)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n## Executions\n\n")
	for _, res := range in.Results {
		fmt.Fprintf(&b, "### %s\n\n", res.ExecutionID)
		fmt.Fprintf(&b, "- Status: %s\n", res.Status)
		if res.Summary != "" {
			fmt.Fprintf(&b, "- Summary: %s\n", res.Summary)
		}
		if len(res.Findings) > 0 {
			b.WriteString("- Findings:\n")
			for _, finding := range res.Findings {
				fmt.Fprintf(&b, "  - %s\n", finding)
			}
		}
		if len(res.Artifacts) > 0 {
			b.WriteString("- Artifacts:\n")
			for _, artifact := range res.Artifacts {
				fmt.Fprintf(&b, "  - `%s`\n", artifact)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("## Decisions\n\n")
	if len(in.Decisions) == 0 {
		b.WriteString("No supervisor decisions.\n\n")
	}
	for _, d := range in.Decisions {
		title := d.Execution
		if title == "" {
			title = "run"
		}
		fmt.Fprintf(&b, "### after %s\n\n", title)
		fmt.Fprintf(&b, "- Backend: %s\n", d.Backend)
		fmt.Fprintf(&b, "- Decision: %s\n", d.Decision)
		if d.Edge != "" {
			fmt.Fprintf(&b, "- Edge: `%s`\n", d.Edge)
		}
		if d.Reason != "" {
			fmt.Fprintf(&b, "- Reason: %s\n", d.Reason)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Teardown\n\n")
	if in.Teardown.Status == "" {
		fmt.Fprintf(&b, "- Status: not run\n")
	} else {
		fmt.Fprintf(&b, "- Status: %s\n", in.Teardown.Status)
		if in.Teardown.Summary != "" {
			fmt.Fprintf(&b, "- Summary: %s\n", in.Teardown.Summary)
		}
	}
	return b.String()
}

// SummaryFromResults is the deterministic summary used when no model is configured.
func SummaryFromResults(in run.ReportInput) string {
	if len(in.Results) == 0 {
		if in.Reason != "" {
			return in.Reason
		}
		return "The run ended before any execution."
	}
	passed, failed := 0, 0
	for _, res := range in.Results {
		if res.Status == "pass" {
			passed++
		} else if res.Failed() {
			failed++
		}
	}
	return fmt.Sprintf("%s finished with status %s. %d passed, %d failed.", in.GraphName, in.Status, passed, failed)
}

// Write stores Markdown and a JSON record, then posts configured sinks.
func Write(dir string, in run.ReportInput, summary string, finished time.Time) (run.Report, error) {
	if dir == "" {
		dir = "reports"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return run.Report{}, err
	}
	id := sanitize(in.RunID)
	mdPath := filepath.Join(dir, id+".md")
	jsonPath := filepath.Join(dir, id+".json")
	body := Render(in, summary, finished)
	report := run.Report{
		RunID:                 in.RunID,
		Graph:                 in.GraphName,
		Status:                in.Status,
		Reason:                in.Reason,
		StartedAt:             in.StartedAt,
		FinishedAt:            finished,
		Summary:               summary,
		Results:               in.Results,
		Decisions:             in.Decisions,
		Teardown:              in.Teardown,
		MarkdownPath:          mdPath,
		JSONPath:              jsonPath,
		GraphSnapshot:         in.Graph,
		Inputs:                in.Inputs,
		SupervisorChain:       in.SupervisorChain,
		SameExecutionFailures: in.SameExecutionFailures,
		AreaFailures:          in.AreaFailures,
		MinConfidence:         in.MinConfidence,
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return run.Report{}, err
	}
	if err := os.WriteFile(mdPath, []byte(body), 0o644); err != nil {
		return run.Report{}, err
	}
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		return run.Report{}, err
	}
	for _, sink := range in.Sinks {
		if err := postSink(sink, in.Inputs, body); err != nil {
			report.SinkErrors = append(report.SinkErrors, sink.Kind+": "+err.Error())
		}
	}
	if len(report.SinkErrors) > 0 {
		raw, err = json.MarshalIndent(report, "", "  ")
		if err == nil {
			_ = os.WriteFile(jsonPath, raw, 0o644)
		}
	}
	return report, nil
}

func postSink(sink run.SinkSpec, inputs map[string]graph.Value, body string) error {
	switch sink.Kind {
	case "", "file":
		return nil
	case "slack":
		return postSlack(sink, body)
	case "github":
		return postGitHub(sink, inputs, body)
	default:
		return fmt.Errorf("unknown sink %q", sink.Kind)
	}
}

func postSlack(sink run.SinkSpec, body string) error {
	env := sink.WebhookEnv
	if env == "" {
		env = "SLACK_WEBHOOK_URL"
	}
	url := os.Getenv(env)
	if url == "" {
		return fmt.Errorf("%s is empty", env)
	}
	if len(body) > 39000 {
		body = body[:39000] + "\n...[truncated]"
	}
	payload, _ := json.Marshal(map[string]string{"text": body})
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack returned %d", resp.StatusCode)
	}
	return nil
}

func postGitHub(sink run.SinkSpec, inputs map[string]graph.Value, body string) error {
	env := sink.TokenEnv
	if env == "" {
		env = "GITHUB_TOKEN"
	}
	token := os.Getenv(env)
	if token == "" {
		return fmt.Errorf("%s is empty", env)
	}
	if sink.Repo == "" {
		return fmt.Errorf("github sink is missing repo")
	}
	key := sink.PRInput
	if key == "" {
		key = "pr"
	}
	pr, ok := inputs[key]
	if !ok || pr.String() == "" {
		return fmt.Errorf("input %s is empty", key)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%s/comments", sink.Repo, pr.String())
	payload, _ := json.Marshal(map[string]string{"body": body})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github returned %d", resp.StatusCode)
	}
	return nil
}

func sanitize(id string) string {
	id = strings.ReplaceAll(id, string(os.PathSeparator), "_")
	id = strings.ReplaceAll(id, "/", "_")
	if id == "" {
		return "run"
	}
	return id
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 180 {
		return s[:180] + "..."
	}
	return s
}
