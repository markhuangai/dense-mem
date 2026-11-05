#!/usr/bin/env python3
"""Adverse checks for independent score aggregation using saved score records."""

import copy
import json
from pathlib import Path
import tempfile
import unittest

import verify_measurement_scores as verification


class MeasurementAggregatesTest(unittest.TestCase):
    def example(self):
        row = {"recall_at_k": 1, "mrr": 0.5, "ndcg_at_k": 0.6, "bad_at_k": 1,
               "first_required_rank": 2, "first_bad_rank": 1, "unmapped_source_refs": []}
        summary = {"case_count": 1, "scored_case_count": 1, "unmapped_source_refs": 0}
        for prefix in ("", "context_", "evidence_", "dream_"):
            active = prefix in ("", "evidence_")
            if prefix:
                row[prefix + "scored"] = active
                summary[prefix + "scored_case_count"] = int(active)
            for metric in (*verification.METRICS, "bad_at_k"):
                if prefix:
                    row[prefix + metric] = row[metric] if active else 0
                summary["average_" + prefix + metric] = row[prefix + metric] if active else 0
            for kind in ("required", "bad"):
                rank = row["first_" + kind + "_rank"] if active else 0
                row[prefix + "first_" + kind + "_rank"] = rank
                summary[prefix + kind + "_rank1_rate"] = int(rank == 1)
        return row, summary

    def test_all_quality_aggregates_and_denominators_are_checked(self):
        row, summary = self.example()
        verification.verify_aggregates([row], summary, "valid", rank_rates=True)
        for key in summary:
            changed = copy.deepcopy(summary)
            changed[key] += 1
            with self.subTest(field=key), self.assertRaises(ValueError):
                verification.verify_aggregates([row], changed, "corrupt", rank_rates=True)

    def retained_example(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        seed = root / "seed.json"
        seed.write_text(json.dumps({"corpus_file": "corpus.jsonl", "cases_file": "cases.jsonl", "qrels_file": "qrels.jsonl"}))
        (root / "corpus.jsonl").write_text("")
        (root / "qrels.jsonl").write_text("")
        cases = [{"case_id": f"case-{index}", "slices": ["seed-only", "seed-only", " "]} for index in range(204)]
        (root / "cases.jsonl").write_text("".join(json.dumps(row) + "\n" for row in cases))
        row, summary = self.example()
        summary["case_count"] = summary["scored_case_count"] = summary["evidence_scored_case_count"] = 204
        summary["seed_hash"], _ = verification.frozen_cases(seed)
        summary["slices"] = {axis: copy.deepcopy(summary) for axis in ("seed-only", "suite-only")}
        scores = [{**row, "case_id": case["case_id"]} for case in cases]
        suite = [{"case_id": case["case_id"], "slices": [" suite-only "]} for case in cases]
        original, replay = root / "original", root / "replay"
        for directory in (original, replay):
            directory.mkdir()
            (directory / "retrieval_scores.jsonl").write_text("".join(json.dumps(row) + "\n" for row in scores))
            (directory / "summary.json").write_text(json.dumps(summary))
        (replay / "suite.jsonl").write_text("".join(json.dumps(row) + "\n" for row in suite))
        return original, replay, seed

    def test_retained_scores_and_suite_require_the_frozen_cohort(self):
        for corruption in ("score_replacement", "missing_suite", "duplicate_suite", "unexpected_suite"):
            with self.subTest(corruption=corruption):
                original, replay, seed = self.retained_example()
                self.assertEqual(verification.retained_pair(original, replay, seed)["case_count"], 204)
                suite = verification.rows(replay / "suite.jsonl")
                if corruption == "score_replacement":
                    for directory in (original, replay):
                        scores = verification.rows(directory / "retrieval_scores.jsonl")
                        scores[-1]["case_id"] = "unexpected"
                        (directory / "retrieval_scores.jsonl").write_text("".join(json.dumps(row) + "\n" for row in scores))
                    suite.pop()
                elif corruption == "missing_suite":
                    suite.pop()
                elif corruption == "duplicate_suite":
                    suite[-1] = suite[0]
                else:
                    suite[-1]["case_id"] = "unexpected"
                (replay / "suite.jsonl").write_text("".join(json.dumps(row) + "\n" for row in suite))
                with self.assertRaisesRegex(ValueError, "frozen (scored|suite) cases"):
                    verification.retained_pair(original, replay, seed)

    def test_retained_summary_requires_every_expected_slice(self):
        for corruption in ("missing", "unexpected"):
            with self.subTest(corruption=corruption):
                original, replay, seed = self.retained_example()
                verification.retained_pair(original, replay, seed)
                summary = json.loads((replay / "summary.json").read_text())
                if corruption == "missing":
                    summary["slices"].pop("seed-only")
                else:
                    summary["slices"]["unexpected"] = summary["slices"]["seed-only"]
                (replay / "summary.json").write_text(json.dumps(summary))
                with self.assertRaisesRegex(ValueError, "summary slices"):
                    verification.retained_pair(original, replay, seed)


if __name__ == "__main__":
    unittest.main()
