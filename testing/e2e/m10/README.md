# m10 — harness components live gate

Installs harness components into the test home, then proves, against live
Strata (headless):

1. **file-defined subagent works**: `subagents/summarizer.md` shows up as an
   Agent-tool type and answers in one line.
2. **guardrail rule is enforced**: a `deny:` rule in `guardrails.md` blocks
   the model's `rm -rf` even under `-yolo` (deny beats bypassPermissions).

```sh
make live-m10-test          # from the repo root (Strata must be up)
```

Manual equivalent:

```sh
cp -r testing/e2e/m10/harness /tmp/opencode/anneal-home/
cd /tmp/opencode/demo
ANNEAL_HOME=/tmp/opencode/anneal-home ANNEAL_ENDPOINT=http://127.0.0.1:8081/v1/chat/completions \
  <repo>/bin/anneal-headless -json -yolo -p 'Delegate with the Agent tool (subagent type "summarizer"): summarize what go.mod declares in one line.'
```

Gate assertions are made on the JSON event stream (`tool.start` names,
`tool.result` error flags), never on model prose.
