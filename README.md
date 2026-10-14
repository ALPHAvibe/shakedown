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
```

## Models

Copy `config.example.yaml` to `shakedown.yaml`. Profiles live under `models`, and `roles.worker` / `roles.supervisor` select which profile drives prompt executions and tie-breaking decisions.

Flags and environment variables override a profile without editing the file:

```sh
shakedown run graph.yaml --base-url http://127.0.0.1:8080/v1 --model my-model --json-mode json_schema
shakedown run graph.yaml --worker-model large-model --supervisor-model small-model
```

`SHAKEDOWN_BASE_URL`, `SHAKEDOWN_MODEL`, `SHAKEDOWN_WORKER_MODEL`, `SHAKEDOWN_SUPERVISOR_MODEL`, `SHAKEDOWN_JSON_MODE`, and `SHAKEDOWN_API_KEY_ENV` do the same. Put the API key in the named environment variable. The workflow history stores the variable name, not the key.

`json_mode` is `prompt` (works everywhere), `json_object`, `json_schema`, or `grammar` (llama.cpp). `tool_protocol` is `auto`, `openai`, or `text`. `auto` sends OpenAI tool calls and also accepts `{"tool":"name","args":["..."]}` from models that ignore the tools field. `extra_body` is merged into the request for server-specific fields such as vLLM `guided_json`.

`decision_style: yesno` asks the supervisor model a one-token yes or no per edge and reads logprobs when the server returns them.

The supervisor chain is `rules`, then `model`, then optional `jev`. The first confident answer wins. A required execution that fails stops before the supervisor. Rules then stop on repeated failures, on too many failures in one `area`, and on a single matching `when` edge. Two matching conditional edges are a tie, and that is what the model is for. Set `supervisor.chain: [rules]` to stay fully offline.

Jev is off unless `supervisor.jev.enabled` is true and `base_url` is set. The adapter POSTs `{execution_id, outputs, history, edges}` to `decide_path` and expects `{decision, edge, reason, confidence}`.

## Graphs

Each execution has `run` (a command or a list) or `prompt` (a file the model follows). `tools` is the allowlist the model may execute. `on_fail` is a prompt used only when `run` fails. `next` edges are ordered and may carry `when` expressions such as `status == "pass"`. `required: true` stops the run on failure. `timeout` is a Go duration (`20m`). `area` groups executions for the failure rule.

Substitutions use `{{ inputs.name }}`. In commands they are shell-quoted. Credentials listed in `runner.secret_env` are passed to scripts and stripped from prompts and tool output.

`runner.kind: docker` starts one container per run, execs commands inside it, and removes it during teardown. A graph-level `teardown` hook always runs, including after stop or failure.

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

`schedule sync` creates or updates one Temporal Schedule per entry. Schedules and manual runs start the same workflow. `replay` runs the current supervisor chain over a saved report so rules, a local model, and Jev can be compared on the same history.

Reports are Markdown plus JSON under `report.dir`. Optional sinks are a Slack webhook and a GitHub pull request comment.

Scripts should be safe to retry. Temporal retries an activity when the worker crashes; a script that exits non-zero is a result, not a retry.
