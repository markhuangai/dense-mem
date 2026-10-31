import copy
import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest


SPEC = importlib.util.spec_from_file_location("comparison", Path(__file__).with_name("compare_ontology_recall_pairs.py"))
comparison = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(comparison)


def output(adjustment=0, count=200):
    return "\n".join(
        f"BenchmarkRecallOntology/{workload}/{mode}-8 {count} 100 ns/op {1000 + adjustment} p95-ns/op"
        for workload in comparison.WORKLOADS for mode in comparison.MODES
    )


class RecallPairComparisonTests(unittest.TestCase):
    def protocol(self, directory, adjustments):
        protocol = {"sources": {"base": {"fingerprint": "a" * 64}, "candidate": {"fingerprint": "b" * 64}}, "pairs": []}
        for number, adjustment in enumerate(adjustments, 1):
            pair = {"pair": number, "order": ["base", "candidate"] if number % 2 else ["candidate", "base"], "runs": {}}
            for arm in pair["order"]:
                data = output(adjustment if arm == "candidate" else 0).encode()
                name = f"{number}-{arm}.log"
                (directory / name).write_bytes(data)
                pair["runs"][arm] = {"status": "passed", "exit_code": 0, "source_fingerprint": protocol["sources"][arm]["fingerprint"],
                                      "log": name, "log_sha256": hashlib.sha256(data).hexdigest()}
            protocol["pairs"].append(pair)
        return protocol

    def test_scores_all_five_pairs_and_reports_controls(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            protocol = self.protocol(directory, [0, 50, 100, 150, 200])
            result = comparison.compare(protocol, directory)
            self.assertEqual(set(result), set(comparison.WORKLOADS))
            self.assertAlmostEqual(result["discovery"]["enabled"]["median_paired_p95_regression"], 0.10)
            self.assertTrue(result["discovery"]["passed"], "exactly 10% remains within the bound")
            failing = self.protocol(directory, [101] * 5)
            self.assertFalse(comparison.compare(failing, directory)["discovery"]["passed"])
            self.assertEqual(len(result["grouping"]["disabled_control"]["paired_regressions"]), 5)
            protocol = self.protocol(directory, [0, 10, 20, 30, 40])
            self.assertTrue(all(result["passed"] for result in comparison.compare(protocol, directory).values()))

    def test_rejects_missing_failed_reordered_or_tampered_arms(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            protocol = self.protocol(directory, [0] * 5)
            variants = []
            missing = copy.deepcopy(protocol)
            missing["pairs"].pop()
            variants.append(missing)
            failed = copy.deepcopy(protocol)
            failed["pairs"][0]["runs"]["candidate"]["status"] = "failed"
            variants.append(failed)
            changed = copy.deepcopy(protocol)
            changed["pairs"][0]["runs"]["candidate"]["source_fingerprint"] = "c" * 64
            variants.append(changed)
            reordered = copy.deepcopy(protocol)
            reordered["pairs"][1]["order"] = ["base", "candidate"]
            variants.append(reordered)
            for variant in variants:
                with self.assertRaises(ValueError):
                    comparison.compare(variant, directory)
            (directory / "1-base.log").write_text(output(1))
            with self.assertRaisesRegex(ValueError, "log changed"):
                comparison.compare(protocol, directory)

    def test_rejects_incomplete_duplicate_wrong_size_and_invalid_samples(self):
        for invalid in [output().splitlines()[0], output() + "\n" + output(), output(count=199), output().replace("1000 p95", "nan p95")]:
            with self.assertRaises(ValueError):
                comparison.parse_sample(invalid)


if __name__ == "__main__":
    unittest.main()
