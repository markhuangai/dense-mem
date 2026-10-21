"""Verify the installed ADK adapter against the frozen session input corpus."""

import hashlib
import json
import sys
from pathlib import Path

from google.adk.events import Event
from google.genai import types

from densemem.adk import _session_requests


def main():
    source, output = map(Path, sys.argv[1:])
    raw = source.read_bytes()
    cohort = json.loads(raw)
    root = Path(__file__).resolve().parents[3]
    lock = json.loads(
        (root / "tests/eval/source_locks/session_ingest_v1.json").read_text()
    )
    generator = root / "tests/eval/scripts/generate_session_ingest_v1.py"
    # The accepted #574 change added Remember targets without changing the 32 quality cases.
    assert hashlib.sha256(generator.read_bytes()).hexdigest() == (
        "c130187c2413710a02a243ccb2a1962c20b1e5fe6aacd31a115073266b4c1833"
    )
    assert len(cohort["quality_cases"]) == lock["quality_cases"] == 32
    calls = 0
    for case in cohort["quality_cases"]:
        for batch in case["calls"]:
            events = [
                Event(
                    id=e["event_id"],
                    author="user",
                    timestamp=1720000000,
                    content=types.Content(
                        role="user", parts=[types.Part(text=e["text"])]
                    ),
                )
                for e in batch
            ]
            events.extend(
                [
                    Event(
                        author="assistant",
                        content=types.Content(parts=[types.Part(text="assistant")]),
                    ),
                    Event(
                        author="user",
                        content=types.Content(
                            parts=[types.Part(text="hidden", thought=True)]
                        ),
                    ),
                    Event(
                        author="user",
                        content=types.Content(
                            parts=[
                                types.Part(
                                    inline_data=types.Blob(
                                        data=b"binary", mime_type="image/png"
                                    )
                                )
                            ]
                        ),
                    ),
                ]
            )
            requests = _session_requests(
                "session_ingest_v1", "external-user", case["id"], events
            )
            assert [
                {"event_id": e["event_id"], "text": e["text"]}
                for request in requests
                for e in request["events"]
            ] == batch
            assert requests == _session_requests(
                "session_ingest_v1", "external-user", case["id"], events
            )
            calls += 1
    report = {
        "cohort": cohort["cohort"],
        "cohort_sha256": hashlib.sha256(raw).hexdigest(),
        "quality_cases": 32,
        "calls": calls,
        "exact_mapping": True,
        "passed": True,
    }
    output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report))


if __name__ == "__main__":
    main()
