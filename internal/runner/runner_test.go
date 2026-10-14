package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/ALPHAvibe/shakedown/internal/run"
)

func TestLocalShell(t *testing.T) {
	r := &Local{}
	ws, err := r.Setup(context.Background(), "run 1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Shell(context.Background(), ws, "printf '%s' hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("%+v", res)
	}
	res, err = r.Shell(context.Background(), ws, "exit 3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit %d", res.ExitCode)
	}
	if err := r.Teardown(context.Background(), run.Workspace{}); err != nil {
		t.Fatal(err)
	}
}

func TestDockerExecArgs(t *testing.T) {
	args := dockerExecArgs(run.Workspace{ContainerName: "shakedown-run"}, []string{"TOKEN=secret"}, []string{"sh", "-c", "echo hi"})
	got := strings.Join(args, " ")
	want := "exec -w /work -e TOKEN=secret shakedown-run sh -c echo hi"
	if got != want {
		t.Fatalf("got %s", got)
	}
}
