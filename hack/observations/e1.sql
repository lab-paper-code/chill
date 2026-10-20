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
    r.elapsedNanos / 1e6 - (r.queueMS + r.serviceMS) AS margin, -- outside firstfloor queue + execution
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

-- Tick table: scheduler evaluation time per decision.
WITH d AS (
  SELECT decision.evaluationElapsedNanos / 1e6 AS ms
  FROM read_json_auto('obs/*/scheduler.jsonl.gz', union_by_name = true, maximum_object_size = 67108864)
  WHERE decision IS NOT NULL)
SELECT count(*) AS ticks, empirical(list(ms), 0.50) AS p50_ms, empirical(list(ms), 0.95) AS p95_ms, max(ms) AS max_ms FROM d;
