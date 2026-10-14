package activity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ALPHAvibe/shakedown/internal/config"
	"github.com/ALPHAvibe/shakedown/internal/graph"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

func TestExecuteScriptQuotesInputs(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "greet.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	acts := testActivities(t, config.Default())
	ws, err := acts.Setup(context.Background(), run.SetupInput{RunID: "t", GraphDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	res, err := acts.Execute(context.Background(), run.ExecuteInput{
		RunID:     "t",
		GraphDir:  dir,
		Node:      graph.Execution{ID: "greet", Run: []string{script + " {{ inputs.name }}"}},
		Inputs:    map[string]graph.Value{"name": {Kind: "string", Str: "$(echo pwned)"}},
		Workspace: ws,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "pass" || !strings.Contains(res.Summary, "$(echo pwned)") {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("shell substitution ran")
	}
}

func TestExecutePromptUsesConfiguredModel(t *testing.T) {
	var modelName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := decode(r, &body); err != nil {
			t.Error(err)
		}
		modelName, _ = body["model"].(string)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"status\":\"pass\",\"summary\":\"explored\"}"}}]}`))
	}))
	defer srv.Close()
	cfg := config.Default()
	profile := cfg.WorkerProfile()
	profile.BaseURL = srv.URL + "/v1"
	profile.Model = "model-under-test"
	profile.JSONMode = "prompt"
	profile.ToolProtocol = "text"
	cfg.Models[cfg.Roles.Worker] = profile
	acts := testActivities(t, cfg)
	dir := t.TempDir()
	ws, err := acts.Setup(context.Background(), run.SetupInput{RunID: "t", GraphDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	res, err := acts.Execute(context.Background(), run.ExecuteInput{
		RunID:     "t",
		GraphDir:  dir,
		Workspace: ws,
		Node:      graph.Execution{ID: "explore", PromptBody: "Look around."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "pass" || res.Summary != "explored" {
		t.Fatalf("%+v", res)
	}
	if modelName != "model-under-test" {
		t.Fatalf("model %s", modelName)
	}
}

func TestRejectsToolOutsideAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"tool\":\"missing\",\"args\":[]}"}}]}`))
	}))
	defer srv.Close()
	cfg := config.Default()
	profile := cfg.WorkerProfile()
	profile.BaseURL = srv.URL + "/v1"
	profile.Model = "model-under-test"
	profile.ToolProtocol = "text"
	profile.MaxToolTurns = 1
	cfg.Models[cfg.Roles.Worker] = profile
	acts := testActivities(t, cfg)
	dir := t.TempDir()
	ws, err := acts.Setup(context.Background(), run.SetupInput{RunID: "t", GraphDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	res, err := acts.Execute(context.Background(), run.ExecuteInput{
		RunID: "t", GraphDir: dir, Workspace: ws,
		Node: graph.Execution{ID: "explore", PromptBody: "use tools", Tools: []string{"scripts/ok.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "error" {
		t.Fatalf("%+v", res)
	}
}

func TestToolPathCannotEscapeGraph(t *testing.T) {
	_, err := allowTools(t.TempDir(), run.Workspace{Dir: "/work"}, []string{"../secret.sh"})
	if err == nil {
		t.Fatal("expected escape to be rejected")
	}
}

func testActivities(t *testing.T, cfg config.Config) *Activities {
	t.Helper()
	acts, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return acts
}
