#!/usr/bin/env python3
"""Gate paired PostgreSQL Search and Recall benchmark output."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys
from decimal import Decimal, InvalidOperation


WORKLOADS = (
    "lexical",
    "exact_vector",
    "ann",
    "expansion",
    "historical",
    "private_space",
    "relationship_recall",
)
REPETITIONS = 5
ITERATIONS = 200
WARMUPS = 20
MAX_REGRESSION = Decimal("0.05")
MIN_REGRESSION_NS = Decimal(1_000_000)
REQUIRED_METRICS = (
    "p50-ns/op",
    "p95-ns/op",
    "sql-statements/op",
    "transactions/op",
    "telemetry-enabled/op",
    "allocs/op",
    "B/op",
)
PROJECTION_QUERY_STATEMENT_COUNTS = {
    "search_readiness": 3,
    "search_full_text": 1,
    "search_exact_vector": 3,
    "recall_readiness": 1,
    "recall_full_text": 1,
    "recall_exact_vector": 2,
    "recall_ann_vector": 2,
    "recall_expansion": 1,
    "recall_hydration": 1,
}


def parse_benchmarks(text: str) -> dict[tuple[str, str], list[dict[str, Decimal]]]:
    records: dict[tuple[str, str], list[dict[str, Decimal]]] = {}
    slot_modes: dict[tuple[str, str], list[str]] = {}
    for line in text.splitlines():
        tokens = line.split()
        if len(tokens) < 4 or not tokens[0].startswith("BenchmarkRecallReadPipeline/"):
            continue
        parts = tokens[0].split("/")
        if len(parts) != 3:
            raise ValueError(f"unexpected benchmark name: {tokens[0]}")
        _, workload, slot_with_procs = parts
        slot, separator, procs = slot_with_procs.partition("-")
        if separator and not procs.isdigit():
            raise ValueError(f"unexpected benchmark process suffix: {tokens[0]}")
        if workload not in WORKLOADS or slot not in ("pair_a", "pair_b"):
            raise ValueError(f"unexpected benchmark workload or slot: {tokens[0]}")
        if not tokens[1].isdigit() or int(tokens[1]) != ITERATIONS:
            raise ValueError(f"{tokens[0]} ran {tokens[1]} iterations, want {ITERATIONS}")

        metrics: dict[str, Decimal] = {}
        index = 2
        while index + 1 < len(tokens):
            value, name = tokens[index : index + 2]
            try:
                metric = Decimal(value)
            except InvalidOperation as error:
                raise ValueError(f"{tokens[0]} has an invalid {name} value: {value}") from error
            if not metric.is_finite() or metric < 0:
                raise ValueError(f"{tokens[0]} has a non-finite or negative {name} value: {value}")
            metrics[name] = metric
            index += 2
        missing = [name for name in REQUIRED_METRICS if name not in metrics]
        if missing:
            raise ValueError(f"{tokens[0]} is missing metrics: {', '.join(missing)}")
        mode_value = metrics["telemetry-enabled/op"]
        if mode_value not in (0, 1):
            raise ValueError(f"{tokens[0]} has invalid telemetry mode: {mode_value}")
        mode = "enabled" if mode_value == 1 else "disabled"
        records.setdefault((workload, mode), []).append(metrics)
        slot_modes.setdefault((workload, slot), []).append(mode)

    expected = {(workload, mode) for workload in WORKLOADS for mode in ("disabled", "enabled")}
    missing = expected - records.keys()
    if missing:
        names = ", ".join(f"{workload}/{mode}" for workload, mode in sorted(missing))
        raise ValueError(f"benchmark output is missing workloads: {names}")
    for key in sorted(expected):
        if len(records[key]) != REPETITIONS:
            raise ValueError(f"{key[0]}/{key[1]} has {len(records[key])} runs, want {REPETITIONS}")
    for workload in WORKLOADS:
        for slot_index, slot in enumerate(("pair_a", "pair_b")):
            expected_order = ["enabled" if (index + slot_index) % 2 else "disabled" for index in range(REPETITIONS)]
            if slot_modes.get((workload, slot)) != expected_order:
                raise ValueError(f"{workload}/{slot} did not alternate telemetry modes across repetitions")
    return records


def summarize(records: dict[tuple[str, str], list[dict[str, Decimal]]]) -> dict[str, object]:
    workloads: dict[str, object] = {}
    overall_passed = True
    for workload in WORKLOADS:
        disabled = records[(workload, "disabled")]
        enabled = records[(workload, "enabled")]
        for repetition, (baseline, candidate) in enumerate(zip(disabled, enabled, strict=True), start=1):
            for metric in ("sql-statements/op", "transactions/op"):
                if baseline[metric] != candidate[metric]:
                    raise ValueError(
                        f"{workload} repetition {repetition} changed {metric}: "
                        f"{baseline[metric]} -> {candidate[metric]}"
                    )

        workload_passed = True
        timings = {}
        for metric in ("p50-ns/op", "p95-ns/op"):
            before = _median([run[metric] for run in disabled])
            after = _median([run[metric] for run in enabled])
            increase = after - before
            allowed = max(before * MAX_REGRESSION, MIN_REGRESSION_NS)
            passed = increase <= allowed
            workload_passed = workload_passed and passed
            timings[metric] = {
                "disabled_median_ns": float(before),
                "enabled_median_ns": float(after),
                "increase_ns": float(increase),
                "allowed_increase_ns": float(allowed),
                "passed": passed,
            }

        workloads[workload] = {
            "timing": timings,
            "disabled_median_allocs_per_op": float(_median([run["allocs/op"] for run in disabled])),
            "enabled_median_allocs_per_op": float(_median([run["allocs/op"] for run in enabled])),
            "sql_statements_per_op": float(_median([run["sql-statements/op"] for run in disabled])),
            "transactions_per_op": float(_median([run["transactions/op"] for run in disabled])),
            "passed": workload_passed,
        }
        overall_passed = overall_passed and workload_passed

    return {
        "benchmark": "BenchmarkRecallReadPipeline",
        "warmups_per_run": WARMUPS,
        "iterations_per_run": ITERATIONS,
        "repetitions": REPETITIONS,
        "mode_schedule": "alternating disabled and enabled runs",
        "timing_gate": {"max_relative_increase": float(MAX_REGRESSION), "minimum_increase_ns": int(MIN_REGRESSION_NS)},
        "workloads": workloads,
        "passed": overall_passed,
    }


def compare_sources(
    baseline: dict[tuple[str, str], list[dict[str, Decimal]]],
    candidate: dict[tuple[str, str], list[dict[str, Decimal]]],
) -> dict[str, object]:
    workloads: dict[str, object] = {}
    overall_passed = True
    for workload in WORKLOADS:
        modes: dict[str, object] = {}
        for mode in ("disabled", "enabled"):
            before_runs = baseline[(workload, mode)]
            after_runs = candidate[(workload, mode)]
            for repetition, (before, after) in enumerate(zip(before_runs, after_runs, strict=True), start=1):
                for metric in ("sql-statements/op", "transactions/op", "transaction-completions/op"):
                    if before[metric] != after[metric]:
                        raise ValueError(
                            f"{workload}/{mode} repetition {repetition} changed {metric}: "
                            f"{before[metric]} -> {after[metric]}"
                        )

            timing: dict[str, object] = {}
            mode_passed = True
            for metric in ("p50-ns/op", "p95-ns/op"):
                before = _median([run[metric] for run in before_runs])
                after = _median([run[metric] for run in after_runs])
                increase = after - before
                allowed = max(before * MAX_REGRESSION, MIN_REGRESSION_NS)
                passed = increase <= allowed
                mode_passed = mode_passed and passed
                timing[metric] = {
                    "baseline_median_ns": float(before),
                    "candidate_median_ns": float(after),
                    "increase_ns": float(increase),
                    "allowed_increase_ns": float(allowed),
                    "passed": passed,
                }

            modes[mode] = {
                "timing": timing,
                "baseline_median_allocs_per_op": float(_median([run["allocs/op"] for run in before_runs])),
                "candidate_median_allocs_per_op": float(_median([run["allocs/op"] for run in after_runs])),
                "sql_statements_per_op": float(_median([run["sql-statements/op"] for run in after_runs])),
                "transactions_per_op": float(_median([run["transactions/op"] for run in after_runs])),
                "passed": mode_passed,
            }
            overall_passed = overall_passed and mode_passed
        workloads[workload] = modes
    return {"workloads": workloads, "passed": overall_passed}


def compare_query_reports(baseline: object, candidate: object) -> dict[str, int]:
    if not isinstance(baseline, list) or not isinstance(candidate, list) or len(baseline) != len(candidate):
        raise ValueError("query reports have different case counts")
    statement_count = 0
    for before, after in zip(baseline, candidate, strict=True):
        if not isinstance(before, dict) or not isinstance(after, dict) or before.get("case") != after.get("case"):
            raise ValueError("query reports have different cases")
        case = before["case"]
        before_statements = before.get("statements")
        after_statements = after.get("statements")
        if not isinstance(before_statements, list) or not isinstance(after_statements, list):
            raise ValueError(f"{case} query statements are missing")
        if len(before_statements) != len(after_statements):
            raise ValueError(f"{case} changed statement count")
        for index, (old, new) in enumerate(zip(before_statements, after_statements, strict=True), start=1):
            if old.get("sql") != new.get("sql"):
                raise ValueError(f"{case} statement {index} changed SQL")
            if old.get("args") != new.get("args"):
                raise ValueError(f"{case} statement {index} changed bound arguments")
        if before.get("result") != after.get("result"):
            raise ValueError(f"{case} changed decoded result")
        statement_count += len(before_statements)
    return {"case_count": len(baseline), "statement_count": statement_count}


def validate_projection_query_report(report: object) -> None:
    if not isinstance(report, list) or len(report) != len(PROJECTION_QUERY_STATEMENT_COUNTS):
        raise ValueError("query report is missing required projection query cases")
    names = set()
    for case in report:
        if not isinstance(case, dict) or case.get("case") not in PROJECTION_QUERY_STATEMENT_COUNTS:
            raise ValueError("query report is missing required projection query cases")
        name = case["case"]
        if name in names:
            raise ValueError("query report has a duplicate projection query case")
        names.add(name)
        statements = case.get("statements")
        if not isinstance(statements, list) or len(statements) != PROJECTION_QUERY_STATEMENT_COUNTS[name]:
            raise ValueError(f"{name} query report has an incorrect statement count")
        if "result" not in case or any(
            not isinstance(statement, dict)
            or not isinstance(statement.get("sql"), str)
            or not statement["sql"].strip()
            or not isinstance(statement.get("args"), list)
            for statement in statements
        ):
            raise ValueError(f"{name} query report has an incomplete statement or result")
    if names != PROJECTION_QUERY_STATEMENT_COUNTS.keys():
        raise ValueError("query report is missing required projection query cases")


def source_fingerprint(root: pathlib.Path) -> dict[str, object]:
    commit = subprocess.check_output(
        ["git", "--no-optional-locks", "-C", str(root), "rev-parse", "HEAD"], text=True
    ).strip()
    status = subprocess.check_output(
        ["git", "--no-optional-locks", "-C", str(root), "status", "--porcelain=v1", "-z", "--untracked-files=all"]
    )
    paths = sorted({entry[3:].decode("utf-8") for entry in status.split(b"\0") if len(entry) >= 4})
    digest = hashlib.sha256()
    for name in paths:
        path = root / name
        digest.update(name.encode("utf-8") + b"\0")
        if path.is_symlink():
            digest.update(b"symlink\0" + os.readlink(path).encode("utf-8"))
        elif path.is_file():
            digest.update(b"file\0" + hashlib.sha256(path.read_bytes()).digest())
        else:
            digest.update(b"deleted\0")
    return {"commit_sha": commit, "working_tree_sha256": digest.hexdigest(), "working_tree_paths": len(paths)}


def require_run_input(path: pathlib.Path, root: pathlib.Path, label: str) -> None:
    if not path.resolve(strict=True).is_relative_to(root / "tests/eval/runs"):
        raise ValueError(f"{label} must stay under its checkout's tests/eval/runs/")


def verified_baseline_checkout(path: pathlib.Path, candidate_root: pathlib.Path, expected_sha: str) -> pathlib.Path:
    baseline_path = path.resolve(strict=True)
    try:
        baseline_root = pathlib.Path(
            subprocess.check_output(
                ["git", "-C", str(baseline_path.parent), "rev-parse", "--show-toplevel"],
                text=True,
                stderr=subprocess.DEVNULL,
            ).strip()
        ).resolve()
    except subprocess.CalledProcessError as error:
        raise ValueError("baseline input is not inside a Git checkout") from error
    if baseline_root == candidate_root:
        raise ValueError("baseline input must come from a separate checkout")
    require_run_input(baseline_path, baseline_root, "baseline input")
    commit = subprocess.check_output(["git", "-C", str(baseline_root), "rev-parse", "HEAD"], text=True).strip()
    if commit != expected_sha:
        raise ValueError("baseline source SHA does not match the input checkout HEAD")
    return baseline_root


def require_query_reports(
    baseline_report: pathlib.Path | None,
    candidate_report: pathlib.Path | None,
    baseline_root: pathlib.Path,
    candidate_root: pathlib.Path,
) -> None:
    if baseline_report is None or candidate_report is None:
        raise ValueError("baseline and candidate query reports are required for source comparison")
    require_run_input(baseline_report, baseline_root, "baseline query report")
    require_run_input(candidate_report, candidate_root, "candidate query report")


def run_source_lock_path(benchmark_input: pathlib.Path) -> pathlib.Path:
    return benchmark_input.with_suffix(".source.json")


def write_run_source_lock(
    benchmark_input: pathlib.Path, query_report: pathlib.Path, source: dict[str, object]
) -> pathlib.Path:
    lock = run_source_lock_path(benchmark_input)
    payload = {
        "schema_version": 1,
        "benchmark_sha256": hashlib.sha256(benchmark_input.read_bytes()).hexdigest(),
        "query_report_sha256": hashlib.sha256(query_report.read_bytes()).hexdigest(),
        "source": source,
    }
    lock.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return lock


def verified_run_source(
    benchmark_input: pathlib.Path, query_report: pathlib.Path, root: pathlib.Path, expected_sha: str
) -> dict[str, object]:
    lock = run_source_lock_path(benchmark_input)
    require_run_input(lock, root, "source lock")
    payload = json.loads(lock.read_text(encoding="utf-8"))
    if not isinstance(payload, dict) or payload.get("schema_version") != 1:
        raise ValueError("benchmark source lock has an invalid schema")
    if payload.get("benchmark_sha256") != hashlib.sha256(benchmark_input.read_bytes()).hexdigest():
        raise ValueError("benchmark hash does not match its source lock")
    if payload.get("query_report_sha256") != hashlib.sha256(query_report.read_bytes()).hexdigest():
        raise ValueError("query report hash does not match its source lock")
    source = payload.get("source")
    if (
        not isinstance(source, dict)
        or source.get("commit_sha") != expected_sha
        or not isinstance(source.get("working_tree_sha256"), str)
        or re.fullmatch(r"[0-9a-f]{64}", source["working_tree_sha256"]) is None
        or type(source.get("working_tree_paths")) is not int
    ):
        raise ValueError("benchmark source fingerprint does not match the expected commit")
    return source


def _median(values: list[Decimal]) -> Decimal:
    ordered = sorted(values)
    middle = len(ordered) // 2
    if len(ordered) % 2:
        return ordered[middle]
    return (ordered[middle - 1] + ordered[middle]) / 2


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=pathlib.Path, help="raw `go test -bench` output")
    parser.add_argument("--baseline-input", type=pathlib.Path, help="raw benchmark output from the exact base source")
    parser.add_argument("--baseline-source-sha", help="exact commit measured by --baseline-input")
    parser.add_argument("--baseline-query-report", type=pathlib.Path, help="test-only SQL and result capture from base source")
    parser.add_argument("--candidate-query-report", type=pathlib.Path, help="test-only SQL and result capture from candidate source")
    parser.add_argument("--output", required=True, type=pathlib.Path, help="ignored comparison JSON destination")
    args = parser.parse_args(argv)

    root = pathlib.Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()).resolve()
    output = (pathlib.Path.cwd() / args.output).resolve() if not args.output.is_absolute() else args.output.resolve()
    if root / "tests/eval/runs" not in output.parents:
        raise ValueError("comparison output must stay under tests/eval/runs/")

    try:
        if (args.baseline_input is None) != (args.baseline_source_sha is None):
            raise ValueError("--baseline-input and --baseline-source-sha must be supplied together")
        if (args.baseline_query_report is None) != (args.candidate_query_report is None):
            raise ValueError("query reports must be supplied together")
        if args.baseline_input is None and args.baseline_query_report is not None:
            raise ValueError("query reports require a baseline input")
        require_run_input(args.input, root, "candidate input")
        candidate_records = parse_benchmarks(args.input.read_text(encoding="utf-8"))
        report = summarize(candidate_records)
        if args.baseline_input is not None:
            if re.fullmatch(r"[0-9a-f]{40}", args.baseline_source_sha) is None:
                raise ValueError("--baseline-source-sha must be a 40-character lowercase commit SHA")
            baseline_root = verified_baseline_checkout(
                args.baseline_input, root, args.baseline_source_sha
            )
            require_query_reports(args.baseline_query_report, args.candidate_query_report, baseline_root, root)
            report["baseline_source"] = verified_run_source(
                args.baseline_input, args.baseline_query_report, baseline_root, args.baseline_source_sha
            )
            candidate_sha = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
            report["source"] = verified_run_source(args.input, args.candidate_query_report, root, candidate_sha)
            if report["source"] == report["baseline_source"]:
                raise ValueError("baseline and candidate source locks identify the same measured source")
            baseline_text = args.baseline_input.read_text(encoding="utf-8")
            baseline_records = parse_benchmarks(baseline_text)
            baseline_report = summarize(baseline_records)
            report["baseline_telemetry_diagnostic"] = baseline_report
            report["baseline_source_sha"] = args.baseline_source_sha
            report["baseline_output_sha256"] = hashlib.sha256(baseline_text.encode("utf-8")).hexdigest()
            report["source_comparison"] = compare_sources(baseline_records, candidate_records)
            report["passed"] = report["passed"] and report["source_comparison"]["passed"]
        if args.baseline_query_report is not None:
            baseline_queries = json.loads(args.baseline_query_report.read_text(encoding="utf-8"))
            candidate_queries = json.loads(args.candidate_query_report.read_text(encoding="utf-8"))
            validate_projection_query_report(baseline_queries)
            validate_projection_query_report(candidate_queries)
            report["query_contract"] = compare_query_reports(baseline_queries, candidate_queries)
        report["schema_version"] = 2
        report["measured_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
        if args.baseline_input is None:
            report["source"] = source_fingerprint(root)
        report["go_version"] = subprocess.check_output(["go", "version"], text=True).strip()
        report["goos"] = subprocess.check_output(["go", "env", "GOOS"], text=True).strip()
        report["goarch"] = subprocess.check_output(["go", "env", "GOARCH"], text=True).strip()
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"recall read benchmark comparison failed: {error}", file=sys.stderr)
        return 1

    print(f"Recall read benchmark comparison: {'passed' if report['passed'] else 'failed'}")
    print(f"wrote {output.relative_to(root)} for source {report['source']['commit_sha']}")
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
