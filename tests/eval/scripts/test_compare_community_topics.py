import unittest

from compare_community_topics import compare


class CommunityLatencyComparisonTest(unittest.TestCase):
    def samples(self, legacy, ontology, count=200):
        return "\n".join(f"BenchmarkCommunityTopicProjection/{mode}-4 {count} 1 ns/op {value} p95-ns/op" for before, after in zip(legacy, ontology) for mode, value in [("legacy", before), ("ontology", after)])

    def test_pairs_use_median_regression_and_enforce_limit(self):
        result = compare(self.samples([100, 200, 400, 800, 1600], [105, 210, 420, 1600, 1680]))
        self.assertTrue(result["passed"])
        self.assertAlmostEqual(result["median_paired_p95_regression"], 0.05)
        self.assertFalse(compare(self.samples([100]*5, [111]*5))["passed"])

    def test_incomplete_and_wrong_sized_measurements_fail(self):
        for text in [self.samples([100]*4, [100]*4), self.samples([100]*5, [100]*5, 201), self.samples([100]*5, [float("nan")]*5)]:
            with self.assertRaises(ValueError):
                compare(text)


if __name__ == "__main__":
    unittest.main()
