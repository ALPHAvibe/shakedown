package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ALPHAvibe/shakedown/internal/config"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

// Jev is an optional HTTP backend. It is off unless a base URL is configured.
// The request and response are the adapter contract; point decide_path at a
// gateway if the upstream API differs.
type Jev struct {
	Config config.JevConfig
	HTTP   *http.Client
}

type jevResponse struct {
	Decision   string  `json:"decision"`
	Edge       string  `json:"edge"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

func (j Jev) Decide(ctx context.Context, in run.DecideInput) (run.Decision, error) {
	if !j.Config.Enabled || j.Config.BaseURL == "" {
		return run.Decision{
			Decision:  run.DecisionAbstain,
			Backend:   "jev",
			Reason:    "jev is disabled",
			Execution: in.Result.ExecutionID,
		}, nil
	}
	payload := map[string]any{
		"execution_id": in.Result.ExecutionID,
		"outputs":      in.Result,
		"history":      in.History,
		"edges":        in.Node.Next,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return run.Decision{}, err
	}
	path := j.Config.DecidePath
	if path == "" {
		path = "/decide"
	}
	url := strings.TrimRight(j.Config.BaseURL, "/") + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return run.Decision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if j.Config.APIKeyEnv != "" {
		if key := getenv(j.Config.APIKeyEnv); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	client := j.HTTP
	if client == nil {
		timeout := j.Config.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return run.Decision{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return run.Decision{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return run.Decision{}, fmt.Errorf("jev returned %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var parsed jevResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return run.Decision{}, fmt.Errorf("jev response: %w", err)
	}
	min := j.Config.MinConfidence
	if parsed.Confidence > 0 && min > 0 && parsed.Confidence < min {
		return run.Decision{
			Decision:   run.DecisionAbstain,
			Backend:    "jev",
			Reason:     "jev confidence below threshold",
			Confidence: parsed.Confidence,
			Execution:  in.Result.ExecutionID,
		}, nil
	}
	d := run.Decision{
		Decision:   parsed.Decision,
		Edge:       parsed.Edge,
		Reason:     parsed.Reason,
		Backend:    "jev",
		Confidence: parsed.Confidence,
		Execution:  in.Result.ExecutionID,
	}
	if !d.Confident() {
		d.Decision = run.DecisionAbstain
		if d.Reason == "" {
			d.Reason = "jev abstained"
		}
		return d, nil
	}
	if d.Decision == run.DecisionContinue && !knownEdge(in, d.Edge) {
		return run.Decision{
			Decision:  run.DecisionStop,
			Reason:    fmt.Sprintf("jev chose unknown edge %q", d.Edge),
			Backend:   "jev",
			Execution: in.Result.ExecutionID,
		}, nil
	}
	return d, nil
}

func getenv(key string) string {
	return envLookup(key)
}
