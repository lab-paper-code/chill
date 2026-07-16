# Optional Local CodeGraph

## Decision

CodeGraph v1.4.1 is allowed only as a local discovery accelerator. It is not a
correctness dependency, CI gate, automatic agent configuration, or contributor
requirement.

Repeated checks found useful discovery for focused entry points and package
boundaries, but incomplete results for interface dispatch, API-field impact,
affected tests, and Helm topology. Mandatory verification therefore remains
outside CodeGraph.

| CodeGraph output | Required authority |
|---|---|
| Candidate symbols, callers, and source snippets | direct source |
| Interface implementations | `gopls implementation` and the composition root |
| Package edges | `go list`; `.go-arch-lint.yml` for allowed direction |
| Controller event path | setup registration and focused tests; label `framework-derived` |
| Impact or affected tests | source, `gopls` references, generated artifacts, and tests |
| Deployment resources | validated `helm template` output |

## Local controls

- Version: v1.4.1, installed under ignored `bin/`.
- Release SHA-256 is pinned and verified by `hack/codegraph-install.sh`.
- Index: ignored `.codegraph/`; never commit or upload it.
- `DO_NOT_TRACK=1`, telemetry off, update checks off, and runtime fallback
  downloads off are fixed by `hack/codegraph-chill.sh`.
- Repository, editor, Codex, and agent configuration are never edited
  automatically.

## Commands

```bash
make codegraph
make codegraph-index
make codegraph-status
make codegraph-check
make codegraph-watcher
```

Print the optional Codex MCP entry without installing it:

```bash
make codegraph-mcp-config
```

Before relying on a result, ensure `make codegraph-status` reports a complete,
clean index. Keep `gopls`, `go list`, source search, tests, and Helm rendering
available in the same workflow.

## Revalidation and rollback

Rerun `make codegraph-check` and `make codegraph-watcher` after a CodeGraph
version change or suspected freshness regression. Impact and affected-test
features remain unapproved unless a separate corpus closes the known misses.

Rollback is local and does not remove any baseline capability:

```bash
make codegraph-uninit
```

Also remove the manually added MCP entry, if present.
