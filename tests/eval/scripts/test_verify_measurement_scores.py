#!/usr/bin/env python3
"""Adverse checks for independent score aggregation using saved score records."""

import copy
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


if __name__ == "__main__":
    unittest.main()
