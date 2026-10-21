-- Run in the fetch directory: duckdb < hack/observations/e1.sql
-- Inputs: obs/<hour>/*.jsonl.gz (fetch.sh) and loadgen/<stage>.jsonl.
CREATE MACRO empirical(xs, p) AS list_sort(xs)[greatest(1, ceil(p * len(xs)))::INT]; -- gonum stat.Empirical
CREATE VIEW r AS SELECT * FROM read_json_auto('obs/*/requests.jsonl.gz', union_by_name = true) WHERE kind = 'final';
CREATE VIEW c AS SELECT *, regexp_extract(filename, '([^/]+)\.jsonl$', 1) AS stage
  FROM read_json_auto('loadgen/*.jsonl', filename = true);

-- Request table: sent - recorded is the number of requests without a server row.
WITH j AS (
  SELECT c.stage, r.outcome, r.elapsedNanos AS e2e, r.executionComplete AS done,
    r.elapsedNanos - r.dispatchedNanos - r.nativeElapsedMS * 1e6 AS residual,
    -- Outside the firstfloor actor's admit-to-finish span; queueMS - (running - queued) is the ingress read.
    r.elapsedNanos / 1e6 - r.nativeElapsedMS + r.queueMS - (r.runningNanos - r.queuedNanos) / 1e6 AS margin,
    r.outcome NOT IN ('delivery_failed', 'incomplete', 'canceled') AND r.elapsedNanos > 0 AS sample
  FROM c LEFT JOIN r ON c.requestID = r.requestID)
SELECT stage, count(*) AS sent, count(outcome) AS recorded,
  empirical(list(e2e) FILTER (WHERE sample), 0.95) / 1e6 AS p95_ms,
  count(*) FILTER (WHERE outcome = 'completed' AND e2e < 1e9) AS within_1s,
  empirical(list(margin) FILTER (WHERE outcome = 'completed' AND margin IS NOT NULL), 0.95) AS margin_p95_ms,
  empirical(list(residual) FILTER (WHERE sample AND done), 0.50) / 1e6 AS residual_p50_ms,
  empirical(list(residual) FILTER (WHERE sample AND done), 0.99) / 1e6 AS residual_p99_ms,
  max(residual) FILTER (WHERE sample AND done) / 1e6 AS residual_max_ms
FROM j GROUP BY stage ORDER BY stage;

-- Saturation table: run a stage that keeps one instance's firstfloor queue deep at one batch size.
-- ms_per_request = span / executed requests; batch IDs count executions the rows miss.
-- Dispatch occupancy = ms_per_request - mean profile service at that batch size.
WITH s AS (
  SELECT c.stage, r.instance, r.batchSize, r.batchID,
    epoch(CAST(r.receivedAt AS TIMESTAMPTZ)) + r.runningNanos / 1e9 AS started
  FROM c JOIN r ON c.requestID = r.requestID
  WHERE r.outcome = 'completed' AND r.runningNanos IS NOT NULL)
SELECT stage, instance, min(batchSize) AS batch, max(batchSize) AS max_batch,
  (max(batchID) - min(batchID)) * min(batchSize) AS requests, max(started) - min(started) AS seconds,
  1e3 * (max(started) - min(started)) / ((max(batchID) - min(batchID)) * min(batchSize)) AS ms_per_request
FROM s GROUP BY stage, instance ORDER BY stage, instance;

-- Tick table: scheduler evaluation time per decision.
WITH d AS (
  SELECT decision.evaluationElapsedNanos / 1e6 AS ms
  FROM read_json_auto('obs/*/scheduler.jsonl.gz', union_by_name = true, maximum_object_size = 67108864)
  WHERE decision IS NOT NULL)
SELECT count(*) AS ticks, empirical(list(ms), 0.50) AS p50_ms, empirical(list(ms), 0.95) AS p95_ms, max(ms) AS max_ms FROM d;
