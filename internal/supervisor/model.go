package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ALPHAvibe/shakedown/internal/model"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

// DecisionSchema is the constrained JSON object the supervisor model must return.
var DecisionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"decision":   map[string]any{"type": "string", "enum": []any{"continue", "stop"}},
		"edge":       map[string]any{"type": "string"},
		"reason":     map[string]any{"type": "string"},
		"confidence": map[string]any{"type": "number"},
	},
	"required": []any{"decision", "edge", "reason"},
}

// Model asks a configured chat model to break ties the rules layer abstains on.
type Model struct {
	Client        *model.Client
	MinConfidence float64
}

func (m Model) Decide(ctx context.Context, in run.DecideInput) (run.Decision, error) {
	if m.Client == nil || m.Client.Profile.BaseURL == "" || m.Client.Profile.Model == "" {
		return run.Decision{
			Decision:  run.DecisionAbstain,
			Backend:   "model",
			Reason:    "no supervisor model configured",
			Execution: in.Result.ExecutionID,
		}, nil
	}
	if m.Client.Profile.DecisionStyle == "yesno" {
		return m.decideYesNo(ctx, in)
	}
	return m.decideJSON(ctx, in)
}

func (m Model) decideJSON(ctx context.Context, in run.DecideInput) (run.Decision, error) {
	messages := []model.Message{
		{Role: "system", Content: supervisorSystem},
		{Role: "user", Content: supervisorUser(in)},
	}
	temp := 0.0
	resp, err := m.Client.Chat(ctx, model.ChatRequest{
		Messages:    messages,
		Schema:      DecisionSchema,
		SchemaName:  "shakedown_decision",
		Temperature: &temp,
	})
	if err != nil {
		return run.Decision{}, err
	}
	d, err := parseDecision(resp.Content)
	if err != nil {
		messages = append(messages,
			model.Message{Role: "assistant", Content: resp.Content},
			model.Message{Role: "user", Content: "That was not the required JSON object. " + err.Error() + " Reply with only the JSON object."},
		)
		resp, err = m.Client.Chat(ctx, model.ChatRequest{
			Messages:    messages,
			Schema:      DecisionSchema,
			SchemaName:  "shakedown_decision",
			Temperature: &temp,
		})
		if err != nil {
			return run.Decision{}, err
		}
		d, err = parseDecision(resp.Content)
		if err != nil {
			return run.Decision{
				Decision:  run.DecisionStop,
				Reason:    "supervisor model returned invalid JSON: " + err.Error(),
				Backend:   "model",
				Execution: in.Result.ExecutionID,
			}, nil
		}
	}
	return m.finish(in, d)
}

func (m Model) decideYesNo(ctx context.Context, in run.DecideInput) (run.Decision, error) {
	if len(in.Node.Next) == 0 {
		return run.Decision{
			Decision:  run.DecisionStop,
			Reason:    "no outgoing edges",
			Backend:   "model",
			Execution: in.Result.ExecutionID,
		}, nil
	}
	bestEdge := ""
	bestYes := -1.0
	var notes []string
	for _, edge := range in.Node.Next {
		resp, err := m.Client.Chat(ctx, model.ChatRequest{
			Messages: []model.Message{
				{Role: "system", Content: "Answer with one token: yes or no."},
				{Role: "user", Content: yesNoPrompt(in, edge.To, edge.When)},
			},
			Logprobs:    true,
			Temperature: floatPtr(0),
			JSONMode:    "prompt",
		})
		if err != nil {
			return run.Decision{}, err
		}
		yes, no, ok := model.YesNoProbability(resp)
		if !ok {
			notes = append(notes, edge.To+": unreadable")
			continue
		}
		notes = append(notes, fmt.Sprintf("%s yes=%.2f no=%.2f", edge.To, yes, no))
		if yes > no && yes > bestYes {
			bestYes = yes
			bestEdge = edge.To
		}
	}
	reason := strings.Join(notes, "; ")
	if bestEdge == "" || bestYes < 0.5 {
		conf := 0.0
		if bestYes >= 0 {
			conf = 1 - bestYes
		}
		return run.Decision{
			Decision:   run.DecisionStop,
			Reason:     "yes/no supervisor chose stop (" + reason + ")",
			Backend:    "model",
			Confidence: conf,
			Execution:  in.Result.ExecutionID,
		}, nil
	}
	return m.finish(in, run.Decision{
		Decision:   run.DecisionContinue,
		Edge:       bestEdge,
		Reason:     "yes/no supervisor chose " + bestEdge + " (" + reason + ")",
		Confidence: bestYes,
	})
}

func (m Model) finish(in run.DecideInput, d run.Decision) (run.Decision, error) {
	d.Backend = "model"
	d.Execution = in.Result.ExecutionID
	min := m.MinConfidence
	if in.MinConfidence > 0 {
		min = in.MinConfidence
	}
	if d.Confidence > 0 && min > 0 && d.Confidence < min {
		d.Decision = run.DecisionAbstain
		if d.Reason == "" {
			d.Reason = "confidence below threshold"
		}
		return d, nil
	}
	switch d.Decision {
	case run.DecisionStop:
		if d.Reason == "" {
			d.Reason = "model chose stop"
		}
		return d, nil
	case run.DecisionContinue:
		if !knownEdge(in, d.Edge) {
			return run.Decision{
				Decision:  run.DecisionStop,
				Reason:    fmt.Sprintf("model chose unknown edge %q", d.Edge),
				Backend:   "model",
				Execution: in.Result.ExecutionID,
			}, nil
		}
		if d.Reason == "" {
			d.Reason = "model chose " + d.Edge
		}
		return d, nil
	default:
		return run.Decision{
			Decision:  run.DecisionAbstain,
			Backend:   "model",
			Reason:    "model returned no decision",
			Execution: in.Result.ExecutionID,
		}, nil
	}
}

func knownEdge(in run.DecideInput, edge string) bool {
	for _, next := range in.Node.Next {
		if next.To == edge {
			return true
		}
	}
	return false
}

func parseDecision(content string) (run.Decision, error) {
	raw, err := model.ExtractJSON(content)
	if err != nil {
		return run.Decision{}, err
	}
	var d run.Decision
	if err := json.Unmarshal(raw, &d); err != nil {
		return run.Decision{}, err
	}
	switch d.Decision {
	case run.DecisionContinue, run.DecisionStop:
	default:
		return run.Decision{}, fmt.Errorf("decision must be continue or stop")
	}
	if strings.TrimSpace(d.Reason) == "" {
		return run.Decision{}, fmt.Errorf("reason is required")
	}
	if d.Decision == run.DecisionContinue && d.Edge == "" {
		return run.Decision{}, fmt.Errorf("continue requires an edge")
	}
	d.Backend = ""
	return d, nil
}

func supervisorUser(in run.DecideInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Execution %s returned status %q.\n", in.Result.ExecutionID, in.Result.Status)
	if in.Result.Summary != "" {
		fmt.Fprintf(&b, "Summary: %s\n", truncate(in.Result.Summary, 800))
	}
	if len(in.Result.Findings) > 0 {
		fmt.Fprintf(&b, "Findings: %s\n", truncate(strings.Join(in.Result.Findings, "; "), 800))
	}
	b.WriteString("Edges:\n")
	for _, edge := range in.Node.Next {
		if edge.When == "" {
			fmt.Fprintf(&b, "- %s (default)\n", edge.To)
		} else {
			fmt.Fprintf(&b, "- %s when %s\n", edge.To, edge.When)
		}
	}
	if len(in.History) > 0 {
		b.WriteString("History:\n")
		start := 0
		if len(in.History) > 20 {
			start = len(in.History) - 20
		}
		for _, res := range in.History[start:] {
			fmt.Fprintf(&b, "- %s %s %s\n", res.ExecutionID, res.Status, truncate(res.Summary, 200))
		}
	}
	b.WriteString("Choose one edge or stop. The rules layer abstained because this result was ambiguous.")
	return b.String()
}

func yesNoPrompt(in run.DecideInput, edge, when string) string {
	cond := "no condition"
	if when != "" {
		cond = when
	}
	return fmt.Sprintf("Execution %s status %s. Summary: %s. Should the run follow edge %s (%s)? Answer yes or no.",
		in.Result.ExecutionID, in.Result.Status, truncate(in.Result.Summary, 400), edge, cond)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func floatPtr(v float64) *float64 { return &v }

const supervisorSystem = `You are the Shakedown supervisor. You do not run tools or edit code.
Reply with one JSON object and no markdown:
{"decision":"continue","edge":"<id>","reason":"<why>","confidence":0.0}
decision is continue or stop. edge is empty when you stop, otherwise it must be one of the listed edge ids.
reason is a short explanation for the report. confidence is optional, from 0 to 1.`
