import importlib.util
import pathlib
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
