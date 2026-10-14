package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExampleAndEnvOverride(t *testing.T) {
	clearModelEnv(t)
	path := filepath.Join("..", "..", "config.example.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Roles.Worker != "local" {
		t.Fatalf("worker role = %s", cfg.Roles.Worker)
	}
	if cfg.WorkerProfile().Model != "qwen2.5-coder" {
		t.Fatalf("model = %s", cfg.WorkerProfile().Model)
	}
	t.Setenv("SHAKEDOWN_WORKER_MODEL", "worker-model")
	t.Setenv("SHAKEDOWN_SUPERVISOR_MODEL", "supervisor-model")
	t.Setenv("SHAKEDOWN_BASE_URL", "http://example.test/v1")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkerProfile().Model != "worker-model" {
		t.Fatalf("worker model = %s", cfg.WorkerProfile().Model)
	}
	if cfg.SupervisorProfile().Model != "supervisor-model" {
		t.Fatalf("supervisor model = %s", cfg.SupervisorProfile().Model)
	}
	if cfg.Roles.Worker == cfg.Roles.Supervisor {
		t.Fatal("distinct model overrides should split profiles")
	}
}

func TestMissingFileUsesDefaults(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("SHAKEDOWN_CONFIG", "")
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Temporal.TaskQueue != DefaultTaskQueue {
		t.Fatalf("queue = %s", cfg.Temporal.TaskQueue)
	}
	if cfg.Supervisor.Chain[0] != "rules" {
		t.Fatalf("chain = %v", cfg.Supervisor.Chain)
	}
	if cfg.Logs.TailLines != DefaultTailLines {
		t.Fatalf("tail lines = %d", cfg.Logs.TailLines)
	}
}

func clearModelEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"SHAKEDOWN_CONFIG", "SHAKEDOWN_BASE_URL", "SHAKEDOWN_MODEL", "SHAKEDOWN_API_KEY",
		"SHAKEDOWN_API_KEY_ENV", "SHAKEDOWN_JSON_MODE", "SHAKEDOWN_WORKER_MODEL",
		"SHAKEDOWN_SUPERVISOR_MODEL", "SHAKEDOWN_WORKER_BASE_URL", "SHAKEDOWN_SUPERVISOR_BASE_URL",
		"SHAKEDOWN_TEMPORAL_ADDRESS", "SHAKEDOWN_TASK_QUEUE",
	} {
		t.Setenv(key, "")
	}
}
