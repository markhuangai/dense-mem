#!/usr/bin/env python3
"""Score the maintainer-approved issue #659 base/candidate latency protocol."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import statistics


WORKLOADS = ("grouping", "discovery", "ordinary_fallback")
MODES = ("disabled", "enabled")


def parse_sample(text):
    samples = {}
    for line in text.splitlines():
        match = re.match(r"BenchmarkRecallOntology/(grouping|discovery|ordinary_fallback)/(disabled|enabled)-\d+\s+(\d+)\s+(.+)", line)
        if not match:
            continue
        workload, mode, count, metrics = match.groups()
        key = (workload, mode)
        if int(count) != 200 or key in samples:
            raise ValueError("each arm requires one 200-measurement sample per workload/mode")
        fields = metrics.split()
        values = dict(zip(fields[1::2], fields[::2]))
        value = float(values["p95-ns/op"])
        if not 0 < value < float("inf"):
            raise ValueError("p95 must be finite and positive")
        samples[key] = value
    if set(samples) != {(workload, mode) for workload in WORKLOADS for mode in MODES}:
        raise ValueError("every arm requires all six workload/mode samples")
    return samples


def compare(protocol, directory):
    pairs = protocol["pairs"]
    if [pair["pair"] for pair in pairs] != list(range(1, 6)):
        raise ValueError("five predeclared pairs are required in order")
    samples = {arm: [] for arm in ("base", "candidate")}
    for pair in pairs:
        expected = ["base", "candidate"] if pair["pair"] % 2 else ["candidate", "base"]
        if pair["order"] != expected or set(pair["runs"]) != set(expected):
            raise ValueError("base/candidate order must alternate across pairs")
        for arm in expected:
            run = pair["runs"][arm]
            if run["status"] != "passed" or run["exit_code"] != 0:
                raise ValueError("failed or incomplete arms cannot satisfy the gate")
            if run["source_fingerprint"] != protocol["sources"][arm]["fingerprint"]:
                raise ValueError("source identity changed after freezing the protocol")
            data = (directory / run["log"]).read_bytes()
            if hashlib.sha256(data).hexdigest() != run["log_sha256"]:
                raise ValueError("benchmark log changed after capture")
            samples[arm].append(parse_sample(data.decode()))
    results = {}
    for workload in WORKLOADS:
        modes = {}
        for mode in MODES:
            base = [sample[(workload, mode)] for sample in samples["base"]]
            candidate = [sample[(workload, mode)] for sample in samples["candidate"]]
            regressions = [after / before - 1 for before, after in zip(base, candidate)]
            modes[mode] = {"base_p95_ns": base, "candidate_p95_ns": candidate,
                           "paired_regressions": regressions,
                           "median_paired_p95_regression": statistics.median(regressions),
                           "median_paired_p95_ratio": statistics.median([after / before for before, after in zip(base, candidate)])}
        results[workload] = {"enabled": modes["enabled"], "disabled_control": modes["disabled"],
                             "passed": modes["enabled"]["median_paired_p95_ratio"] <= 1.10}
    return results


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--protocol", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    raw = args.protocol.read_bytes()
    protocol = json.loads(raw)
    results = compare(protocol, args.protocol.parent)
    passed = all(result["passed"] for result in results.values())
    report = {"schema_version": "dense-mem.ontology.recall_base_pairs.v1", "issue": 659,
              "protocol_sha256": hashlib.sha256(raw).hexdigest(), "sources": protocol["sources"],
              "maximum_p95_regression": 0.10, "pairs": 5, "workloads": results, "verified": passed}
    args.out.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"verified": passed, "regressions": {
        workload: result["enabled"]["median_paired_p95_regression"] for workload, result in results.items()}}))
    if not passed:
        raise SystemExit("issue #659 base/candidate p95 latency regression exceeded 10%")


if __name__ == "__main__":
    main()
