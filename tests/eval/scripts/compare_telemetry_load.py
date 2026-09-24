#!/usr/bin/env python3
"""Run paired read-only MCP and Prometheus traffic against baseline/candidate."""

import argparse
import concurrent.futures
import json
import math
import os
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


LEDGER_STATUS = b"densemem_operational_ledger_collection_success 1"
TOOLS_LIST = b'{"jsonrpc":"2.0","id":%d,"method":"tools/list","params":{}}'
MAX_REGRESSION_PERCENT = 10.0


def percentile(values, fraction):
    ordered = sorted(values)
    index = max(0, math.ceil(fraction * len(ordered)) - 1)
    return ordered[index]


def request(url, method, token, body=None, request_id=None, timeout=15):
    headers = {"Authorization": "Bearer " + token}
    if method == "POST":
        headers.update({
            "Accept": "application/json, text/event-stream",
            "Content-Type": "application/json",
            "MCP-Protocol-Version": "2025-11-25",
        })
    req = urllib.request.Request(url, data=body, headers=headers, method=method)
    started = time.perf_counter()
    with urllib.request.urlopen(req, timeout=timeout) as response:
        payload = response.read()
        if response.status != 200:
            raise RuntimeError(f"{method} returned HTTP {response.status}")
    elapsed = time.perf_counter() - started
    if method == "POST":
        content = payload
        if b"text/event-stream" in response.headers.get("Content-Type", "").encode().lower():
            data_lines = [line[6:] for line in payload.splitlines() if line.startswith(b"data: ")]
            content = data_lines[-1] if data_lines else b""
        try:
            envelope = json.loads(content)
        except json.JSONDecodeError as error:
            raise RuntimeError("MCP tools/list returned an invalid response") from error
        if envelope.get("id") != request_id or "result" not in envelope or "error" in envelope:
            raise RuntimeError("MCP tools/list did not return a successful JSON-RPC result")
    return elapsed, payload


class Scraper(threading.Thread):
    def __init__(self, name, url, token, interval, require_ledger_status):
        super().__init__(daemon=True)
        self.name = name
        self.url = url
        self.token = token
        self.interval = interval
        self.require_ledger_status = require_ledger_status
        self.stop_event = threading.Event()
        self.samples = []
        self.errors = []
        self.ledger_status_seen = False

    def run(self):
        while not self.stop_event.is_set():
            try:
                elapsed, payload = request(self.url, "GET", self.token)
                self.samples.append(elapsed)
                if LEDGER_STATUS in payload:
                    self.ledger_status_seen = True
                elif self.require_ledger_status:
                    self.errors.append("candidate ledger collector did not report success")
            except Exception as error:  # surfaced in the run receipt below
                self.errors.append(type(error).__name__)
            if self.stop_event.wait(self.interval):
                break

    def stop(self):
        self.stop_event.set()
        self.join()


def paired_batch(executor, endpoints, tokens, first_id, count):
    futures = []
    for offset in range(count):
        request_id = first_id + offset
        body = TOOLS_LIST % request_id
        for name in ("baseline", "candidate"):
            futures.append((name, executor.submit(request, endpoints[name], "POST", tokens[name], body, request_id)))
    results = {"baseline": [], "candidate": []}
    errors = {"baseline": [], "candidate": []}
    for name, future in futures:
        try:
            elapsed, _ = future.result()
            results[name].append(elapsed)
        except Exception as error:  # surfaced with the arm and exception type
            errors[name].append(type(error).__name__)
    return results, errors


def arm_summary(samples, elapsed):
    return {
        "requests": len(samples),
        "errors": 0,
        "throughput_requests_per_second": len(samples) / elapsed if elapsed > 0 else 0,
        "p50_seconds": percentile(samples, 0.50),
        "p95_seconds": percentile(samples, 0.95),
        "max_seconds": max(samples),
    }


def compare_arm_performance(samples, elapsed_by_arm):
    summaries = {name: arm_summary(samples[name], elapsed_by_arm[name]) for name in samples}
    baseline = summaries["baseline"]
    candidate = summaries["candidate"]
    p95_increase = ((candidate["p95_seconds"] / baseline["p95_seconds"]) - 1) * 100 if baseline["p95_seconds"] > 0 else (0 if candidate["p95_seconds"] == 0 else None)
    throughput_loss = ((baseline["throughput_requests_per_second"] - candidate["throughput_requests_per_second"]) / baseline["throughput_requests_per_second"]) * 100 if baseline["throughput_requests_per_second"] > 0 else None
    return {
        "arms": summaries,
        "candidate_p95_increase_percent": p95_increase,
        "candidate_throughput_loss_percent": throughput_loss,
        "p95_pass": p95_increase is not None and p95_increase <= MAX_REGRESSION_PERCENT,
        "throughput_pass": throughput_loss is not None and throughput_loss <= MAX_REGRESSION_PERCENT,
    }


def request_tokens(environment):
    shared = environment.get("DENSE_MEM_LOAD_TOKEN", "")
    tokens = {
        "baseline": environment.get("DENSE_MEM_BASELINE_LOAD_TOKEN") or shared,
        "candidate": environment.get("DENSE_MEM_CANDIDATE_LOAD_TOKEN") or shared,
    }
    if any(not token for token in tokens.values()):
        raise ValueError(
            "set DENSE_MEM_LOAD_TOKEN or both arm-specific load tokens"
        )
    return tokens


def safe_endpoint(url):
    parsed = urllib.parse.urlsplit(url)
    netloc = parsed.hostname or ""
    if parsed.port is not None:
        netloc += ":" + str(parsed.port)
    return urllib.parse.urlunsplit((parsed.scheme, netloc, parsed.path, "", ""))


def measured_worker(url, token, first_id, count, worker_index, concurrency, start_event):
    start_event.wait()
    samples = []
    errors = []
    for offset in range(worker_index, count, concurrency):
        request_id = first_id + offset
        try:
            elapsed, _ = request(url, "POST", token, TOOLS_LIST % request_id, request_id)
            samples.append(elapsed)
        except Exception as error:  # surfaced with the arm and exception type
            errors.append(type(error).__name__)
    return samples, errors, time.perf_counter()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline-mcp", required=True)
    parser.add_argument("--candidate-mcp", required=True)
    parser.add_argument("--baseline-metrics", required=True)
    parser.add_argument("--candidate-metrics", required=True)
    parser.add_argument("--count", type=int, default=400)
    parser.add_argument("--warmup", type=int, default=20)
    parser.add_argument("--concurrency", type=int, default=16)
    parser.add_argument("--scrape-interval", type=float, default=1.0)
    parser.add_argument("--output", default="tests/eval/runtime/v1/runs/telemetry-overhead.json")
    args = parser.parse_args()

    if min(args.count, args.concurrency) < 1 or args.warmup < 0 or args.scrape_interval <= 0:
        parser.error("count/concurrency must be positive; warmup nonnegative; scrape interval positive")
    common_scrape_token = os.environ.get("DENSE_MEM_LOAD_SCRAPE_TOKEN", "")
    try:
        tokens = request_tokens(os.environ)
    except ValueError as error:
        parser.error(str(error))
    if not common_scrape_token:
        parser.error("set DENSE_MEM_LOAD_SCRAPE_TOKEN in the environment")

    mcp_urls = {"baseline": args.baseline_mcp, "candidate": args.candidate_mcp}
    metrics_urls = {"baseline": args.baseline_metrics, "candidate": args.candidate_metrics}
    for url in [*mcp_urls.values(), *metrics_urls.values()]:
        if urllib.parse.urlsplit(url).username or urllib.parse.urlsplit(url).password:
            parser.error("URLs must not contain credentials")

    scrape_tokens = {
        "baseline": os.environ.get("DENSE_MEM_BASELINE_SCRAPE_TOKEN", common_scrape_token),
        "candidate": os.environ.get("DENSE_MEM_CANDIDATE_SCRAPE_TOKEN", common_scrape_token),
    }
    for name in ("baseline", "candidate"):
        _, payload = request(metrics_urls[name], "GET", scrape_tokens[name])
        if name == "candidate" and LEDGER_STATUS not in payload:
            parser.error("candidate scrape did not report densemem_operational_ledger_collection_success 1")

    workers = max(2, args.concurrency * 2)
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as executor:
        warmup_results, warmup_errors = paired_batch(executor, mcp_urls, tokens, 1, args.warmup)
        if any(warmup_errors.values()):
            raise RuntimeError("warmup traffic failed: " + json.dumps(warmup_errors, sort_keys=True))
        scrapers = [
            Scraper("baseline", metrics_urls["baseline"], scrape_tokens["baseline"], args.scrape_interval, False),
            Scraper("candidate", metrics_urls["candidate"], scrape_tokens["candidate"], args.scrape_interval, True),
        ]
        for scraper in scrapers:
            scraper.start()

        samples = {"baseline": [], "candidate": []}
        errors = {"baseline": [], "candidate": []}
        start_event = threading.Event()
        measured = {
            name: [
                executor.submit(
                    measured_worker,
                    mcp_urls[name],
                    tokens[name],
                    args.warmup + 1,
                    args.count,
                    worker_index,
                    args.concurrency,
                    start_event,
                )
                for worker_index in range(args.concurrency)
            ]
            for name in ("baseline", "candidate")
        }
        started = time.perf_counter()
        start_event.set()
        elapsed_by_arm = {}
        for name, futures in measured.items():
            completed_at = []
            for future in futures:
                arm_samples, arm_errors, finished_at = future.result()
                samples[name].extend(arm_samples)
                errors[name].extend(arm_errors)
                completed_at.append(finished_at)
            elapsed_by_arm[name] = max(completed_at) - started

        time.sleep(min(args.scrape_interval, 2.0))
        for scraper in scrapers:
            scraper.stop()

    if any(errors.values()):
        raise RuntimeError("measured traffic failed: " + json.dumps(errors, sort_keys=True))
    if any(not scraper.samples or scraper.errors for scraper in scrapers):
        raise RuntimeError("Prometheus scrapes failed: " + json.dumps({s.name: s.errors for s in scrapers}, sort_keys=True))

    comparison = compare_arm_performance(samples, elapsed_by_arm)
    receipt = {
        "workload": "paired authenticated MCP tools/list requests with concurrent Prometheus scraping",
        "count_per_arm": args.count,
        "warmup_per_arm": args.warmup,
        "concurrency_per_arm": args.concurrency,
        "scrape_interval_seconds": args.scrape_interval,
        "max_regression_percent": MAX_REGRESSION_PERCENT,
        "mcp_endpoints": {name: safe_endpoint(url) for name, url in mcp_urls.items()},
        "metrics_endpoints": {name: safe_endpoint(url) for name, url in metrics_urls.items()},
        "measurement_seconds": max(elapsed_by_arm.values()),
        "measurement_seconds_by_arm": elapsed_by_arm,
        "arms": comparison["arms"],
        "scrapes": {scraper.name: {
            "count": len(scraper.samples),
            "p95_seconds": percentile(scraper.samples, 0.95) if scraper.samples else None,
            "ledger_collection_success_seen": scraper.ledger_status_seen,
        } for scraper in scrapers},
        "candidate_p95_increase_percent": comparison["candidate_p95_increase_percent"],
        "candidate_throughput_loss_percent": comparison["candidate_throughput_loss_percent"],
        "passed": comparison["p95_pass"] and comparison["throughput_pass"],
    }
    os.makedirs(os.path.dirname(args.output) or ".", exist_ok=True)
    with open(args.output, "w", encoding="utf-8") as output:
        json.dump(receipt, output, indent=2, sort_keys=True)
        output.write("\n")
    print(json.dumps(receipt, indent=2, sort_keys=True))
    return 0 if receipt["passed"] else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, RuntimeError, urllib.error.URLError) as error:
        print(f"telemetry load comparison failed: {error}", file=sys.stderr)
        raise SystemExit(2)
