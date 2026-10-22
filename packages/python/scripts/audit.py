import argparse
import json
import subprocess
import sys
from datetime import UTC, date, datetime
from pathlib import Path

EXPIRES = date(2026, 11, 21)
ADVISORIES = {"CVE-2026-114603", "GHSA-xcq7-p3pf-p88h"}


def audit_status(report: object, status: int, today: date) -> int:
    if status not in (0, 1):
        return status
    dependencies = report.get("dependencies") if isinstance(report, dict) else None
    if not isinstance(dependencies, list) or not dependencies:
        raise ValueError("Invalid pip-audit dependency report")
    approved = today < EXPIRES
    found = has_adk = False
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
        has_adk |= package == ("google-adk", "2.7.0")
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
            found = True
            approved &= package == ("opentelemetry-api", "1.43.0") and bool(
                ADVISORIES.intersection([advisory, *aliases])
            )
    if status == 1 and found and approved and has_adk:
        return 0
    return 1 if found else status


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("requirements", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
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
    if result.returncode not in (0, 1):
        return result.returncode
    today = datetime.now(UTC).date()
    try:
        status = audit_status(json.loads(result.stdout), result.returncode, today)
    except ValueError as error:
        print(f"pip-audit report rejected: {error}", file=sys.stderr)
        return 1
    if result.returncode == 1 and status == 0:
        print(
            f"WARNING: CVE-2026-114603 in opentelemetry-api==1.43.0 is accepted until "
            f"{EXPIRES} 00:00 UTC: google-adk==2.7.0 excludes patched OpenTelemetry 1.45.1. "
            f"The complete finding remains in {args.output}.",
            file=sys.stderr,
        )
    elif today >= EXPIRES:
        print(
            f"ADK audit exception expired on {EXPIRES} UTC; findings are blocking.", file=sys.stderr
        )
    return status


if __name__ == "__main__":
    raise SystemExit(main())
