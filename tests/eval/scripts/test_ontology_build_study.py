import copy
import unittest

from ontology_build_study import (
    LOOKUP_ARMS,
    PENDING_ARMS,
    PENDING_COUNTERS,
    PENDING_ORDERS,
    compare_lookup_dataset,
    compare_pending_queue,
    paired_lookup_savings,
    pending_decision,
    verify_lookup_schedule,
    verify_pending_queue,
)


def scheduled_turns(arms=LOOKUP_ARMS):
    turn = {
        "PageLimit": 100,
        "Sources": ["source-a", "source-b"],
        "Candidates": ["first-topic", "second-topic"],
        "HTTPBodies": ['{"model":"synthetic","messages":[]}'],
    }
    return {arm: [copy.deepcopy(turn) for _ in range(3)] for arm in arms}


class LookupComparisonTests(unittest.TestCase):
    def test_missing_schedule_arm(self):
        schedules = scheduled_turns()
        verify_lookup_schedule(schedules)
        del schedules["rank-first"]
        with self.assertRaisesRegex(ValueError, "missing fixed-schedule arm"):
            verify_lookup_schedule(schedules)

    def test_input_drift_in_order_and_transport_body(self):
        for field, values in (
            ("Candidates", ["second-topic", "first-topic"]),
            ("HTTPBodies", ['{"model":"changed","messages":[]}']),
        ):
            with self.subTest(field=field):
                schedules = scheduled_turns()
                schedules["rank-first"][1][field] = values
                with self.assertRaisesRegex(ValueError, f"turn 2: {field} drift"):
                    verify_lookup_schedule(schedules)

    def test_missing_timed_arm(self):
        source = ("a" * 40, "33", "1")
        sha = "b" * 64
        data = {
            "Protocol": "ontology-build-rank-first-v1",
            "Shape": {"Dataset": "hotpotqa"},
            "Source": source[0],
            "RunID": source[1],
            "RunAttempt": source[2],
            "Runner": "study-runner",
            "PostgresVersion": "18.1",
            "TemplateSHA": sha,
            "CorrectionSHA": "sha256:" + sha,
            "Stage": "complete",
            "Plans": {arm: {"BindingsSHA": "sha256:" + sha, "QuerySHA": sha,
                            "Plan": {"Raw": [{}], "PageBeforePayload": arm == "rank-first"}}
                      for arm in LOOKUP_ARMS},
            "ScheduledInputs": scheduled_turns(),
            "Runs": [{"Pair": 1, "Arm": "base"}, {"Pair": 1, "Arm": "vector"}],
        }
        with self.assertRaisesRegex(ValueError, "incomplete or misordered repetitions"):
            compare_lookup_dataset(data, "hotpotqa", source, dict.fromkeys(LOOKUP_ARMS, sha))

    def test_original_and_incremental_savings_use_their_own_control(self):
        original = [{"ElapsedMS": 100, "CorrectionMS": 5}] * 4
        vector = [{"ElapsedMS": 86, "CorrectionMS": 5}] * 4
        ranked = [{"ElapsedMS": 87, "CorrectionMS": 5}] * 4
        total = paired_lookup_savings(original, ranked)
        incremental = paired_lookup_savings(vector, ranked)
        self.assertEqual(total["median_paired_saving_percent"], 13)
        self.assertTrue(total["passes_screen"])
        self.assertEqual(incremental["paired_saving_ms"], [-1] * 4)
        self.assertFalse(incremental["passes_screen"])

    def test_variability_and_repeatable_correction_regression_hold(self):
        original = [{"ElapsedMS": elapsed, "CorrectionMS": correction}
                    for elapsed, correction in zip([90, 100, 110, 120], [10, 12, 14, 16])]
        faster = [{"ElapsedMS": run["ElapsedMS"] - 1, "CorrectionMS": run["CorrectionMS"]}
                  for run in original]
        self.assertFalse(paired_lookup_savings(original, faster)["passes_screen"])
        slower_correction = [{"ElapsedMS": run["ElapsedMS"] - 20, "CorrectionMS": run["CorrectionMS"] + 4}
                             for run in original]
        report = paired_lookup_savings(original, slower_correction)
        self.assertTrue(report["correction_regression"])
        self.assertFalse(report["passes_screen"])


class PendingRefreshComparisonTests(unittest.TestCase):
    def run_receipt(self, arm, elapsed=100):
        skip = arm.endswith("-skip")
        return {
            **dict.fromkeys(PENDING_COUNTERS, 0),
            "Arm": arm,
            "ElapsedMS": elapsed,
            "CorrectionMS": 5,
            "DefinitionPages": 4,
            "ClaimReads": 4 if skip else 0,
            "PendingSkips": 15 if skip else 0,
            "RefreshedPending": 10 if skip else 25,
            "Requeues": 2,
            "DiscoveryRefreshWrites": 12 if skip else 27,
        }

    def test_validates_actual_writes_and_definition_reads(self):
        for arm in PENDING_ARMS:
            verify_pending_queue(self.run_receipt(arm), arm)
        for arm, field, value, error in (
            ("base", "PendingSkips", 1, "control exercised"),
            ("base-skip", "ClaimReads", 3, "once-per-page"),
            ("vector-skip", "DiscoveryRefreshWrites", 13, "do not reconcile"),
        ):
            with self.subTest(arm=arm, field=field):
                run = self.run_receipt(arm)
                run[field] = value
                with self.assertRaisesRegex(ValueError, error):
                    verify_pending_queue(run, arm)

    def test_only_a_committed_skip_can_qualify(self):
        control = [self.run_receipt("base")] * 4
        faster = [self.run_receipt("base-skip", 80) for _ in range(4)]
        self.assertTrue(compare_pending_queue(control, faster)["passes_screen"])
        faster[0]["PendingSkips"] = 0
        result = compare_pending_queue(control, faster)
        self.assertEqual(result["verdict"], "inconclusive")
        self.assertFalse(result["passes_screen"])

    def test_uses_either_paired_lookup_to_advance_and_current_to_qualify(self):
        current = compare_pending_queue([self.run_receipt("base")] * 4, [self.run_receipt("base-skip", 105)] * 4)
        vectors = compare_pending_queue([self.run_receipt("vector")] * 4, [self.run_receipt("vector-skip", 80)] * 4)
        both = {"skip_vs_current": current, "skip_vs_vectors": vectors}
        primary = {"hotpotqa": both, "musique": both}
        self.assertEqual(pending_decision(primary)["outcome"], "advance-to-remaining")
        complete = {**primary, "popqa": both, "nq_rear": both, "2wikimultihopqa": both}
        self.assertEqual(pending_decision(complete)["outcome"], "stored-vectors-dependency")
        for name in complete:
            complete[name] = {**both, "skip_vs_current": vectors}
        self.assertEqual(pending_decision(complete)["outcome"], "qualifies-for-production-approval")

    def test_requires_exact_order_and_drained_receipt(self):
        source = ("a" * 40, "51", "1")
        sha = "b" * 64
        schedules = scheduled_turns(PENDING_ARMS)
        witnesses = {arm: ("c" if arm.startswith("vector") else "b") * 64 for arm in PENDING_ARMS}
        runs = []
        for pair, order in enumerate(PENDING_ORDERS, 1):
            for arm in order:
                runs.append({
                    **self.run_receipt(arm, 80 if arm.endswith("-skip") else 100),
                    "Pair": pair, "Coverage": {"eligible": 10, "organized": 10, "pending": 0, "failed": 0, "ambiguous": 0},
                    **dict.fromkeys(("Paused", "DiscoveryComplete", "CoverageComplete", "Pages", "FullPages", "ProviderAttempts"), 1),
                    **dict.fromkeys(("ActiveWork", "LiveLeases", "RunningBatches", "UnreconciledAttempts", "RemainingMarkers"), 0),
                    "Quality": {"Canonical": sha}, "VectorInstall": {"Rows": int(arm.startswith("vector"))},
                    "LookupSHA": witnesses[arm], "LastPageSize": 100,
                })
        data = {
            "Protocol": "ontology-build-pending-refresh-v1", "Shape": {"Dataset": "hotpotqa"},
            "Source": source[0], "RunID": source[1], "RunAttempt": source[2], "Runner": "hosted",
            "PostgresVersion": "18.1", "TemplateSHA": sha, "CorrectionSHA": "sha256:" + sha,
            "Stage": "complete", "SeedSources": 10, "ScheduledInputs": schedules, "Runs": runs,
            "Plans": {arm: {"BindingsSHA": "sha256:" + sha, "QuerySHA": witnesses[arm], "Plan": {"Raw": [{}]}} for arm in PENDING_ARMS},
        }
        compare = lambda: compare_lookup_dataset(data, "hotpotqa", source, witnesses, pending=True)
        self.assertTrue(compare()["skip_vs_current"]["passes_screen"])
        runs[-1]["RunningBatches"] = 1
        with self.assertRaisesRegex(ValueError, "incomplete coverage or drain"):
            compare()
        runs[-1]["RunningBatches"] = 0
        runs[0], runs[1] = runs[1], runs[0]
        with self.assertRaisesRegex(ValueError, "incomplete or misordered repetitions"):
            compare()


if __name__ == "__main__":
    unittest.main()
