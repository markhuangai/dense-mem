#!/usr/bin/env python3
"""Capture issue #457 query and benchmark results with their measured source."""

from __future__ import annotations

import argparse
import os
import pathlib
import subprocess

from compare_recall_read_performance import source_fingerprint, write_run_source_lock


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--benchmark-output", required=True, type=pathlib.Path)
    parser.add_argument("--query-report", required=True, type=pathlib.Path)
    args = parser.parse_args()

    root = pathlib.Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()).resolve()
    benchmark = args.benchmark_output.resolve()
    query_report = args.query_report.resolve()
    run_dir = root / "tests/eval/runs/issue-457"
    if not benchmark.is_relative_to(run_dir) or not query_report.is_relative_to(run_dir):
        parser.error("capture outputs must stay under this checkout's tests/eval/runs/issue-457/")
    benchmark.parent.mkdir(parents=True, exist_ok=True)
    query_report.parent.mkdir(parents=True, exist_ok=True)

    source = source_fingerprint(root)
    env = os.environ.copy()
    env.pop("DATABASE_URL", None)
    env["DENSE_MEM_REPOSITORY_TESTCONTAINERS"] = "1"
    env["DENSE_MEM_PROJECTION_QUERY_REPORT"] = str(query_report)
    query_log = query_report.with_suffix(".log")
    with query_log.open("w", encoding="utf-8") as output:
        subprocess.run(
            ["go", "test", "./internal/recall/postgres", "-run", "^TestRelationshipProjectionQueryContract$", "-count=1"],
            cwd=root, env=env, stdout=output, stderr=subprocess.STDOUT, check=True,
        )
    env.pop("DENSE_MEM_PROJECTION_QUERY_REPORT")
    with benchmark.open("w", encoding="utf-8") as output:
        subprocess.run(
            ["go", "test", "-tags=integration", "./internal/recall/postgres", "-run", "^$",
             "-bench", "^BenchmarkRecallReadPipeline$", "-benchtime=200x", "-benchmem", "-count=5"],
            cwd=root, env=env, stdout=output, stderr=subprocess.STDOUT, check=True,
        )
    if source_fingerprint(root) != source:
        raise RuntimeError("source checkout changed during benchmark capture")
    lock = write_run_source_lock(benchmark, query_report, source)
    print(f"captured {benchmark} and {query_report} with source lock {lock}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
