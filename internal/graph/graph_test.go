package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEval(t *testing.T) {
	scope := map[string]any{
		"status": "pass",
		"data":   map[string]any{"n": 2.0, "kind": "flake"},
		"inputs": map[string]any{"ref": "main"},
	}
	cases := []struct {
		expr string
		want bool
	}{
		{`status == "pass"`, true},
		{`status == "fail"`, false},
		{`status != "fail"`, true},
		{`status == "pass" && data.kind == "flake"`, true},
		{`status == "fail" || data.kind == "flake"`, true},
		{`data.n > 1`, true},
		{`data.n >= 2`, true},
		{`data.n < 2`, false},
		{`inputs.ref == "main"`, true},
		{`!(status == "fail")`, true},
		{`(status == "pass")`, true},
	}
	for _, tc := range cases {
		got, err := Eval(tc.expr, scope)
		if err != nil {
			t.Fatalf("%s: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Fatalf("%s = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestParseRejectsJunk(t *testing.T) {
	if err := parseOnly(`status ==`); err == nil {
		t.Fatal("expected a syntax error")
	}
}

func TestRenderShellQuotes(t *testing.T) {
	scope := map[string]any{"inputs": map[string]any{"name": "$(echo pwned)"}}
	got, err := Render("printf %s {{ inputs.name }}", scope, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != `printf %s '$(echo pwned)'` {
		t.Fatalf("got %s", got)
	}
	raw, err := Render("hello {{ inputs.name }}", scope, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "$(echo pwned)") || strings.Contains(raw, "'$(echo") {
		t.Fatalf("prompt render = %s", raw)
	}
}

func TestLoadExamples(t *testing.T) {
	for _, path := range []string{"../../examples/hello/graph.yaml", "../../examples/ctl-api-migrations/graph.yaml"} {
		g, err := Load(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(g.Executions) == 0 {
			t.Fatalf("%s has no executions", path)
		}
	}
	g, err := Load("../../examples/ctl-api-migrations/graph.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if g.StartID() != "provision-sandbox" {
		t.Fatalf("start = %s", g.StartID())
	}
	node, ok := g.Node("api-explore")
	if !ok || node.PromptBody == "" {
		t.Fatal("explore prompt was not inlined")
	}
	if !node.IsTerminal() {
		t.Fatal("api-explore should be terminal")
	}
}

func TestLogSourceValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.yaml")
	body := []byte(`
name: logs
executions:
  - id: step
    run: echo ok
    logs:
      - name: both
        run: echo a
        path: app.log
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected log source validation to fail")
	}
}

func TestResolveInputs(t *testing.T) {
	g, err := Load("../../examples/hello/graph.yaml")
	if err != nil {
		t.Fatal(err)
	}
	vals, err := ResolveInputs(g.Inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if vals["name"].String() != "shakedown" {
		t.Fatalf("default = %s", vals["name"].String())
	}
	vals, err = ResolveInputs(g.Inputs, map[string]string{"name": "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if vals["name"].String() != "ada" {
		t.Fatalf("override = %s", vals["name"].String())
	}
	if _, err := ResolveInputs(g.Inputs, map[string]string{"nope": "x"}); err == nil {
		t.Fatal("expected unknown input error")
	}
}
