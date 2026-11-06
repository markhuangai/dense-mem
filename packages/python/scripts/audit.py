import argparse
import json
import re
import subprocess
import sys
from datetime import UTC, date, datetime
from pathlib import Path


def load_policy(path: Path) -> list[dict]:
    policy = json.loads(path.read_text())
    if (
        not isinstance(policy, dict)
        or type(policy.get("schema_version")) is not int
        or policy["schema_version"] != 1
        or not isinstance(policy.get("go"), list)
        or not isinstance(policy.get("python"), list)
    ):
        raise ValueError("Expected schema_version 1 with go and python arrays")
    for rule in policy["python"]:
        if (
            not isinstance(rule, dict)
            or any(
                not isinstance(rule.get(key), str) or not rule[key].strip()
                for key in ("package", "version", "expires", "reason", "reference")
            )
            or not isinstance(rule.get("advisories"), list)
            or not rule["advisories"]
            or any(not isinstance(item, str) or not item.strip() for item in rule["advisories"])
            or not isinstance(rule.get("requires"), dict)
            or any(
                not isinstance(rule["requires"].get(key), str) or not rule["requires"][key].strip()
                for key in ("package", "version")
            )
        ):
            raise ValueError("Invalid Python dependency exception")
        if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", rule["expires"]):
            raise ValueError("Exception expiry must be YYYY-MM-DD")
        date.fromisoformat(rule["expires"])
    return policy["python"]


def audit_status(
    report: object, status: int, today: date, exceptions: list[dict]
) -> tuple[int, list[str]]:
    if status not in (0, 1):
        return status, [f"ERROR: reason=scanner_execution_failed scanner_status={status}"]
    dependencies = report.get("dependencies") if isinstance(report, dict) else None
    if not isinstance(dependencies, list) or not dependencies:
        raise ValueError("Invalid pip-audit dependency report")
    packages, findings = set(), []
    for dependency in dependencies:
        if (
            not isinstance(dependency, dict)
            or "skip_reason" in dependency
            or not isinstance(dependency.get("name"), str)
            or not dependency["name"]
            or not isinstance(dependency.get("version"), str)
            or not dependency["version"]
            or not isinstance(dependency.get("vulns"), list)
        ):
            raise ValueError("Invalid pip-audit dependency")
        package = (dependency["name"], dependency["version"])
        packages.add(package)
        for vulnerability in dependency["vulns"]:
            advisory = vulnerability.get("id") if isinstance(vulnerability, dict) else None
            aliases = vulnerability.get("aliases", []) if isinstance(vulnerability, dict) else None
            if (
                not isinstance(advisory, str)
                or not advisory
                or not isinstance(aliases, list)
                or any(not isinstance(alias, str) or not alias for alias in aliases)
            ):
                raise ValueError("Invalid pip-audit vulnerability")
            findings.append((package, advisory, {advisory, *aliases}))
    if not findings:
        return status, [f"reason=no_findings scanner_status={status}"]
    approved, messages = True, []
    for package, advisory, identities in findings:
        candidates = [rule for rule in exceptions if identities.intersection(rule["advisories"])]
        exact = [rule for rule in candidates if package == (rule["package"], rule["version"])]
        environment = [
            rule for rule in exact
            if (rule["requires"]["package"], rule["requires"]["version"]) in packages
        ]
        active = [rule for rule in environment if today < date.fromisoformat(rule["expires"])]
        if status == 0:
            reason = "scanner_status_inconsistent"
        elif not candidates:
            reason = "unapproved_advisory"
        elif not exact:
            reason = "dependency_mismatch"
        elif not environment:
            reason = "required_dependency_missing"
        elif not active:
            reason = "exception_expired"
        else:
            reason = "accepted_exception"
        accepted = reason == "accepted_exception"
        approved &= accepted
        messages.append(
            f"{'WARNING' if accepted else 'ERROR'}: advisory={advisory} "
            f"dependency={package[0]}=={package[1]} reason={reason} scanner_status={status}"
        )
    return (0 if approved else 1), messages


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("requirements", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    policy_path = Path(__file__).resolve().parents[3] / "dependency-exceptions.json"
    try:
        exceptions = load_policy(policy_path)
    except (OSError, ValueError) as error:
        print(f"ERROR: policy={policy_path} reason=invalid_policy: {error}", file=sys.stderr)
        return 1
    print(f"policy={policy_path} scanner=pip-audit report={args.output}", file=sys.stderr)
    for rule in exceptions:
        print(
            f"advisories={','.join(rule['advisories'])} "
            f"dependency={rule['package']}=={rule['version']} expires={rule['expires']} "
            f"requires={rule['requires']['package']}=={rule['requires']['version']} "
            f"reason={rule['reason']} reference={rule['reference']}", file=sys.stderr,
        )
    result = subprocess.run(
        [
            sys.executable, "-m", "pip_audit", "-r", str(args.requirements),
            "--no-deps", "--disable-pip", "--strict", "--format", "json",
        ],
        stdout=subprocess.PIPE,
        text=True,
        check=False,
    )
    args.output.write_text(result.stdout)
    sys.stdout.write(result.stdout)
    if result.returncode not in (0, 1):
        print(
            f"ERROR: reason=scanner_execution_failed scanner_status={result.returncode} "
            f"report={args.output}", file=sys.stderr,
        )
        return result.returncode
    try:
        status, messages = audit_status(
            json.loads(result.stdout), result.returncode, datetime.now(UTC).date(), exceptions
        )
    except ValueError as error:
        print(
            f"ERROR: pip-audit report rejected: {error}; scanner_status={result.returncode} "
            f"report={args.output}", file=sys.stderr,
        )
        return 1
    for message in messages:
        print(f"{message} report={args.output}", file=sys.stderr)
    return status


if __name__ == "__main__":
    raise SystemExit(main())
