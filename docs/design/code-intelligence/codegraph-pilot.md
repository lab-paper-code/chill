# CodeGraph Phase 3 Pilot Record

## Status

Pilot in progress: session 1 of 3 completed on 2026-07-16. CodeGraph is not an
adopted correctness dependency, is not configured automatically as an MCP, and
does not run in CI.

The first session found enough discovery benefit to continue the pilot, but it
also reproduced two material blind spots: `IntentDigest` impact resolves the
same-named annotation constant instead of the API field, and Helm template
conditions do not yield the rendered Profiler resource set. The affected-test
feature remains unapproved.

## Pinned distribution and isolation

| Item | Value |
|---|---|
| CodeGraph | v1.4.1 |
| Git tag commit | `ecc8b307ac2f8a7d06bff02ee513c4ea2380b2f8` |
| Release asset | `codegraph-linux-x64.tar.gz` |
| Asset SHA-256 | `fb585ff5018d6faaa46d282b61f4f689bc7967ed8a1b467a5c556dd7ced9b542` |
| Supported pilot host | Linux x86_64 |
| Local runtime location | ignored `bin/codegraph-v1.4.1/` |
| Project index | ignored `.codegraph/` |
| Runtime fallback download | disabled |
| Telemetry | disabled |
| Update check | disabled |
| CI use | none |

`hack/codegraph-chill.sh` fixes `DO_NOT_TRACK=1`,
`CODEGRAPH_TELEMETRY=0`, `CODEGRAPH_NO_UPDATE_CHECK=1`, and
`CODEGRAPH_NO_DOWNLOAD=1` for every invocation. The installer verifies the
published release digest before extraction. No command in the pilot writes
Codex, editor, or agent configuration.

## Session 1 environment and index

The session used commit `d0786e5644bad214c5a89762c17bbd1b828c7c89` plus the
current uncommitted Phase 0 through Phase 3 files.

| Measure | Session 1 |
|---|---:|
| Files indexed | 279 |
| Graph nodes | 3,266 |
| Graph edges | 8,138 |
| Index database | 9.87 MB |
| CodeGraph-reported index time | 1.2 s |
| Full `make codegraph-index` wall time | 2.36 s |
| Maximum resident memory during indexing | 341,864 KiB |
| Pending changes after index | 0 |
| Database journal | WAL |

The ignored extracted runtime occupies about 226 MiB. It is a local tool cost,
not a repository or image size change.

## Golden-query session 1

CodeGraph results are candidate discovery evidence. The authority column is
always executed before an answer is accepted.

| Query | CodeGraph result | Required authority | Session 1 verdict |
|---|---|---|---|
| Q1 ProfilingRun reconciliation | Exact `IntentReconciler` query returns `intent_reconciler.go` and `intent_setup.go`; callers of `NewIntentReconciler` return `cmd/profiler/controller_setup.go`. Broad natural-language exploration ranks unrelated reconcilers. | setup source and focused tests; event edge labeled `framework-derived` | useful with exact symbols; fewer discovery operations |
| Q2 Run intent freezer implementation | Finds the interface and production `IntentFreezer`; targeted exploration synthesizes the production interface dispatch but omits the recording fake. | `gopls implementation` and composition root | partial; `gopls` remains mandatory |
| Q3 Evidence Store adapter | Finds S3-to-core imports, but broad search includes Operator and spike noise. | full production import comparison with `go list` | useful after scope filtering |
| Q4 PowerObserver adapter | Finds adapter-to-core and current Profiler consumers, with spike and test noise. | full production import comparison with `go list` and ADR | useful after scope filtering |
| Q5 `IntentDigest` impact | Resolves `intentDigestAnnotation`; misses the API field, projection, and integration test. `affected` returned no tests for `profilingrun_types.go`. | `gopls references`, source, generated CRD checks | known miss; impact and affected are unapproved |
| Q6 Profiler Helm render | Returns unrelated Go symbols and no reliable conditional template/resource set. | two `helm template` renders and resource-set comparison | known miss; no discovery benefit |

The automated production-import comparison matched all 67 `go list` edges.
The first session reduced discovery operations for Q1, Q3, and Q4, satisfying
the provisional three-of-six benefit threshold for this session only. It does
not satisfy the three-session adoption gate.

## Freshness result

Explicit `codegraph sync` correctly reflected:

- creation of an untracked Go file;
- modification that removed one symbol and added another;
- rename with the symbol rebound to the new file path; and
- deletion with the symbol removed from search.

The final status was clean with zero pending changes. Watcher debounce is not
yet approved: it must be tested through a manually attached MCP server in
sessions 2 and 3 rather than inferred from explicit sync.

## Reproduce one session

```bash
make codegraph
make codegraph-index
make codegraph-status
make codegraph-pilot CODEGRAPH_PILOT_SESSION=1
make codegraph-freshness
```

Print, but do not install, the manual Codex MCP entry:

```bash
make codegraph-mcp-config
```

The output points Codex at the repository wrapper, so privacy and download
controls cannot be bypassed accidentally. Add it manually only for a pilot
session and restart the client. Ordinary `gopls`, `go list`, `rg`, source reads,
and Helm commands stay available.

## Remaining gate

Before optional adoption:

1. run the same script in two later, distinct coding sessions as sessions 2
   and 3;
2. exercise watcher debounce through the manually attached MCP server;
3. confirm the three-of-six discovery benefit in both later sessions;
4. confirm no high-impact false statement survives the mandatory source
   verification; and
5. keep impact and affected-test claims unapproved unless a separate test
   corpus closes the observed miss.

Rollback is local and lossless:

```bash
make codegraph-uninit
```

Remove the manually added MCP entry if one was added. The baseline development
process continues through `gopls`, `go list`, source search, and Helm rendering.
