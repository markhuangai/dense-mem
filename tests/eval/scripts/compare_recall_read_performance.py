#!/usr/bin/env python3
"""Gate paired PostgreSQL Search and Recall benchmark output."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import pathlib
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


def _median(values: list[Decimal]) -> Decimal:
    ordered = sorted(values)
    middle = len(ordered) // 2
    if len(ordered) % 2:
        return ordered[middle]
    return (ordered[middle - 1] + ordered[middle]) / 2


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=pathlib.Path, help="raw `go test -bench` output")
    parser.add_argument("--output", required=True, type=pathlib.Path, help="ignored comparison JSON destination")
    args = parser.parse_args(argv)

    root = pathlib.Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()).resolve()
    output = (pathlib.Path.cwd() / args.output).resolve() if not args.output.is_absolute() else args.output.resolve()
    if root / "tests/eval/runs" not in output.parents:
        raise ValueError("comparison output must stay under tests/eval/runs/")

    try:
        report = summarize(parse_benchmarks(args.input.read_text(encoding="utf-8")))
        report["schema_version"] = 2
        report["measured_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
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
