package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ALPHAvibe/shakedown/internal/run"
)

func TestRenderAndWrite(t *testing.T) {
	in := run.ReportInput{
		RunID:     "run/1",
		GraphName: "hello",
		Status:    "completed",
		StartedAt: time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC),
		Results: []run.Result{{
			ExecutionID: "greet", Status: "pass", Summary: "hello",
			Findings: []string{"fine"}, Artifacts: []string{"artifacts/out.txt"},
		}},
		Decisions: []run.Decision{{
			Execution: "greet", Decision: "continue", Edge: "next", Reason: "matched", Backend: "rules",
		}},
		Teardown: run.TeardownResult{Status: "pass", Summary: "cleanup finished"},
	}
	body := Render(in, "All good.", in.StartedAt)
	for _, want := range []string{"# Shakedown report: hello", "`greet` — pass", "Backend: rules", "cleanup finished"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}
	dir := t.TempDir()
	report, err := Write(dir, in, "All good.", in.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "run_1.md")); err != nil {
		t.Fatal(err)
	}
	if report.JSONPath == "" {
		t.Fatal("missing json path")
	}
}
