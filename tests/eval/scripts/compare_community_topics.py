#!/usr/bin/env python3
"""Verify issue #245's five paired real-PostgreSQL community Recall measurements."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import statistics


def compare(text):
    samples = {"legacy": [], "ontology": []}
    for line in text.splitlines():
        match = re.match(r"BenchmarkCommunityTopicProjection/(legacy|ontology)-\d+\s+(\d+)\s+(.+)", line)
        if not match:
            continue
        mode, count, metrics = match.groups()
        if int(count) != 200:
            raise ValueError("every sample requires 200 measurements")
        fields = metrics.split()
        value = float(dict(zip(fields[1::2], fields[::2]))["p95-ns/op"])
        if not 0 < value < float("inf"):
            raise ValueError("p95 must be finite and positive")
        samples[mode].append(value)
    if any(len(values) != 5 for values in samples.values()):
        raise ValueError("five complete legacy/ontology pairs are required")
    regressions = [after / before - 1 for before, after in zip(samples["legacy"], samples["ontology"])]
    median = statistics.median(regressions)
    return {"legacy_p95_ns": samples["legacy"], "ontology_p95_ns": samples["ontology"], "paired_regressions": regressions, "median_paired_p95_regression": median, "passed": median <= 0.10}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    source = args.input.read_bytes()
    result = compare(source.decode())
    report = {"schema_version": "dense-mem.community.topic_latency.v1", "issue": 245, "source_sha256": "sha256:" + hashlib.sha256(source).hexdigest(), "configuration": {"source_count": 26, "warmups": 20, "measurements": 200, "pairs": 5, "limit": 3, "relationship_limit": 5, "maximum_p95_regression": 0.10}, "results": result, "verified": result["passed"]}
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"verified": result["passed"], "median_paired_p95_regression": result["median_paired_p95_regression"]}))
    if not result["passed"]:
        raise SystemExit("issue #245 median paired p95 regression exceeded 10%")


if __name__ == "__main__":
    main()
