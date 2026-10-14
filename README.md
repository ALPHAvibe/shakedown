# Shakedown

Shakedown is a local agent loop. A run is a YAML graph of executions. Temporal walks the graph, runs scripts directly, and calls a model only when an execution carries a prompt. After each step a supervisor picks the next edge or stops. The run always writes a Markdown report.

The model is configuration. Point it at any OpenAI-compatible server: Ollama, llama.cpp, vLLM, LM Studio, OpenAI, OpenRouter, or another gateway. The worker model and the supervisor model can be different profiles.

## Run

Start Temporal, then a worker, then a graph:

```sh
temporal server start-dev
go run ./cmd/shakedown worker --base-url http://127.0.0.1:11434/v1 --model qwen2.5-coder
go run ./cmd/shakedown run examples/hello/graph.yaml --input name=ada
```

`run` prints the Temporal UI link and waits for the report. `--detach` returns after the workflow starts. Prompt executions need a model; script-only graphs do not. `examples/hello` is script-only. `examples/ctl-api-migrations` is the sandbox migration graph from the RFC and calls a model for `api-explore` and for failed migrations.

```sh
go run ./cmd/shakedown check examples/ctl-api-migrations/graph.yaml
shakedown config
```

`shakedown config` prints the config the process would use, with API keys omitted.

## Configuration

Copy `config.example.yaml` to `shakedown.yaml`. The file is read from `./shakedown.yaml`, or from `--config`, or from `SHAKEDOWN_CONFIG`. A missing file uses the defaults below. Flags on `run` are stored with that run. Flags on `worker` change the worker process.

```yaml
temporal:
  address: 127.0.0.1:7233   # SHAKEDOWN_TEMPORAL_ADDRESS
  namespace: default        # SHAKEDOWN_TEMPORAL_NAMESPACE
  task_queue: shakedown     # SHAKEDOWN_TASK_QUEUE
  ui: http://localhost:8233 # SHAKEDOWN_TEMPORAL_UI
  run_timeout: 24h

models:
  local:
    base_url: http://127.0.0.1:11434/v1
    model: qwen2.5-coder
    api_key_env: SHAKEDOWN_API_KEY
    temperature: 0.2
    max_tokens: 4096
    timeout: 10m
    json_mode: prompt
    tool_protocol: auto
    max_tool_turns: 8
    decision_style: json
    token_limit_field: max_tokens
    # extra_body: {guided_json: true}
    # extra_headers: {X-Title: shakedown}

roles:
  worker: local
  supervisor: local

supervisor:
  chain: [rules, model]       # rules, model, jev
  same_execution_failures: 2
  area_failures: 3
  max_steps: 100
  min_confidence: 0
  jev:
    enabled: false
    base_url: ""
    api_key_env: JEV_API_KEY
    decide_path: /decide
    min_confidence: 0.6
    timeout: 30s

runner:
  kind: local                 # local or docker
  docker_image: alpine:3.20   # required when kind is docker
  work_dir: ""                # empty uses the graph directory
  secret_env: []              # env var names passed to scripts, never to prompts

report:
  dir: reports
  sinks: []

artifacts_dir: artifacts

logs:
  tail_lines: 200             # lines of each stream included in a model prompt
```

### Models

`roles.worker` drives prompt executions. `roles.supervisor` breaks ties the rules do not cover. They can name the same profile or two different ones.

`json_mode` is `prompt` (works everywhere), `json_object`, `json_schema`, or `grammar` (llama.cpp). `tool_protocol` is `auto`, `openai`, or `text`. `auto` sends OpenAI tool calls and also accepts `{"tool":"name","args":["..."]}` from models that ignore the tools field. `extra_body` is merged into the chat request for server-specific fields such as vLLM `guided_json`. `token_limit_field` is `max_tokens` or `max_completion_tokens`.

`decision_style: yesno` asks the supervisor model a one-token yes or no per edge and reads logprobs when the server returns them.

These environment variables override the selected profiles: `SHAKEDOWN_BASE_URL`, `SHAKEDOWN_MODEL`, `SHAKEDOWN_API_KEY`, `SHAKEDOWN_API_KEY_ENV`, `SHAKEDOWN_JSON_MODE`, `SHAKEDOWN_WORKER_MODEL`, `SHAKEDOWN_SUPERVISOR_MODEL`, `SHAKEDOWN_WORKER_BASE_URL`, `SHAKEDOWN_SUPERVISOR_BASE_URL`. The same switches exist as flags: `--base-url`, `--model`, `--api-key-env`, `--json-mode`, `--tool-protocol`, `--decision-style`, `--worker-model`, `--supervisor-model`, `--profile`.

Put the API key in the named environment variable. Workflow history stores the variable name, not the key.

### Supervisor

The chain is `rules`, then `model`, then optional `jev`. The first confident answer wins. A required execution that fails stops before the supervisor. Rules then stop when the same execution has failed `same_execution_failures` times, when one `area` has failed `area_failures` times, and when a single `when` edge matches. Two matching conditional edges are a tie, and that is what the model is for. Set `supervisor.chain: [rules]` to stay fully offline. `max_steps` stops a cycle.

Jev is off unless `supervisor.jev.enabled` is true and `base_url` is set. The adapter POSTs `{execution_id, outputs, history, edges}` to `decide_path` and expects `{decision, edge, reason, confidence}`.

### Runner, reports, artifacts

`runner.kind: docker` starts one container per run, execs graph commands inside it, and removes it during teardown. Log commands still run on the host, because that is where `kubectl` and host log files are. `secret_env` lists variable names. Those values are passed into scripts and redacted from prompts and from the log text shown to a model.

`report.dir` receives `<run-id>.md` and `<run-id>.json`. Sinks are optional:

```yaml
sinks:
  - kind: slack
    webhook_env: SLACK_WEBHOOK_URL
  - kind: github
    token_env: GITHUB_TOKEN
    repo: org/repo
    pr_input: pr
```

`artifacts_dir` holds command output and captured log streams.

## Graphs

Each execution has `run` (a command or a list) or `prompt` (a file the model follows). `tools` is the allowlist the model may execute. `on_fail` is a prompt used only when `run` fails. `next` edges are ordered and may carry `when` expressions such as `status == "pass"`. `required: true` stops the run on failure. `timeout` is a Go duration (`20m`). `area` groups executions for the failure rule.

Substitutions use `{{ inputs.name }}`. In commands they are shell-quoted. In prompt text and log paths they are inserted as plain text.

A graph-level `teardown` hook always runs, including after stop or failure.

### Log streams

An execution can name streams to copy while it runs. `run` is a host command. `path` is a file, absolute or relative to the graph directory. Each source needs one of those, not both.

```yaml
executions:
  - id: run-migrations
    run: scripts/migrate.sh {{ inputs.ref }}
    logs:
      - name: database
        run: kubectl logs -f deployment/db
      - name: migration
        path: /var/log/migration.log
        tail_lines: 50
```

Shakedown creates the artifact files immediately and appends to them for the life of the execution:

```text
artifacts/<run-id>/run-migrations/database.log
artifacts/<run-id>/run-migrations/migration.log
```

Tail those paths with `tail -f` or point a Cursor agent at them. For a file that already exists, the artifact starts with the last `tail_lines` and then follows new bytes. A command stream is the command's own output. `logs.tail_lines` in `shakedown.yaml` is the default number of lines placed in a model prompt (200). A source can override it with `tail_lines`. The prompt never receives the whole stream. The artifact file keeps the capture for that execution.

## Other commands

```sh
shakedown worker
shakedown status <workflow-id>
shakedown stop <workflow-id>
shakedown signal <workflow-id> skip --edge <id>
shakedown signal <workflow-id> rerun --execution <id>
shakedown schedule sync schedules.yaml
shakedown replay reports/<id>.json
shakedown config
```

`schedule sync` creates or updates one Temporal Schedule per entry. Schedules and manual runs start the same workflow. `replay` runs the current supervisor chain over a saved report so rules, a local model, and Jev can be compared on the same history. `status` shows the current node, results, and decisions. Live script and log output is in the artifact files, not in that status payload.

Scripts should be safe to retry. Temporal retries an activity when the worker crashes; a script that exits non-zero is a result, not a retry. A retry rewrites that execution's log artifacts.
