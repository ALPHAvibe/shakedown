package activity

import (
	"context"
	"encoding/json"
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

func TestExecuteCopiesLogStreams(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	if err := os.WriteFile(src, []byte("old-1\nold-2\nold-3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "run.sh")
	body := "#!/bin/sh\nprintf 'new-line\\n' >> " + src + "\nprintf '%s\\n' '{\"status\":\"pass\",\"summary\":\"ok\"}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.ArtifactsDir = t.TempDir()
	acts := testActivities(t, cfg)
	ws, err := acts.Setup(context.Background(), run.SetupInput{RunID: "run-1", GraphDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	res, err := acts.Execute(context.Background(), run.ExecuteInput{
		RunID:     "run-1",
		GraphDir:  dir,
		Workspace: ws,
		Inputs:    map[string]graph.Value{"name": {Kind: "string", Str: "$(echo pwned)"}},
		Node: graph.Execution{
			ID:  "step",
			Run: []string{script},
			Logs: []graph.LogSource{
				{Name: "app", Path: src, TailLines: 2},
				{Name: "events", Run: "printf %s {{ inputs.name }}"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "pass" {
		t.Fatalf("%+v", res)
	}
	app := filepath.Join(cfg.ArtifactsDir, "run-1", "step", "app.log")
	events := filepath.Join(cfg.ArtifactsDir, "run-1", "step", "events.log")
	appBody, err := os.ReadFile(app)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(appBody), "old-1") || !strings.Contains(string(appBody), "old-3") || !strings.Contains(string(appBody), "new-line") {
		t.Fatalf("app log:\n%s", appBody)
	}
	eventBody, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(eventBody), "$(echo pwned)") {
		t.Fatalf("events log:\n%s", eventBody)
	}
	joined := strings.Join(res.Artifacts, " ")
	if !strings.Contains(joined, "app.log") || !strings.Contains(joined, "events.log") {
		t.Fatalf("artifacts %v", res.Artifacts)
	}
}

func TestPromptSeesLogTailNotSecrets(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	secret := "supersecretvalue"
	if err := os.WriteFile(src, []byte("token "+secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOX_TOKEN", secret)
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := decode(r, &body); err != nil {
			t.Error(err)
		}
		raw, _ := json.Marshal(body)
		seen = string(raw)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"status\":\"pass\",\"summary\":\"saw logs\"}"}}]}`))
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.ArtifactsDir = t.TempDir()
	cfg.Runner.SecretEnv = []string{"SANDBOX_TOKEN"}
	profile := cfg.WorkerProfile()
	profile.BaseURL = srv.URL + "/v1"
	profile.Model = "model-under-test"
	profile.ToolProtocol = "text"
	cfg.Models[cfg.Roles.Worker] = profile
	acts := testActivities(t, cfg)
	ws, err := acts.Setup(context.Background(), run.SetupInput{RunID: "t", GraphDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	res, err := acts.Execute(context.Background(), run.ExecuteInput{
		RunID: "t", GraphDir: dir, Workspace: ws,
		Node: graph.Execution{
			ID: "explore", PromptBody: "Look at the logs.",
			Logs: []graph.LogSource{{Name: "app", Path: src, TailLines: 5}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "pass" {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(seen, "[redacted]") || strings.Contains(seen, secret) {
		t.Fatalf("prompt leaked or dropped the log tail: %s", seen)
	}
	if !strings.Contains(seen, "app.log") {
		t.Fatalf("prompt missing artifact path: %s", seen)
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
