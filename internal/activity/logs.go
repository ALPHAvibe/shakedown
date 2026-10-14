package activity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ALPHAvibe/shakedown/internal/config"
	"github.com/ALPHAvibe/shakedown/internal/graph"
	"github.com/ALPHAvibe/shakedown/internal/logs"
	"github.com/ALPHAvibe/shakedown/internal/model"
	"github.com/ALPHAvibe/shakedown/internal/run"
)

const logMessagePrefix = "Log streams:\n"

type logCapture struct {
	Name      string
	Path      string
	TailLines int
}

// startLogWatches copies each declared stream into an artifact file until stop
// is called. The files exist as soon as this returns, so they can be tailed
// while the execution is still running. stop is safe to call more than once.
func (a *Activities) startLogWatches(parent context.Context, in run.ExecuteInput) ([]logCapture, func()) {
	noop := func() {}
	if len(in.Node.Logs) == 0 {
		return nil, noop
	}
	ctx, cancel := context.WithCancel(parent)
	dir := filepath.Join(a.Config.ArtifactsDir, sanitize(in.RunID), sanitize(in.Node.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		cancel()
		return nil, noop
	}
	caps := make([]logCapture, 0, len(in.Node.Logs))
	var wg sync.WaitGroup
	scope := graph.InputScope(in.Inputs)
	for _, src := range in.Node.Logs {
		dest := filepath.Join(dir, src.Name+".log")
		if err := os.WriteFile(dest, nil, 0o644); err != nil {
			continue
		}
		lines := a.tailLines(src)
		caps = append(caps, logCapture{Name: src.Name, Path: dest, TailLines: lines})
		var seeded string
		if strings.TrimSpace(src.Path) != "" {
			path, renderErr := graph.Render(src.Path, scope, false)
			if renderErr != nil {
				_ = appendNote(dest, renderErr.Error())
				continue
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(in.GraphDir, path)
			}
			seeded = path
			_ = logs.WriteTail(seeded, dest, lines)
		}
		wg.Add(1)
		go func(src graph.LogSource, dest, seeded string) {
			defer wg.Done()
			var err error
			if strings.TrimSpace(src.Run) != "" {
				command, renderErr := graph.Render(src.Run, scope, true)
				if renderErr != nil {
					err = renderErr
				} else {
					err = logs.FollowCommand(ctx, in.GraphDir, command, os.Environ(), dest)
				}
			} else if seeded != "" {
				err = logs.FollowPath(ctx, seeded, dest, 0)
			}
			if err != nil && ctx.Err() == nil {
				_ = appendNote(dest, err.Error())
			}
		}(src, dest, seeded)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			wg.Wait()
		})
	}
	return caps, stop
}

func (a *Activities) tailLines(src graph.LogSource) int {
	if src.TailLines > 0 {
		if src.TailLines > config.MaxTailLines {
			return config.MaxTailLines
		}
		return src.TailLines
	}
	if a.Config.Logs.TailLines > 0 {
		return a.Config.Logs.TailLines
	}
	return config.DefaultTailLines
}

func (a *Activities) logSection(caps []logCapture) string {
	if len(caps) == 0 {
		return ""
	}
	secrets := a.secretValues()
	var b strings.Builder
	for _, cap := range caps {
		if cap.Path == "" {
			continue
		}
		text, err := logs.LastLines(cap.Path, cap.TailLines, 0)
		if err != nil {
			text = err.Error()
		}
		text = redact(text, secrets)
		fmt.Fprintf(&b, "### %s\nfile: %s\n%s\n", cap.Name, cap.Path, text)
	}
	return b.String()
}

func setLogMessage(messages []model.Message, section string) []model.Message {
	filtered := make([]model.Message, 0, len(messages)+1)
	for _, msg := range messages {
		if msg.Role == "user" && strings.HasPrefix(msg.Content, logMessagePrefix) {
			continue
		}
		filtered = append(filtered, msg)
	}
	if strings.TrimSpace(section) == "" {
		return filtered
	}
	return append(filtered, model.Message{Role: "user", Content: logMessagePrefix + section})
}

func attachLogs(res run.Result, caps []logCapture) run.Result {
	for _, cap := range caps {
		if cap.Path != "" {
			res.Artifacts = append(res.Artifacts, cap.Path)
		}
	}
	if res.Artifacts == nil {
		res.Artifacts = []string{}
	}
	return res
}

func appendNote(path, note string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "shakedown: %s\n", note)
	return err
}
