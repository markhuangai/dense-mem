import importlib.util
import json
import pathlib
import subprocess
import tempfile
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("compare_recall_read_performance.py")
SPEC = importlib.util.spec_from_file_location("compare_recall_read_performance", MODULE_PATH)
comparison = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(comparison)


def benchmark_output(enabled_adjustment=0, changed_count=None):
    lines = []
    for workload in comparison.WORKLOADS:
        for slot_index, slot in enumerate(("pair_a", "pair_b")):
            for repetition in range(comparison.REPETITIONS):
                enabled = (repetition + slot_index) % 2 == 1
                mode = "enabled" if enabled else "disabled"
                adjustment = enabled_adjustment if mode == "enabled" else 0
                statements = 8 if changed_count is None or mode == "disabled" else changed_count
                lines.append(
                    f"BenchmarkRecallReadPipeline/{workload}/{slot}-8 200 "
                    f"{2_000_000 + adjustment} ns/op 1200 B/op 20 allocs/op "
                    f"{1_500_000 + adjustment} p50-ns/op "
                    f"{3_000_000 + adjustment} p95-ns/op {statements} sql-statements/op "
                    f"4 transactions/op 4 transaction-completions/op {int(enabled)} telemetry-enabled/op"
                )
    return "\n".join(lines)


class ReadPerformanceComparisonTests(unittest.TestCase):
    def test_baseline_log_is_bound_to_its_checkout_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory) / "base"
            root.mkdir()
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            (root / ".gitignore").write_text("tests/eval/runs/\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", ".gitignore"], check=True)
            subprocess.run(
                ["git", "-C", str(root), "-c", "user.name=Test", "-c", "user.email=ci@dense-mem.dev",
                 "commit", "--allow-empty", "-qm", "base"],
                check=True,
            )
            sha = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
            log = root / "tests/eval/runs/issue-457/base.txt"
            log.parent.mkdir(parents=True)
            log.write_text(benchmark_output(), encoding="utf-8")

            baseline_root = comparison.verified_baseline_checkout(log, root.parent / "candidate", sha)
            self.assertEqual(baseline_root, root)
            with self.assertRaisesRegex(ValueError, "does not match"):
                comparison.verified_baseline_checkout(log, root.parent / "candidate", "0" * 40)
            with self.assertRaisesRegex(ValueError, "separate checkout"):
                comparison.verified_baseline_checkout(log, root, sha)

            misplaced = root / "other.txt"
            misplaced.write_text(benchmark_output(), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "tests/eval/runs"):
                comparison.verified_baseline_checkout(misplaced, root.parent / "candidate", sha)

            candidate_root = root.parent / "candidate"
            candidate_report = candidate_root / "tests/eval/runs/issue-457/candidate.json"
            candidate_report.parent.mkdir(parents=True)
            candidate_report.write_text("[]", encoding="utf-8")
            base_report = root / "tests/eval/runs/issue-457/base.json"
            base_report.write_text("[]", encoding="utf-8")
            comparison.require_query_reports(base_report, candidate_report, root, candidate_root)
            with self.assertRaisesRegex(ValueError, "required"):
                comparison.require_query_reports(None, candidate_report, root, candidate_root)
            with self.assertRaisesRegex(ValueError, "baseline query report"):
                comparison.require_query_reports(candidate_report, candidate_report, root, candidate_root)
            with self.assertRaisesRegex(ValueError, "candidate query report"):
                comparison.require_query_reports(base_report, base_report, root, candidate_root)

            source = comparison.source_fingerprint(root)
            lock = comparison.write_run_source_lock(log, base_report, source)
            self.assertEqual(comparison.verified_run_source(log, base_report, root, sha), source)
            log.write_text(benchmark_output(50_000), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "benchmark hash"):
                comparison.verified_run_source(log, base_report, root, sha)
            log.write_text(benchmark_output(), encoding="utf-8")
            base_report.write_text("[{}]", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "query report hash"):
                comparison.verified_run_source(log, base_report, root, sha)
            base_report.write_text("[]", encoding="utf-8")
            misplaced.write_text("changed after capture", encoding="utf-8")
            self.assertEqual(comparison.verified_run_source(log, base_report, root, sha), source)
            payload = json.loads(lock.read_text(encoding="utf-8"))
            payload["source"]["commit_sha"] = "0" * 40
            lock.write_text(json.dumps(payload), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "source fingerprint"):
                comparison.verified_run_source(log, base_report, root, sha)

    def test_projection_query_report_requires_every_planned_case(self):
        expected = (
            ("search_readiness", 3), ("search_full_text", 1), ("search_exact_vector", 3),
            ("recall_readiness", 1), ("recall_full_text", 1), ("recall_exact_vector", 2),
            ("recall_ann_vector", 2), ("recall_expansion", 1), ("recall_hydration", 1),
        )
        report = [
            {"case": name, "statements": [{"sql": "SELECT 1", "args": []} for _ in range(count)], "result": []}
            for name, count in expected
        ]
        comparison.validate_projection_query_report(report)
        for incomplete in ([], report[:-1]):
            with self.assertRaisesRegex(ValueError, "required projection query cases"):
                comparison.validate_projection_query_report(incomplete)
        report[0]["statements"].pop()
        with self.assertRaisesRegex(ValueError, "statement count"):
            comparison.validate_projection_query_report(report)

    def test_query_report_comparison_checks_sql_arguments_and_results(self):
        baseline = [{"case": "recall_exact_vector", "statements": [{"sql": "SELECT ?", "args": ["str:team-a"]}], "result": [{"id": "relationship-a"}]}]
        self.assertEqual(comparison.compare_query_reports(baseline, baseline), {"case_count": 1, "statement_count": 1})
        for field, value, message in (
            ("sql", "SELECT other", "changed SQL"),
            ("args", ["str:team-b"], "changed bound arguments"),
        ):
            candidate = [{"case": baseline[0]["case"], "statements": [dict(baseline[0]["statements"][0])], "result": baseline[0]["result"]}]
            candidate[0]["statements"][0][field] = value
            with self.assertRaisesRegex(ValueError, message):
                comparison.compare_query_reports(baseline, candidate)
        candidate = [{"case": baseline[0]["case"], "statements": baseline[0]["statements"], "result": []}]
        with self.assertRaisesRegex(ValueError, "changed decoded result"):
            comparison.compare_query_reports(baseline, candidate)

    def test_base_source_comparison_accepts_matching_counts_and_small_latency_delta(self):
        baseline = comparison.parse_benchmarks(benchmark_output())
        candidate = comparison.parse_benchmarks(benchmark_output(50_000))
        report = comparison.compare_sources(baseline, candidate)
        self.assertTrue(report["passed"])
        self.assertEqual(len(report["workloads"]), 7)
        self.assertTrue(report["workloads"]["relationship_recall"]["enabled"]["passed"])

    def test_base_source_comparison_rejects_p95_regression_in_one_mode(self):
        baseline = comparison.parse_benchmarks(benchmark_output())
        candidate = comparison.parse_benchmarks(benchmark_output(1_100_000))
        report = comparison.compare_sources(baseline, candidate)
        self.assertFalse(report["passed"])
        self.assertTrue(report["workloads"]["relationship_recall"]["disabled"]["passed"])
        self.assertFalse(report["workloads"]["relationship_recall"]["enabled"]["timing"]["p95-ns/op"]["passed"])

    def test_base_source_comparison_rejects_statement_and_transaction_drift(self):
        baseline = comparison.parse_benchmarks(benchmark_output())
        for candidate_text, metric in (
            (benchmark_output().replace("8 sql-statements/op", "9 sql-statements/op"), "sql-statements/op"),
            (benchmark_output().replace("4 transactions/op", "5 transactions/op"), "transactions/op"),
        ):
            candidate = comparison.parse_benchmarks(candidate_text)
            with self.assertRaisesRegex(ValueError, f"changed {metric}"):
                comparison.compare_sources(baseline, candidate)

    def test_accepts_small_latency_change_and_preserves_workload_counts(self):
        report = comparison.summarize(comparison.parse_benchmarks(benchmark_output(50_000)))
        self.assertTrue(report["passed"])
        self.assertEqual(len(report["workloads"]), 7)
        self.assertEqual(report["workloads"]["lexical"]["transactions_per_op"], 4.0)

    def test_rejects_p95_above_relative_and_absolute_threshold(self):
        report = comparison.summarize(comparison.parse_benchmarks(benchmark_output(1_100_000)))
        self.assertFalse(report["passed"])
        self.assertFalse(report["workloads"]["lexical"]["passed"])

    def test_rejects_additional_database_statements(self):
        runs = comparison.parse_benchmarks(benchmark_output(changed_count=9))
        with self.assertRaisesRegex(ValueError, "changed sql-statements/op"):
            comparison.summarize(runs)

    def test_rejects_missing_repetition(self):
        lines = benchmark_output().splitlines()
        with self.assertRaisesRegex(ValueError, "has 4 runs, want 5"):
            comparison.parse_benchmarks("\n".join(lines[:-1]))

    def test_rejects_nonstandard_iteration_count(self):
        lines = benchmark_output().splitlines()
        lines[0] = lines[0].replace(" 200 ", " 199 ", 1)
        with self.assertRaisesRegex(ValueError, "want 200"):
            comparison.parse_benchmarks("\n".join(lines))

    def test_rejects_invalid_metric_values(self):
        lines = benchmark_output().splitlines()
        lines[0] = lines[0].replace("1500000 p50-ns/op", "unknown p50-ns/op")
        with self.assertRaisesRegex(ValueError, "invalid p50-ns/op value"):
            comparison.parse_benchmarks("\n".join(lines))

    def test_accepts_single_cpu_benchmark_names_without_suffix(self):
        output = benchmark_output().replace("-8 200 ", " 200 ")
        report = comparison.summarize(comparison.parse_benchmarks(output))
        self.assertTrue(report["passed"])

    def test_rejects_non_alternating_mode_schedule(self):
        lines = benchmark_output().splitlines()
        lines[0] = lines[0].replace("0 telemetry-enabled/op", "1 telemetry-enabled/op")
        lines[comparison.REPETITIONS] = lines[comparison.REPETITIONS].replace(
            "1 telemetry-enabled/op", "0 telemetry-enabled/op"
        )
        with self.assertRaisesRegex(ValueError, "did not alternate telemetry modes"):
            comparison.parse_benchmarks("\n".join(lines))


if __name__ == "__main__":
    unittest.main()
