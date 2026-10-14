package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ALPHAvibe/shakedown/internal/config"
)

func TestChatJSONSchemaAndGrammar(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("auth %s", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &seen); err != nil {
			t.Errorf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"content": `{"decision":"continue","edge":"next","reason":"ok"}`,
					"tool_calls": []any{map[string]any{
						"id":       "call-1",
						"function": map[string]any{"name": "logs", "arguments": `{"args":["tail"]}`},
					}},
				},
				"logprobs": map[string]any{
					"content": []any{map[string]any{
						"token": "yes", "logprob": -0.1,
						"top_logprobs": []any{
							map[string]any{"token": "yes", "logprob": -0.1},
							map[string]any{"token": "no", "logprob": -2.3},
						},
					}},
				},
			}},
		})
	}))
	defer srv.Close()

	client := New(config.ModelProfile{
		BaseURL:         srv.URL + "/v1",
		Model:           "any-model",
		APIKey:          "secret",
		JSONMode:        "json_schema",
		MaxTokens:       128,
		TokenLimitField: "max_tokens",
		Temperature:     0,
		ExtraBody:       map[string]any{"guided_json": true},
	})
	resp, err := client.Chat(context.Background(), ChatRequest{
		Messages:   []Message{{Role: "user", Content: "decide"}},
		Schema:     map[string]any{"type": "object"},
		SchemaName: "shakedown_decision",
		Tools:      []Tool{{Name: "logs", Description: "logs", Parameters: map[string]any{"type": "object"}}},
		Logprobs:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content == "" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "logs" {
		t.Fatalf("response %+v", resp)
	}
	if len(resp.ToolCalls[0].Args) != 1 || resp.ToolCalls[0].Args[0] != "tail" {
		t.Fatalf("args %+v", resp.ToolCalls[0].Args)
	}
	if seen["model"] != "any-model" {
		t.Fatalf("model %v", seen["model"])
	}
	if _, ok := seen["response_format"]; !ok {
		t.Fatal("missing response_format")
	}
	if seen["guided_json"] != true {
		t.Fatalf("extra body was not merged: %#v", seen["guided_json"])
	}
	yes, no, ok := YesNoProbability(resp)
	if !ok || yes <= no {
		t.Fatalf("yes=%v no=%v ok=%v", yes, no, ok)
	}
}

func TestGrammarInjectedForDecisions(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"decision\":\"stop\",\"edge\":\"\",\"reason\":\"done\"}"}}]}`))
	}))
	defer srv.Close()
	client := New(config.ModelProfile{BaseURL: srv.URL + "/v1", Model: "local", JSONMode: "grammar"})
	if _, err := client.Chat(context.Background(), ChatRequest{
		Messages:   []Message{{Role: "user", Content: "decide"}},
		SchemaName: "shakedown_decision",
		JSONMode:   "grammar",
	}); err != nil {
		t.Fatal(err)
	}
	if seen["grammar"] == "" || seen["grammar"] == nil {
		t.Fatalf("grammar not set: %#v", seen["grammar"])
	}
}

func TestExtractJSONFence(t *testing.T) {
	raw, err := ExtractJSON("```json\n{\"status\":\"pass\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"status":"pass"}` {
		t.Fatalf("got %s", raw)
	}
}

func TestHTTPErrorTemporary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()
	client := New(config.ModelProfile{BaseURL: srv.URL + "/v1", Model: "m"})
	_, err := client.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("got %T %v", err, err)
	}
	if httpErr.Temporary() {
		t.Fatal("401 should not be retried")
	}
}
