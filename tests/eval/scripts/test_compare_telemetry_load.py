import unittest

import compare_telemetry_load


class CompareTelemetryLoadTests(unittest.TestCase):
    def test_request_tokens_allow_isolated_deployment_credentials(self):
        tokens = compare_telemetry_load.request_tokens({
            "DENSE_MEM_BASELINE_LOAD_TOKEN": "baseline-key",
            "DENSE_MEM_CANDIDATE_LOAD_TOKEN": "candidate-key",
        })

        self.assertEqual("baseline-key", tokens["baseline"])
        self.assertEqual("candidate-key", tokens["candidate"])

    def test_request_tokens_keep_shared_token_fallback(self):
        tokens = compare_telemetry_load.request_tokens({"DENSE_MEM_LOAD_TOKEN": "shared-key"})

        self.assertEqual({"baseline": "shared-key", "candidate": "shared-key"}, tokens)

    def test_request_tokens_require_both_isolated_credentials(self):
        with self.assertRaisesRegex(ValueError, "both arm-specific load tokens"):
            compare_telemetry_load.request_tokens({"DENSE_MEM_BASELINE_LOAD_TOKEN": "baseline-key"})

    def test_throughput_gate_uses_each_arm_completion_time(self):
        count = 400
        samples = {
            "baseline": [0.01] * count,
            "candidate": [0.01] * count,
        }

        comparison = compare_telemetry_load.compare_arm_performance(
            samples,
            {"baseline": 4.0, "candidate": 5.0},
        )

        self.assertAlmostEqual(100.0, comparison["arms"]["baseline"]["throughput_requests_per_second"])
        self.assertAlmostEqual(80.0, comparison["arms"]["candidate"]["throughput_requests_per_second"])
        self.assertAlmostEqual(20.0, comparison["candidate_throughput_loss_percent"])
        self.assertTrue(comparison["p95_pass"])
        self.assertFalse(comparison["throughput_pass"])


if __name__ == "__main__":
    unittest.main()
