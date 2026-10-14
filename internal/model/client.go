package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/ALPHAvibe/shakedown/internal/config"
)

// Client talks to any OpenAI-compatible chat completions endpoint.
type Client struct {
	Profile config.ModelProfile
	HTTP    *http.Client
}

func New(profile config.ModelProfile) *Client {
	timeout := profile.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &Client{Profile: profile, HTTP: &http.Client{Timeout: timeout}}
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Arguments string   `json:"arguments"`
	Args      []string `json:"-"`
}

type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type ChatRequest struct {
	Messages    []Message
	Tools       []Tool
	Schema      map[string]any
	SchemaName  string
	JSONMode    string
	Logprobs    bool
	Temperature *float64
}

type TokenLogprob struct {
	Token   string
	Logprob float64
	Top     []TokenLogprob
}

type ChatResponse struct {
	Content   string
	ToolCalls []ToolCall
	Logprobs  []TokenLogprob
}

type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("model server returned %d: %s", e.Status, trimBody(e.Body))
}

func (e *HTTPError) Temporary() bool {
	return e.Status == 429 || e.Status >= 500
}

// Chat posts one completion. JSON mode and provider-specific fields come from
// the profile so the same call works against different servers.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if c.Profile.BaseURL == "" {
		return ChatResponse{}, fmt.Errorf("model base_url is empty")
	}
	if c.Profile.Model == "" {
		return ChatResponse{}, fmt.Errorf("model name is empty")
	}
	body, err := c.payload(req)
	if err != nil {
		return ChatResponse{}, err
	}
	url := strings.TrimRight(c.Profile.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key := c.Profile.APIKeyValue(); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range c.Profile.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return ChatResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatResponse{}, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	return decodeChat(raw)
}

func (c *Client) payload(req ChatRequest) ([]byte, error) {
	messages := make([]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, messageJSON(m))
	}
	field := c.Profile.TokenLimitField
	if field == "" {
		field = "max_tokens"
	}
	payload := map[string]any{
		"model":    c.Profile.Model,
		"messages": messages,
	}
	temp := c.Profile.Temperature
	if req.Temperature != nil {
		temp = *req.Temperature
	}
	payload["temperature"] = temp
	if c.Profile.MaxTokens > 0 {
		payload[field] = c.Profile.MaxTokens
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			params := tool.Parameters
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tool.Name,
					"description": tool.Description,
					"parameters":  params,
				},
			})
		}
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}
	mode := req.JSONMode
	if mode == "" {
		mode = c.Profile.JSONMode
	}
	if rf := responseFormat(mode, req.SchemaName, req.Schema, c.Profile.ExtraBody); rf != nil {
		payload["response_format"] = rf
	}
	if mode == "grammar" {
		if _, ok := c.Profile.ExtraBody["grammar"]; !ok && req.SchemaName == "shakedown_decision" {
			payload["grammar"] = DecisionGrammar
		}
	}
	wantLogprobs := req.Logprobs || c.Profile.Logprobs
	if wantLogprobs {
		payload["logprobs"] = true
		payload["top_logprobs"] = 5
	}
	for k, v := range c.Profile.ExtraBody {
		payload[k] = v
	}
	return json.Marshal(payload)
}

func messageJSON(m Message) map[string]any {
	out := map[string]any{"role": m.Role}
	if m.Content != "" {
		out["content"] = m.Content
	}
	if m.ToolCallID != "" {
		out["tool_call_id"] = m.ToolCallID
	}
	if m.Name != "" {
		out["name"] = m.Name
	}
	if len(m.ToolCalls) > 0 {
		calls := make([]any, 0, len(m.ToolCalls))
		for _, call := range m.ToolCalls {
			calls = append(calls, map[string]any{
				"id":   call.ID,
				"type": "function",
				"function": map[string]any{
					"name":      call.Name,
					"arguments": call.Arguments,
				},
			})
		}
		out["tool_calls"] = calls
	}
	return out
}

func responseFormat(mode, name string, schema map[string]any, extra map[string]any) any {
	if _, ok := extra["response_format"]; ok {
		return nil
	}
	switch mode {
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "json_schema":
		if schema == nil {
			return map[string]any{"type": "json_object"}
		}
		if name == "" {
			name = "shakedown_output"
		}
		return map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"strict": true,
				"schema": schema,
			},
		}
	default:
		return nil
	}
}

func decodeChat(raw []byte) (ChatResponse, error) {
	var body struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			Logprobs *struct {
				Content []struct {
					Token       string  `json:"token"`
					Logprob     float64 `json:"logprob"`
					TopLogprobs []struct {
						Token   string  `json:"token"`
						Logprob float64 `json:"logprob"`
					} `json:"top_logprobs"`
				} `json:"content"`
			} `json:"logprobs"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return ChatResponse{}, fmt.Errorf("decode model response: %w", err)
	}
	if body.Error != nil && body.Error.Message != "" {
		return ChatResponse{}, fmt.Errorf("model error: %s", body.Error.Message)
	}
	if len(body.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("model response had no choices")
	}
	msg := body.Choices[0].Message
	out := ChatResponse{Content: msg.Content}
	for i, call := range msg.ToolCalls {
		id := call.ID
		if id == "" {
			id = fmt.Sprintf("call-%d", i+1)
		}
		tc := ToolCall{ID: id, Name: call.Function.Name, Arguments: call.Function.Arguments}
		tc.Args = argsFromJSON(call.Function.Arguments)
		out.ToolCalls = append(out.ToolCalls, tc)
	}
	if body.Choices[0].Logprobs != nil {
		for _, tok := range body.Choices[0].Logprobs.Content {
			item := TokenLogprob{Token: tok.Token, Logprob: tok.Logprob}
			for _, top := range tok.TopLogprobs {
				item.Top = append(item.Top, TokenLogprob{Token: top.Token, Logprob: top.Logprob})
			}
			out.Logprobs = append(out.Logprobs, item)
		}
	}
	return out, nil
}

func argsFromJSON(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	var asList []string
	if json.Unmarshal([]byte(raw), &asList) == nil {
		return asList
	}
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return []string{raw}
	}
	if args, ok := obj["args"]; ok {
		return stringList(args)
	}
	if args, ok := obj["arguments"]; ok {
		return stringList(args)
	}
	return nil
}

func stringList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, fmt.Sprint(item))
		}
		return out
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		return strings.Fields(t)
	default:
		return nil
	}
}

// YesNoProbability reads the first token's top logprobs for yes and no.
func YesNoProbability(resp ChatResponse) (yes, no float64, ok bool) {
	if len(resp.Logprobs) == 0 {
		text := strings.ToLower(strings.TrimSpace(resp.Content))
		switch {
		case strings.HasPrefix(text, "yes"):
			return 1, 0, true
		case strings.HasPrefix(text, "no"):
			return 0, 1, true
		default:
			return 0, 0, false
		}
	}
	cands := resp.Logprobs[0].Top
	if len(cands) == 0 {
		cands = []TokenLogprob{{Token: resp.Logprobs[0].Token, Logprob: resp.Logprobs[0].Logprob}}
	}
	found := false
	for _, cand := range cands {
		switch strings.ToLower(strings.TrimSpace(cand.Token)) {
		case "yes":
			yes = math.Exp(cand.Logprob)
			found = true
		case "no":
			no = math.Exp(cand.Logprob)
			found = true
		}
	}
	return yes, no, found
}

// ExtractJSON pulls one JSON object out of a model reply, including fenced blocks.
func ExtractJSON(s string) (json.RawMessage, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if json.Valid([]byte(s)) && (strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")) {
		return json.RawMessage(s), nil
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		chunk := s[start : end+1]
		if json.Valid([]byte(chunk)) {
			return json.RawMessage(chunk), nil
		}
	}
	return nil, fmt.Errorf("no json object in model output")
}

func trimBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return s[:400]
	}
	return s
}

// DecisionGrammar is a llama.cpp grammar for the supervisor decision object.
const DecisionGrammar = `root ::= "{" ws "\"decision\"" ws ":" ws ("\"continue\"" | "\"stop\"") ws "," ws "\"edge\"" ws ":" ws string ws "," ws "\"reason\"" ws ":" ws string ws "}"
ws ::= [ \t\n]*
string ::= "\"" char* "\""
char ::= [^"\\] | "\\" ["\\/bfnrt]
`
