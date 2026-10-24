"""Offline derivation of requested CSV, summary and report from preserved capture JSONL."""
import collections
import csv
import json
import os
import argparse
from pathlib import Path
import statistics

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
INPUT = Path(ENV["cadenceInput"])
ROOT = Path(ENV["cadenceOutput"])
CODE = Path(__file__).resolve().parent
NODES = list(ENV["cadenceNodes"])


def distribution(values):
    values = sorted(values)
    if not values:
        return {"count": 0, "min": None, "p05": None, "p50": None, "p95": None, "p99": None, "max": None, "mean": None}
    def quantile(p):
        x = (len(values) - 1) * p
        i = int(x)
        return values[i] + (values[min(i+1, len(values)-1)] - values[i]) * (x-i)
    return {"count": len(values), "min": values[0], "p05": quantile(.05), "p50": quantile(.5),
            "p95": quantile(.95), "p99": quantile(.99), "max": values[-1], "mean": statistics.fmean(values)}


def delta_seconds(rows, key):
    return [(b[key]-a[key])/1e9 for a,b in zip(rows, rows[1:])]


def analyze(node):
    raw = INPUT / f"{node}.raw.jsonl"
    rows = [json.loads(line) for line in raw.read_text().splitlines()]
    attempted = [r for r in rows if not r["skipped"]]
    valid = [r for r in attempted if r["power_w"] is not None and r["error"] is None and r["parser_error"] is None]
    fields = ["node", "slot", "planned_start_monotonic_ns", "planned_start_wall_unix_ns", "actual_start_monotonic_ns", "actual_start_wall_unix_ns", "actual_start_utc", "actual_end_monotonic_ns", "actual_end_wall_unix_ns", "actual_end_utc", "duration_ns", "start_jitter_ns", "power_w", "power_text", "power_unit", "status", "error", "parser_error", "skipped", "skip_reason"]
    with (ROOT / f"{node}.csv").open("w", newline="") as out:
        writer = csv.DictWriter(out, fieldnames=fields+["raw_jsonl_line"], extrasaction="ignore")
        writer.writeheader()
        for line_number, row in enumerate(rows, 1):
            writer.writerow(dict(row, raw_jsonl_line=line_number))
    runs = []
    change_rows = []
    prior = None
    for r in valid:
        # Missing/error slots break identical runs so no unobserved interval is claimed identical.
        contiguous = prior is not None and r["slot"] == prior["slot"] + 1
        if prior is None or not contiguous or r["power_w"] != prior["power_w"]:
            if prior is not None and r["power_w"] != prior["power_w"]:
                change_rows.append(r)
            runs.append({"power_w": r["power_w"], "start_slot": r["slot"], "end_slot": r["slot"],
                         "count": 1, "first_receipt_monotonic_ns": r["actual_end_monotonic_ns"],
                         "last_receipt_monotonic_ns": r["actual_end_monotonic_ns"]})
        else:
            runs[-1]["count"] += 1
            runs[-1]["end_slot"] = r["slot"]
            runs[-1]["last_receipt_monotonic_ns"] = r["actual_end_monotonic_ns"]
        prior = r
    for run in runs:
        run["observed_unchanged_dwell_seconds"] = (run["last_receipt_monotonic_ns"]-run["first_receipt_monotonic_ns"])/1e9
        run["edge_censored"] = run["start_slot"] == 0 or run["end_slot"] == 1199
        run["gap_censored"] = (run["start_slot"] > 0 and rows[run["start_slot"]-1]["power_w"] is None) or (run["end_slot"] < len(rows)-1 and rows[run["end_slot"]+1]["power_w"] is None)
    inter_changes = distribution(delta_seconds(change_rows, "actual_end_monotonic_ns"))
    dwell = distribution([r["observed_unchanged_dwell_seconds"] for r in runs])
    latency = distribution([r["duration_ns"]/1e9 for r in attempted])
    jitter = distribution([r["start_jitter_ns"]/1e9 for r in attempted])
    completion_gaps = distribution(delta_seconds(valid, "actual_end_monotonic_ns"))
    low_changes = len(change_rows) < 20
    base = max(inter_changes["max"] or 0, dwell["max"] or 0, completion_gaps["max"] or 0)
    uncertainty = (latency["max"] or 0) + (completion_gaps["max"] or 0)
    planning = 1.25 * (base + uncertainty)
    first = attempted[0] if attempted else None
    last = attempted[-1] if attempted else None
    span = (last["actual_end_monotonic_ns"]-first["actual_start_monotonic_ns"])/1e9 if attempted else None
    start_span = (last["actual_start_monotonic_ns"]-first["actual_start_monotonic_ns"])/1e9 if len(attempted)>1 else None
    summary = {
        "node": node, "family": ENV["cadenceFamilies"][node],
        "scheduled": len(rows), "attempted": len(attempted), "missed_slots": sum(r["skipped"] for r in rows), "valid_power_samples": len(valid),
        "http_status_counts": dict(collections.Counter(str(r["status"]) for r in attempted)),
        "request_errors": sum(r["error"] is not None for r in attempted), "parser_errors": sum(r["parser_error"] is not None for r in attempted),
        "first_actual_start_utc": first["actual_start_utc"] if first else None,
        "last_actual_end_utc": last["actual_end_utc"] if last else None,
        "actual_first_start_to_last_completion_seconds": span,
        "attempts_per_scheduled_120s_hz": len(attempted)/120,
        "actual_start_interarrival_rate_hz": (len(attempted)-1)/start_span if start_span else None,
        "successful_samples_per_scheduled_120s_hz": len(valid)/120,
        "start_jitter_seconds": jitter, "request_latency_seconds": latency,
        "actual_start_interarrival_seconds": distribution(delta_seconds(attempted, "actual_start_monotonic_ns")),
        "successful_completion_interarrival_seconds": completion_gaps,
        "power_watts": distribution([r["power_w"] for r in valid]),
        "distinct_power_values": len(set(r["power_w"] for r in valid)),
        "change_count": len(change_rows), "inter_change_seconds": inter_changes,
        "change_count_note": "Differences between successive valid observations, including across skipped slots; intervening unobserved changes cannot be counted.",
        "change_observations_adjacent_to_gap": sum(1 for a,b in zip(valid,valid[1:]) if b["slot"] != a["slot"]+1 and b["power_w"] != a["power_w"]),
        "edge_censored_run_count": sum(r["edge_censored"] for r in runs),
        "gap_censored_run_count": sum(r["gap_censored"] for r in runs),
        "identical_run_lengths_samples": distribution([r["count"] for r in runs]),
        "observed_unchanged_dwell_seconds": dwell, "identical_runs": runs,
        "estimated_output_update_cadence_seconds": inter_changes["p50"],
        "cadence_label": "실측 추정 — visible apower value-change interval, not ADC/acquisition guarantee",
        "low_change_count_threshold": 20, "low_change_count": low_changes,
        "planning_bound_seconds": planning, "planning_coefficient": 1.25,
        "planning_base_seconds": base, "planning_poll_transport_allowance_seconds": uncertainty,
        "planning_formula": "1.25 * (max(max inter-change interval, max observed unchanged dwell, max valid receipt gap) + max request duration + max valid receipt gap)",
        "planning_status": "declared conservative condition for calculations NOW; not a formal physical upper bound",
        "low_change_rule": "If fewer than 20 changes, unchanged dwell explicitly dominates evidence; median inter-change (if available) remains descriptive, not identified refresh.",
        "raw_jsonl": str(raw), "csv": str(ROOT / f"{node}.csv")}
    return summary


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input-dir", type=Path, default=INPUT)
    parser.add_argument("--output-dir", type=Path, default=ROOT)
    args = parser.parse_args()
    INPUT, ROOT = args.input_dir.resolve(), args.output_dir.resolve()
    repository = Path(CONFIG["paths"]["repository"]).resolve()
    if repository == ROOT or repository in ROOT.parents:
        parser.error("output must remain outside the repository source tree")
    ROOT.mkdir(parents=True, exist_ok=True)
    summary = {"user_decision_date_label": "2026-10-24", "actual_timestamp_note": "Actual wall-clock timestamps retained verbatim; observation UTC may be previous day. No clock adjusted.", "nodes": {n: analyze(n) for n in NODES}}
    before = json.loads((INPUT / "health-before.json").read_text())
    after = json.loads((INPUT / "health-after.json").read_text())
    summary["minimal_health"] = {"before_status": before["status"], "after_status": after["status"],
                                 "before_error": before["error"], "after_error": after["error"],
                                 "registry_body_unchanged": before["raw_reply"] == after["raw_reply"]}
    summary["transfer"] = {"provisional_only": True, "measured_nodes": NODES,
        "unmeasured_current_nodes": ENV["cadenceUnmeasuredNodes"],
        "rule": "Use genesis2 Gen3 planning bound provisionally for six unmeasured Gen3-family nodes; this is an explicit family-transfer assumption, not measurement or firmware-equivalence proof. jetson-nano Plus estimate is local, not transferred to Gen3."}
    (ROOT / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False) + "\n")
    lines = ["APPROVED READ-ONLY 10 HZ / 120 S TWO-FAMILY METER OBSERVATION", "User-decision date label: 2026-10-24. Actual UTC wall times below are observed host clock; not rewritten to label date.", "Authorization: CampaignOps #034, sent 2026-10-23T18:53:01.224Z (Main).", "", "METHOD / SCOPE", f"Existing gateway only: {ENV['meterURL']}/prometheus?hostname=<Node>. One in-flight request per Node; two Node threads; 1200 planned 0.1s starts each; late slots skipped, never concurrent catch-up. 120s monotonic deadline; final request timeout clipped to remaining time. Minimal registry GET /devices before/after only. No new route, RPC, induced load, restart, settings change, deployment or measurement CR. Existing consumers were not suppressed.", "Raw JSONL retains every scheduled slot, projected planned wall and real monotonic deadline, independently observed actual monotonic/wall start/end, latency, status/error, exact power numeric text/unit, headers and full raw HTTP reply. Missed slots have no fabricated actual times. CSV has one row per slot plus raw JSONL line reference. Planned wall values are explicitly anchor projections, not observed wall timestamps. Host time is not sensor acquisition time.", "", "OBSERVATIONS"]
    for node, s in summary["nodes"].items():
        lines += [f"{node} — {s['family']}",
                  f"  Actual UTC {s['first_actual_start_utc']} .. {s['last_actual_end_utc']}; start-to-last-completion={s['actual_first_start_to_last_completion_seconds']:.6f}s.",
                  f"  Scheduled={s['scheduled']}, attempted={s['attempted']}, valid={s['valid_power_samples']}, missed={s['missed_slots']}, request errors={s['request_errors']}, parser errors={s['parser_errors']}, HTTP={s['http_status_counts']}.",
                  f"  Actual start-interarrival rate={s['actual_start_interarrival_rate_hz']:.6f} Hz; attempts/120s={s['attempts_per_scheduled_120s_hz']:.6f} Hz.",
                  f"  Start jitter seconds={json.dumps(s['start_jitter_seconds'])}",
                  f"  Request latency seconds={json.dumps(s['request_latency_seconds'])}",
                  f"  Valid receipt gaps seconds={json.dumps(s['successful_completion_interarrival_seconds'])}",
                  f"  Observed W={json.dumps(s['power_watts'])}; distinct={s['distinct_power_values']}.",
                  f"  Visible changes={s['change_count']}; inter-change seconds={json.dumps(s['inter_change_seconds'])}",
                  f"  Identical-run sample lengths={json.dumps(s['identical_run_lengths_samples'])}; unchanged dwell seconds={json.dumps(s['observed_unchanged_dwell_seconds'])}",
                  f"  Change count compares successive valid observations; {s['change_observations_adjacent_to_gap']} differences span missed slots. Hidden intervening changes unknown. Runs split at gaps: {s['edge_censored_run_count']} edge-censored and {s['gap_censored_run_count']} gap-censored runs; unchanged dwell never spans missing readings.",
                  f"  Estimated visible output-update cadence={s['estimated_output_update_cadence_seconds']}s (실측 추정). Low change count (<20)={s['low_change_count']}.",
                  f"  Conservative declared planning bound NOW={s['planning_bound_seconds']:.9f}s: coefficient=1.25 * (base {s['planning_base_seconds']:.9f}s + poll/transport allowance {s['planning_poll_transport_allowance_seconds']:.9f}s). Base=max(max inter-change gap, max observed unchanged dwell, max valid receipt gap); allowance=max request duration + max valid receipt gap.",
                  f"  Raw={s['raw_jsonl']}; decoded={s['csv']}", ""]
    lines += ["INTERPRETATION / EXPLICIT UNCERTAINTY", "Observed: host retrieval timing and last-measured returned apower values. Estimated: their visible change-interval distribution (실측 추정), usable now in planning/error calculations under the declared conservative condition. Values can remain equal across genuine updates due to stable load or quantization; inter-change intervals therefore can overestimate the register-update period and do not identify ADC cadence or averaging. Polling can alias faster updates, transport shifts receipt boundaries, and absent sample identity/timestamps prevents a formal maximum freshness guarantee. Edge identical runs are censored; even a 120s unchanged run is only observed dwell, not proof the meter did not refresh. No induced load was used to manufacture changes.", "The chosen 1.25 coefficient is an explicit engineering margin, not statistical confidence or a supplier guarantee. With fewer than 20 changes the longest observed unchanged dwell is used in the declared conservative base rather than halting for missing firmware/timestamps. The formula also incorporates maximum observed interval and poll/transport slack for all nodes. A future observation may exceed this planning condition; record that change, do not silently relabel it as a physical guarantee. These bounds replace an unknown-only planning stop, not instrument certification.", "Power maxima in summary are observed in this capture, not rated or guaranteed transition power maxima; cost-error calculations must use an explicit conservative action-specific Pmax separately.", "TRANSFER: genesis2 is measured Gen3; jetson-nano is measured Plus Plug S. genesis3, jetsono, jetsonx, lattepanda, orangepi and rasp5 are NOT measured here. Applying genesis2's bound to those six known Gen3-family Nodes is explicitly provisional same-family transfer; installed hardware/firmware/configuration equivalence is unproved. No other Node is silently called measured.", "", "MINIMAL HEALTH / IMMUTABILITY", json.dumps(summary['minimal_health']), "Only existing HTTP readonly routes queried. Old reports/raw, canonical docs, source/Helm/feeder and runtime/settings left untouched. Existing cost/d/new-root holds retained. No builds, lint, formatter or persistent test framework. One tiny parser/arithmetic self-check executed in capture.py before live requests.", "", f"Machine evidence: {ROOT / 'summary.json'}", f"Procedure/anchors: {CODE / 'capture.py'}, {INPUT / 'capture-metadata.json'}", f"Offline derivation: {CODE / 'analyze.py'}", f"Health: {INPUT / 'health-before.json'}, {INPUT / 'health-after.json'}"]
    (ROOT / "report.txt").write_text("\n".join(lines) + "\n")
    print(json.dumps({"nodes": {n: {k:v for k,v in s.items() if k not in ['identical_runs']} for n,s in summary["nodes"].items()}, "minimal_health": summary["minimal_health"], "report": str(ROOT / "report.txt")}, ensure_ascii=False, indent=2))
