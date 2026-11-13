# Internal Fast Image Deployment Loop

## Status

Accepted

## Context

Internal publication shortens testbed deployment feedback. Docker Hub remains the public distribution path. Deployment preserves approved state, source provenance and failure evidence.

## Decision

`hack/fast-deploy.sh` invokes `hack/deploy/main.py`. The [deployment runner guide](../../hack/README.md) owns recipe fields and executable procedures.

- Default and `--dry-run` produce a local plan without network access, builds, locks or persistent evidence writes. `--inspect` authorizes read-only inspection. Only `--execute` authorizes publication and cluster mutation.
- Require an explicit target, expected release revision and disk limits. Resolve deployment and tool sources to immutable Git commits and archive them. Commands use frozen sources and bounded argv, without shell interpolation.
- Select image roles explicitly from the existing role catalog. Verify the selected registry artifact and requested platform configurations, use digest-pinned images, and preserve originating build metadata. Unknown build origins rebuild conservatively.
- Execute bounded preflight, stage, quiesce, migrate, configure and accept phases. Runtime activation requires real declarations, probes and acceptance work. Generated stage values freeze before final review and quiesce.
- Preserve live user values plus approved source values with Helm `--reset-values`. Back up live values, manifests, hooks and retained resources privately. Require exact chart and staged-diff approval, recheck context and revision, and perform server-side dry-run before mutation.
- Helm owns CRDs. A departing CRD must already carry its keep annotation. The runner does not add ownership or retention annotations to conceal destructive changes.
- Failures stop with partial state and evidence. Recovery requires a new reviewed roll-forward deployment. Do not automatically retry, restore, roll back or reuse evidence directories.
- Operational identity, readiness, authorization and required phase completion gate deployment READY. Scientific coverage and measurement comparisons retain their factual results separately; READY does not certify a campaign, browser or hardware result.
- Execute shares the heavy-operation lock. Keep credentials out of recipes, argv, build arguments and evidence. Node pull authentication and trust remain explicit prerequisites.
- Enforce disk limits without pruning unrelated data. An explicit `pruneCompletedBuildCache` recipe option may prune the selected builder's cache after a completed build.

## Consequences

One reviewed recipe binds source, images, desired configuration and acceptance evidence. Merge and release verification remain separate gates. Implementing the runner grants no live deployment or image-publication authority.

