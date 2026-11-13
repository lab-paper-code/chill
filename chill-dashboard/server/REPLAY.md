# Replay server

Serves the dashboard against a captured ChillServer, without a cluster. Local development only.

    node --experimental-strip-types server/replay.mjs --capture <dir> [--port 3600] [--scene captured|active] [--no-next]

One process starts a fake ChillServer on `port+1` (HTTPS, loopback), a browser-facing HTTPS server on `port` (the live proxy from `live-proxy.mjs` plus a raw pipe for Next HMR upgrades), and `next dev` on `port+2` wired to the fake upstream. Open `https://localhost:<port>/` and accept the self-signed certificate. `--no-next` starts only the two servers.

## Scenes and tokens

Each request picks its scene from the Bearer token, so many clients can share one server: token `active` shows the active scene, token `captured` the captured scene, any other non-empty token the default (`--scene`). This applies to REST and to the live WebSocket. Mutation state (settings, catalog lists) is kept per scene.

- `captured`: the capture as recorded (an idle cluster), time-shifted to now.
- `active`: a synthetic overlay on the captured names and UIDs. Three services serve (image-classification on genesis2 and jetsono, plc-anomaly on jetson-nano, smolvlm2-vqa on genesis3), a prepare action on orin-nano repeats every 2 minutes, lattepanda holds a measurement lease, one evaluation runs. Metrics carry a per-service `slo` window (by finish time); plc-anomaly moves in and out of its target, and for 8 s after each snapshot smolvlm2-vqa's `slo` omits `quantileMs` and `met` (window not yet covered). REST overlays: workloads, pools, scheduler state summary and settings, observations (workloads, load, events, scheduler `decision-change` and `settings`), active evaluations.
- `active` measurement evidence (`replay-measure.mjs`, plain JSON that `parseMeasurementJSON` accepts): coverage turns 20 cells of the three serving models usable (batches 1, 2, 4, 8; the four serving cells marked scheduler-used and Compatible), 3 running, 2 insufficient, the rest as captured, with `summary` and `nextActions` recomputed. Scheduler profiles list those 22 profiles (filters, paging, `profile=<id>`); instances in the live snapshot and scheduler state reference the same profile IDs. Requests `coverage-694507bcc68d-06b30e6db9c5` and `coverage-4de3c8086996-8277c899db90` are complete with `/result`, child `profiling-runs` (`parentUID=`) and `evidence-passport`; `coverage-69d06d5c6f97-6de8b857ed79` is measuring (a trial every 45 s); the captured failed request stays. Dispatch occupancy gains one complete short-burst item and one measuring item.

`filter=decision-change` returns decisions whose target exists and is not the keep plan. Active: one changed decision every 2 minutes (summary rows without target, compact/full rows with `keep` and `target`). Captured: the captured `filter=decision` rows filtered by that predicate, with targets looked up from the captured compact decision reads (empty for this capture).

`GET /__replay/status` (browser port) reports the default scene, live connections per scene, settings ETags and the last 200 mutations. `POST /__replay/scene?name=captured|active` changes the default and closes live sockets that use it (1013).

## GET matching

Exact `path?query` first; else the same path scored on the non-volatile query keys (`from,to,at,cursor,limit,offset,order,page,sort` are ignored); else 404 `{code:"ReplayMissing"}`. Captured status codes and ETags are kept. Every RFC3339 timestamp in bodies and live frames is shifted by now minus `capturedAt` (whole seconds; `0001-01-01T00:00:00Z` untouched). Observation reads with `from` and `to` echo the requested window; events are filtered to it and never page.

## Live

`/api/v1/live` speaks chill-live/1: hello, then a snapshot after each resume, then frames with a contiguous seq and a heartbeat every 5 s. `captured` replays the recorded frames with their spacing, looping from the first metrics frame. `active` sends metrics every second, decision deltas and events every 20 s, and action start/complete events.

## Mutations

- `PUT v1/scheduler/settings`: `If-Match` required (428) and must equal the current ETag (409); stores the merged settings, bumps the ETag, returns a settings change.
- `POST v1/workloads/{name}/infer`: canned 200 after 400 ms with `X-Chill-Request-ID`.
- `v1/{workloads,models,preprocessors,datasets}`: POST creates (201), PUT updates with `If-Match` = resourceVersion (200), DELETE removes (204, 202 for workloads); later GETs reflect it.
- POST with `?dryRun=All` or a `/preflight` path: 200 with a minimal body. Any other mutation: 202 `{name, uid}`, no change.

## Capture format

A capture directory holds `_index.json` = `{meta:{capturedAt,from1h,from15m,namespace}, index:{"<path?query>":{file,status,etag}}}` with paths relative to `/api/` (for example `v1/pools`), one body file per GET, and `_live.json` = an array of chill-live/1 frames starting with hello and snapshot. Refresh it by capturing a cluster with read-only GETs and a live recording into a new directory of this shape.
