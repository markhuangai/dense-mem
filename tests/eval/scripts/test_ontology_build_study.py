import copy
import unittest

from ontology_build_study import (
    LOOKUP_ARMS,
    compare_lookup_dataset,
    paired_lookup_savings,
    verify_lookup_schedule,
)


def scheduled_turns():
    turn = {
        "PageLimit": 100,
        "Sources": ["source-a", "source-b"],
        "Candidates": ["first-topic", "second-topic"],
        "HTTPBodies": ['{"model":"synthetic","messages":[]}'],
    }
    return {arm: [copy.deepcopy(turn) for _ in range(3)] for arm in LOOKUP_ARMS}


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


if __name__ == "__main__":
    unittest.main()
