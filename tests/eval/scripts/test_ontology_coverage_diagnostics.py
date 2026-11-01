import copy
import importlib.util
from pathlib import Path
import unittest


SPEC = importlib.util.spec_from_file_location("diagnostics", Path(__file__).with_name("ontology_coverage_diagnostics.py"))
diagnostics = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(diagnostics)


def source(identifier, status, reason, assessment):
    return {"kind": "evidence", "id": identifier, "version": 1, "fingerprint": "sha256:" + identifier,
            "status": status, "reason": reason, "assessment_id": assessment}


def assessment(identifier, sources, code="", validation="", created="2026-01-01T00:00:00Z"):
    return {"assessment_id": identifier, "created_at": created, "body": {
        "id": identifier, "sources": [{key: item[key] for key in ("kind", "id", "version", "fingerprint")} for item in sources],
        "result": {"failure_code": code, "attempts": [{"validation": {"code": validation}}],
                   "outcomes": [{"source": {key: item[key] for key in ("kind", "id", "version")},
                                 "status": item["status"], "reason": item["reason"]} for item in sources]},
    }}


class OntologyCoverageDiagnosticsTests(unittest.TestCase):
    def test_repeated_and_recovered_sources_are_not_terminal_failures(self):
        failed = source("a", "failed", "provider_response_invalid", "first")
        recovered = source("b", "organized", "", "last")
        old = source("b", "failed", "provider_response_invalid", "first")
        rows = [assessment("first", [failed, old], "provider_response_invalid", "item_locked_classification"),
                assessment("again", [old], "provider_response_invalid", "request_id_mismatch"),
                assessment("last", [recovered])]
        result = diagnostics.reconcile(rows, [failed, recovered])
        self.assertEqual(result["unresolved_sources"], 1)
        self.assertEqual(result["invalid_assessment_source_appearances"], 3)
        self.assertEqual(result["invalid_assessment_unique_sources"], 2)
        self.assertEqual(result["invalid_assessment_repeated_appearances"], 1)
        self.assertEqual(result["current_dispositions_of_invalid_sources"], {"failed:provider_response_invalid": 1, "organized:": 1})
        self.assertEqual(result["terminal_invalid_validation_codes"], {"item_locked_classification": 1})

    def test_missing_changed_and_duplicate_lineage_are_visible(self):
        terminal = source("a", "failed", "provider_response_invalid", "first")
        rows = [assessment("first", [terminal], "provider_response_invalid", "request_id_mismatch")]
        variants = [dict(terminal, assessment_id="missing"), dict(terminal, fingerprint="changed"), dict(terminal, reason="stale_input")]
        for changed in variants:
            with self.assertRaises(ValueError):
                diagnostics.reconcile(rows, [changed])
        with self.assertRaises(ValueError):
            diagnostics.reconcile(rows, [terminal, terminal])
        with self.assertRaises(ValueError):
            diagnostics.reconcile(rows + rows, [terminal])

    def test_selection_uses_source_count_then_time_then_identity(self):
        rows = []
        for family in diagnostics.FAMILIES:
            item = source("a", "ambiguous" if family == "classification_ambiguous" else "failed", family, family)
            rows.append(assessment(family, [item], family, family))
        larger = copy.deepcopy(rows[0])
        larger["assessment_id"] = larger["body"]["id"] = "larger"
        larger["body"]["sources"] *= 2
        earlier = copy.deepcopy(rows[0])
        earlier["assessment_id"] = earlier["body"]["id"] = "earlier"
        earlier["created_at"] = "2025-01-01T00:00:00Z"
        tied = copy.deepcopy(earlier)
        tied["assessment_id"] = tied["body"]["id"] = "a-tie"
        selected = diagnostics.select_witnesses([larger, *reversed(rows), earlier, tied])
        self.assertEqual([item["family"] for item in selected], list(diagnostics.FAMILIES))
        self.assertEqual(selected[0]["original_assessment_id"], "a-tie")
        self.assertTrue(all(not item["historical_context_available"] for item in selected))
        with self.assertRaisesRegex(ValueError, "no retained assessment"):
            diagnostics.select_witnesses([])


if __name__ == "__main__":
    unittest.main()
