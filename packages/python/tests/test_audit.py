import copy
import importlib.util
from datetime import date, timedelta
from pathlib import Path

import pytest

spec = importlib.util.spec_from_file_location(
    "audit_policy", Path(__file__).parents[1] / "scripts" / "audit.py"
)
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)


@pytest.fixture
def report():
    return {
        "dependencies": [
            {"name": "google-adk", "version": "2.7.0", "vulns": []},
            {
                "name": "opentelemetry-api",
                "version": "1.43.0",
                "vulns": [{"id": "CVE-2026-114603", "aliases": ["GHSA-xcq7-p3pf-p88h"]}],
            },
        ],
        "fixes": [],
    }


def test_known_advisory_is_accepted_only_before_expiry(report):
    assert audit.audit_status(report, 1, audit.EXPIRES - timedelta(days=1)) == 0
    assert audit.audit_status(report, 1, audit.EXPIRES) == 1
    assert audit.audit_status(report, 1, audit.EXPIRES + timedelta(days=1)) == 1


def test_advisory_alias_is_recognized(report):
    report["dependencies"][1]["vulns"] = [{"id": "GHSA-xcq7-p3pf-p88h"}]
    assert audit.audit_status(report, 1, date(2026, 10, 22)) == 0


@pytest.mark.parametrize(
    "package,version", [("opentelemetry-api", "1.44.0"), ("another-package", "1.43.0")]
)
def test_exception_does_not_cover_another_package_or_version(report, package, version):
    report["dependencies"][1].update(name=package, version=version)
    assert audit.audit_status(report, 1, date(2026, 10, 22)) == 1


@pytest.mark.parametrize("version", [None, "2.8.0"])
def test_exception_requires_the_pinned_adk_environment(report, version):
    if version is None:
        report["dependencies"].pop(0)
    else:
        report["dependencies"][0]["version"] = version
    assert audit.audit_status(report, 1, date(2026, 10, 22)) == 1


@pytest.mark.parametrize("same_package", [True, False])
def test_additional_advisories_remain_blocking(report, same_package):
    finding = {"id": "CVE-2099-0001", "aliases": []}
    if same_package:
        report["dependencies"][1]["vulns"].append(finding)
    else:
        report["dependencies"].append(
            {"name": "another-package", "version": "1.0", "vulns": [finding]}
        )
    assert audit.audit_status(report, 1, date(2026, 10, 22)) == 1


@pytest.mark.parametrize("status", [2, 7, -9])
def test_scanner_execution_failures_remain_blocking(report, status):
    assert audit.audit_status(report, status, date(2026, 10, 22)) == status


def test_failed_scan_without_findings_is_not_waived(report):
    report["dependencies"][1]["vulns"] = []
    assert audit.audit_status(report, 1, date(2026, 10, 22)) == 1
    assert audit.audit_status(report, 0, audit.EXPIRES) == 0


def test_success_status_cannot_hide_findings(report):
    assert audit.audit_status(report, 0, date(2026, 10, 22)) == 1


@pytest.mark.parametrize(
    "invalid",
    [None, {}, {"dependencies": []}, {"dependencies": [None]}, {"dependencies": "invalid"}],
)
def test_incomplete_reports_are_rejected(invalid):
    with pytest.raises(ValueError, match="Invalid pip-audit"):
        audit.audit_status(invalid, 1, date(2026, 10, 22))


@pytest.mark.parametrize(
    "invalid",
    [
        {"skip_reason": "dependency could not be audited"},
        {"name": ""},
        {"version": None},
        {"version": ""},
        {"vulns": None},
        {"vulns": [{}]},
        {"vulns": [{"id": "CVE-2026-114603", "aliases": "GHSA-xcq7-p3pf-p88h"}]},
    ],
)
def test_skipped_or_malformed_dependencies_cannot_be_waived(report, invalid):
    report["dependencies"][1].update(invalid)
    with pytest.raises(ValueError, match="Invalid pip-audit"):
        audit.audit_status(report, 1, date(2026, 10, 22))


def test_original_report_is_not_filtered(report):
    original = copy.deepcopy(report)
    assert audit.audit_status(report, 1, date(2026, 10, 22)) == 0
    assert report == original
