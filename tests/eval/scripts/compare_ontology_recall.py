#!/usr/bin/env python3
"""Verify the pinned issue #244 paired Recall latency gate."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import statistics


def compare(text):
    samples = {}
    for line in text.splitlines():
        match = re.match(r"BenchmarkRecallOntology/(grouping|discovery|ordinary_fallback)/(disabled|enabled)-\d+\s+(\d+)\s+(.+)", line)
        if not match:
            continue
        workload, mode, count, metrics = match.groups()
        if int(count) != 200:
            raise ValueError("every sample must contain 200 measurements")
        fields = metrics.split()
        values = dict(zip(fields[1::2], fields[::2]))
        value = float(values["p95-ns/op"])
        if not 0 < value < float("inf"):
            raise ValueError("p95 must be finite and positive")
        samples.setdefault((workload, mode), []).append(value)
    results = {}
    for workload in ("grouping", "discovery", "ordinary_fallback"):
        disabled, enabled = samples.get((workload, "disabled"), []), samples.get((workload, "enabled"), [])
        if len(disabled) != 5 or len(enabled) != 5:
            raise ValueError(f"{workload} requires five complete disabled/enabled pairs")
        regressions = [after / before - 1 for before, after in zip(disabled, enabled)]
        median = statistics.median(regressions)
        results[workload] = {"disabled_p95_ns": disabled, "enabled_p95_ns": enabled, "paired_regressions": regressions, "median_paired_p95_regression": median, "passed": median <= 0.10}
    return results


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    source = args.input.read_bytes()
    results = compare(source.decode())
    passed = all(value["passed"] for value in results.values())
    report = {"schema_version": "dense-mem.ontology.recall_latency.v1", "issue": 244, "source_sha256": "sha256:" + hashlib.sha256(source).hexdigest(), "configuration": {"corpus_count": 96, "limit": 10, "query_embedding": [1, 0, 0], "index_strategy": "exact", "warmups": 20, "measurements": 200, "pairs": 5, "maximum_p95_regression": 0.10}, "workloads": results, "verified": passed}
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"verified": passed, "regressions": {key: value["median_paired_p95_regression"] for key, value in results.items()}}))
    if not passed:
        raise SystemExit("issue #244 p95 latency regression exceeded 10%")


if __name__ == "__main__":
    main()
