package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/ALPHAvibe/shakedown/internal/config"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

const maxCapture = 64 * 1024

// CommandResult is the captured output of one script or shell command.
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner executes graph commands. Local runs on the host. Docker uses one
// container per run and removes it on teardown.
type Runner interface {
	Setup(ctx context.Context, runID, graphDir string) (run.Workspace, error)
	Shell(ctx context.Context, ws run.Workspace, command string, env []string) (CommandResult, error)
	Exec(ctx context.Context, ws run.Workspace, program string, args []string, env []string) (CommandResult, error)
	Teardown(ctx context.Context, ws run.Workspace) error
}

// New builds the runner selected by config.
func New(cfg config.Config) (Runner, error) {
	switch cfg.Runner.Kind {
	case "docker":
		if cfg.Runner.DockerImage == "" {
			return nil, fmt.Errorf("runner.docker_image is required")
		}
		return &Docker{Image: cfg.Runner.DockerImage, WorkDir: cfg.Runner.WorkDir}, nil
	default:
		return &Local{WorkDir: cfg.Runner.WorkDir}, nil
	}
}

// Local runs commands on the host, in the graph directory.
type Local struct {
	WorkDir string
}

func (l *Local) Setup(_ context.Context, runID, graphDir string) (run.Workspace, error) {
	dir := graphDir
	if l.WorkDir != "" {
		dir = l.WorkDir
	}
	return run.Workspace{Kind: "local", Dir: dir, HostDir: dir, ContainerName: "shakedown-" + sanitize(runID)}, nil
}

func (l *Local) Shell(ctx context.Context, ws run.Workspace, command string, env []string) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	return runCmd(ctx, cmd, ws.Dir, env)
}

func (l *Local) Exec(ctx context.Context, ws run.Workspace, program string, args []string, env []string) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	return runCmd(ctx, cmd, ws.Dir, env)
}

func (l *Local) Teardown(context.Context, run.Workspace) error { return nil }

// Docker keeps one container alive for the run and execs into it.
type Docker struct {
	Image   string
	WorkDir string
}

func (d *Docker) Setup(ctx context.Context, runID, graphDir string) (run.Workspace, error) {
	host := graphDir
	if d.WorkDir != "" {
		host = d.WorkDir
	}
	name := "shakedown-" + sanitize(runID)
	args := []string{
		"run", "-d", "--name", name,
		"-v", host + ":/work",
		"-w", "/work",
		"--entrypoint", "sleep",
		d.Image, "infinity",
	}
	res, err := runHost(ctx, "docker", args...)
	if err != nil {
		return run.Workspace{}, err
	}
	if res.ExitCode != 0 {
		return run.Workspace{}, fmt.Errorf("docker run failed: %s", strings.TrimSpace(res.Stderr+" "+res.Stdout))
	}
	return run.Workspace{Kind: "docker", Dir: "/work", HostDir: host, ContainerName: name}, nil
}

func (d *Docker) Shell(ctx context.Context, ws run.Workspace, command string, env []string) (CommandResult, error) {
	args := dockerExecArgs(ws, env, []string{"sh", "-c", command})
	return runHost(ctx, "docker", args...)
}

func (d *Docker) Exec(ctx context.Context, ws run.Workspace, program string, args []string, env []string) (CommandResult, error) {
	cmd := append([]string{program}, args...)
	return runHost(ctx, "docker", dockerExecArgs(ws, env, cmd)...)
}

func (d *Docker) Teardown(ctx context.Context, ws run.Workspace) error {
	if ws.ContainerName == "" {
		return nil
	}
	res, err := runHost(ctx, "docker", "rm", "-f", ws.ContainerName)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("docker rm: %s", strings.TrimSpace(res.Stderr))
	}
	return nil
}

func dockerExecArgs(ws run.Workspace, env []string, command []string) []string {
	args := []string{"exec", "-w", "/work"}
	for _, item := range env {
		args = append(args, "-e", item)
	}
	args = append(args, ws.ContainerName)
	return append(args, command...)
}

// StreamShell runs a command on the host and copies stdout and stderr to w
// until the command exits or ctx is cancelled.
func StreamShell(ctx context.Context, dir, command string, env []string, w io.Writer) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = env
	}
	configureCmd(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	out := &lockedWriter{w: w}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(out, stdout)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(out, stderr)
	}()
	waitErr := cmd.Wait()
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return waitErr
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

func runHost(ctx context.Context, name string, args ...string) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return runCmd(ctx, cmd, "", nil)
}

func runCmd(ctx context.Context, cmd *exec.Cmd, dir string, env []string) (CommandResult, error) {
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	configureCmd(cmd)
	var stdout, stderr limitWriter
	stdout.max = maxCapture
	stderr.max = maxCapture
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0}
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
		return result, nil
	}
	return result, err
}

type limitWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	remain := w.max - w.buf.Len()
	if remain > 0 {
		if len(p) > remain {
			_, _ = w.buf.Write(p[:remain])
			w.truncated = true
		} else {
			_, _ = w.buf.Write(p)
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil
}

func (w *limitWriter) String() string {
	s := w.buf.String()
	if w.truncated {
		s += "\n...[truncated]"
	}
	return s
}

func sanitize(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		return "run"
	}
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

// Environ returns the host environment, which is how sandbox credentials reach
// scripts without being copied into prompts.
func Environ() []string { return os.Environ() }
