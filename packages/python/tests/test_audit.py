import copy
import importlib.util
import json
import os
import shutil
import subprocess
import sys
from datetime import date, timedelta
from pathlib import Path

import pytest

spec = importlib.util.spec_from_file_location(
    "audit_policy", Path(__file__).parents[1] / "scripts" / "audit.py"
)
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)


EXPIRY = date(2026, 11, 21)


@pytest.fixture
def policy_document():
    return {
        "schema_version": 1, "go": [],
        "python": [{
            "advisories": ["CVE-2026-114603", "GHSA-xcq7-p3pf-p88h"],
            "package": "opentelemetry-api", "version": "1.43.0",
            "requires": {"package": "google-adk", "version": "2.7.0"},
            "expires": "2026-11-21", "reason": "Pinned ADK excludes the patched dependency",
            "reference": "https://github.com/markhuangai/dense-mem/pull/612",
        }],
    }


@pytest.fixture
def policy_path(tmp_path, policy_document):
    path = tmp_path / "dependency-exceptions.json"
    path.write_text(json.dumps(policy_document))
    return path


@pytest.fixture
def policy(policy_path):
    return audit.load_policy(policy_path)


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


def test_known_advisory_is_accepted_only_before_expiry(report, policy):
    assert audit.audit_status(report, 1, EXPIRY - timedelta(days=1), policy)[0] == 0
    assert audit.audit_status(report, 1, EXPIRY, policy)[0] == 1
    assert audit.audit_status(report, 1, EXPIRY + timedelta(days=1), policy)[0] == 1


def test_advisory_alias_is_recognized(report, policy):
    report["dependencies"][1]["vulns"] = [{"id": "GHSA-xcq7-p3pf-p88h"}]
    assert audit.audit_status(report, 1, date(2026, 10, 22), policy)[0] == 0


@pytest.mark.parametrize(
    "package,version", [("opentelemetry-api", "1.44.0"), ("another-package", "1.43.0")]
)
def test_exception_does_not_cover_another_package_or_version(report, package, version, policy):
    report["dependencies"][1].update(name=package, version=version)
    assert audit.audit_status(report, 1, date(2026, 10, 22), policy)[0] == 1


@pytest.mark.parametrize("version", [None, "2.8.0"])
def test_exception_requires_the_pinned_adk_environment(report, version, policy):
    if version is None:
        report["dependencies"].pop(0)
    else:
        report["dependencies"][0]["version"] = version
    assert audit.audit_status(report, 1, date(2026, 10, 22), policy)[0] == 1


@pytest.mark.parametrize("same_package", [True, False])
def test_additional_advisories_remain_blocking(report, same_package, policy):
    finding = {"id": "CVE-2099-0001", "aliases": []}
    if same_package:
        report["dependencies"][1]["vulns"].append(finding)
    else:
        report["dependencies"].append(
            {"name": "another-package", "version": "1.0", "vulns": [finding]}
        )
    assert audit.audit_status(report, 1, date(2026, 10, 22), policy)[0] == 1


@pytest.mark.parametrize("status", [2, 7, -9])
def test_scanner_execution_failures_remain_blocking(report, status, policy):
    assert audit.audit_status(report, status, date(2026, 10, 22), policy)[0] == status


def test_failed_scan_without_findings_is_not_waived(report, policy):
    report["dependencies"][1]["vulns"] = []
    assert audit.audit_status(report, 1, date(2026, 10, 22), policy)[0] == 1
    assert audit.audit_status(report, 0, EXPIRY, policy)[0] == 0


def test_success_status_cannot_hide_findings(report, policy):
    assert audit.audit_status(report, 0, date(2026, 10, 22), policy)[0] == 1


@pytest.mark.parametrize(
    "invalid",
    [None, {}, {"dependencies": []}, {"dependencies": [None]}, {"dependencies": "invalid"}],
)
def test_incomplete_reports_are_rejected(invalid, policy):
    with pytest.raises(ValueError, match="Invalid pip-audit"):
        audit.audit_status(invalid, 1, date(2026, 10, 22), policy)[0]


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
def test_skipped_or_malformed_dependencies_cannot_be_waived(report, invalid, policy):
    report["dependencies"][1].update(invalid)
    with pytest.raises(ValueError, match="Invalid pip-audit"):
        audit.audit_status(report, 1, date(2026, 10, 22), policy)[0]


def test_original_report_is_not_filtered(report, policy):
    original = copy.deepcopy(report)
    assert audit.audit_status(report, 1, date(2026, 10, 22), policy)[0] == 0
    assert report == original



def test_empty_python_section_grants_no_exception(report, policy_document, policy_path):
    policy_document["python"] = []
    policy_path.write_text(json.dumps(policy_document))
    policy = audit.load_policy(policy_path)
    status, messages = audit.audit_status(report, 1, date(2026, 10, 22), policy)
    assert status == 1
    assert "reason=unapproved_advisory" in messages[0]


def test_policy_file_values_select_accepted_advisories(report, policy_document, policy_path):
    policy_document["python"][0]["advisories"] = ["CVE-2099-0001"]
    policy_path.write_text(json.dumps(policy_document))
    policy = audit.load_policy(policy_path)
    assert audit.audit_status(report, 1, date(2026, 10, 22), policy)[0] == 1
    report["dependencies"][1]["vulns"] = [{"id": "CVE-2099-0001"}]
    assert audit.audit_status(report, 1, date(2026, 10, 22), policy)[0] == 0


@pytest.mark.parametrize("change", [
    {"version": ""}, {"version": None}, {"expires": ""},
    {"expires": "20261121"}, {"expires": "2026-11-31"}, {"expires": None},
    {"reason": " "}, {"reference": None}, {"advisories": []}, {"advisories": "CVE-2099-1"},
    {"advisories": [None]}, {"requires": None}, {"requires": {}},
    {"requires": {"package": "google-adk", "version": ""}},
])
def test_invalid_exception_entries_fail_closed(policy_document, policy_path, change):
    policy_document["python"][0].update(change)
    policy_path.write_text(json.dumps(policy_document))
    with pytest.raises(ValueError):
        audit.load_policy(policy_path)


@pytest.mark.parametrize("document", [
    None, {}, {"schema_version": 2, "go": [], "python": []},
    {"schema_version": True, "go": [], "python": []},
    {"schema_version": 1, "go": [], "python": None},
    {"schema_version": 1, "python": []},
    {"schema_version": 1, "go": [], "python": [None]},
])
def test_invalid_policy_structure_is_rejected(policy_path, document):
    policy_path.write_text(json.dumps(document))
    with pytest.raises(ValueError):
        audit.load_policy(policy_path)


def run_cli(tmp_path, policy_document, report, scanner_status=1):
    root = tmp_path / "repository"
    script = root / "packages" / "python" / "scripts" / "audit.py"
    script.parent.mkdir(parents=True)
    shutil.copyfile(audit.__file__, script)
    if policy_document is not None:
        (root / "dependency-exceptions.json").write_text(
            policy_document if isinstance(policy_document, str) else json.dumps(policy_document)
        )
    wire = json.dumps(report)
    (root / "pip_audit.py").write_text(
        f"import sys\nsys.stdout.write({wire!r})\nraise SystemExit({scanner_status})\n"
    )
    requirements, output = root / "requirements.txt", root / "audit.json"
    requirements.write_text("opentelemetry-api==1.43.0\n")
    output.write_text("cached successful report")
    outside = tmp_path / "outside"
    outside.mkdir()
    result = subprocess.run(
        [sys.executable, str(script), str(requirements), str(output)], cwd=outside,
        env={**os.environ, "PYTHONPATH": str(root), "DENSEMEM_TOKEN": "fixture-secret-marker"},
        capture_output=True, text=True, check=False,
    )
    return result, output, wire


def test_cli_resolves_policy_outside_repo_and_retains_complete_report(
    tmp_path, policy_document, report
):
    policy_document["python"][0]["expires"] = "2099-01-01"
    result, output, wire = run_cli(tmp_path, policy_document, report)
    assert result.returncode == 0, result.stderr
    assert output.read_text() == result.stdout == wire
    for required in ("dependency-exceptions.json", "advisory=CVE-2026-114603",
                     "dependency=opentelemetry-api==1.43.0", "expires=2099-01-01",
                     "requires=google-adk==2.7.0", "reason=accepted_exception",
                     "scanner_status=1", str(output)):
        assert required in result.stderr
    assert "fixture-secret-marker" not in result.stdout + result.stderr


def test_cli_diagnoses_unapproved_findings_from_complete_logs(tmp_path, policy_document, report):
    policy_document["python"][0]["expires"] = "2099-01-01"
    report["dependencies"][1]["vulns"].append({"id": "CVE-2099-0001"})
    result, output, wire = run_cli(tmp_path, policy_document, report)
    assert result.returncode == 1
    assert output.read_text() == result.stdout == wire
    assert "advisory=CVE-2099-0001" in result.stderr
    assert "reason=unapproved_advisory scanner_status=1" in result.stderr


def test_cli_scanner_failure_cannot_reuse_a_cached_report(tmp_path, policy_document, report):
    result, output, wire = run_cli(tmp_path, policy_document, report, 7)
    assert result.returncode == 7
    assert output.read_text() == result.stdout == wire
    assert "reason=scanner_execution_failed scanner_status=7" in result.stderr


@pytest.mark.parametrize("document", [None, "{", {"schema_version": 2}])
def test_cli_rejects_missing_or_invalid_policy(tmp_path, report, document):
    result, _, _ = run_cli(tmp_path, document, report)
    assert result.returncode == 1
    assert "dependency-exceptions.json" in result.stderr
    assert "reason=invalid_policy" in result.stderr


def test_expired_exception_has_an_explicit_diagnostic_reason(report, policy):
    status, messages = audit.audit_status(report, 1, EXPIRY, policy)
    assert status == 1
    assert "reason=exception_expired" in messages[0]


def test_missing_adk_has_an_explicit_diagnostic_reason(report, policy):
    report["dependencies"].pop(0)
    status, messages = audit.audit_status(report, 1, date(2026, 10, 22), policy)
    assert status == 1
    assert "reason=required_dependency_missing" in messages[0]


@pytest.mark.parametrize("field", ["package", "expires", "reference", "requires"])
def test_missing_exception_fields_fail_closed(policy_document, policy_path, field):
    del policy_document["python"][0][field]
    policy_path.write_text(json.dumps(policy_document))
    with pytest.raises(ValueError):
        audit.load_policy(policy_path)
