import hashlib
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from types import SimpleNamespace

import pytest
from test_release import release


@pytest.fixture
def publication(tmp_path, monkeypatch):
    receipt = {"version": "2.6.6rc10", "source_sha": "a" * 40}
    files = []
    for name in sorted(release.artifact_names(receipt["version"])):
        (tmp_path / name).write_bytes(name.encode())
        files.append(
            {"filename": name, "digests": {"sha256": hashlib.sha256(name.encode()).hexdigest()}}
        )
    state = {"responses": [], "calls": 0}
    release_response = threading.Event()

    class Registry(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_GET(self):
            state["calls"] += 1
            if state["calls"] == state.get("hold_call"):
                release_response.wait()
                return
            status, body = state["responses"][min(state["calls"] - 1, len(state["responses"]) - 1)]
            self.send_response(status)
            self.end_headers()
            self.wfile.write(json.dumps(body).encode())

    with ThreadingHTTPServer(("127.0.0.1", 0), Registry) as server:
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        monkeypatch.setattr(release, "PYPI_URL", f"http://127.0.0.1:{server.server_port}/densemem")
        monkeypatch.setattr(release, "PUBLICATION_WAIT_SECONDS", 2)
        monkeypatch.setattr(release, "PUBLICATION_POLL_SECONDS", 0.01)
        data = {
            "info": {"project_urls": {"Source": release.SOURCE_URL + receipt["source_sha"]}},
            "urls": files,
        }
        try:
            yield receipt, tmp_path, state, data
        finally:
            release_response.set()
            server.shutdown()
            thread.join(timeout=5)


def test_absent_and_partial_metadata_propagate_without_reupload(publication):
    receipt, dist, state, data = publication
    state["responses"] = [(404, {}), (200, {**data, "urls": data["urls"][:1]}), (200, data)]
    release.verify_artifacts(receipt, dist, complete=True)
    assert state["calls"] == 3


def test_missing_metadata_exhaustion_is_visible(publication, monkeypatch):
    receipt, dist, state, _ = publication
    state["responses"] = [(404, {})]
    monkeypatch.setattr(release, "PUBLICATION_WAIT_SECONDS", 0.05)
    with pytest.raises(ValueError, match="incomplete after.*version 2.6.6rc10 is missing"):
        release.verify_artifacts(receipt, dist, complete=True)
    assert state["calls"] > 1


def test_final_http_timeout_preserves_last_validated_missing_artifact(publication):
    receipt, dist, state, data = publication
    state["responses"] = [(200, {**data, "urls": data["urls"][:1]})]
    state["hold_call"] = 2
    with pytest.raises(ValueError, match="Python publication is incomplete") as error:
        release.verify_artifacts(receipt, dist, complete=True)
    assert str(error.value).rsplit("is missing ", 1)[1] == data["urls"][1]["filename"]
    assert isinstance(error.value.__cause__, TimeoutError)
    assert state["calls"] == 2


def test_http_timeout_without_publication_evidence_remains_visible(publication):
    receipt, dist, state, _ = publication
    state["hold_call"] = 1
    with pytest.raises(TimeoutError):
        release.verify_artifacts(receipt, dist, complete=True)
    assert state["calls"] == 1


def test_http_timeout_before_publication_deadline_remains_visible(publication, monkeypatch):
    receipt, dist, state, _ = publication
    state["responses"] = [(404, {})]
    state["hold_call"] = 2
    clock = release.time
    monkeypatch.setattr(release, "time", SimpleNamespace(
        monotonic=lambda: 0.0, sleep=clock.sleep, time_ns=clock.time_ns,
    ))
    with pytest.raises(TimeoutError):
        release.verify_artifacts(receipt, dist, complete=True)
    assert state["calls"] == 2


@pytest.mark.parametrize("failure", ["source", "hash", "unexpected", "http"])
def test_mismatches_and_required_http_failures_are_immediate(publication, failure):
    receipt, dist, state, data = publication
    if failure == "source":
        data["info"]["project_urls"]["Source"] = release.SOURCE_URL + "b" * 40
    elif failure == "hash":
        data["urls"][0]["digests"]["sha256"] = "c" * 64
    elif failure == "unexpected":
        data["urls"].append({"filename": "unexpected.whl", "digests": {"sha256": "d" * 64}})
    state["responses"] = [(503 if failure == "http" else 200, data)]
    with pytest.raises(release.urllib.error.HTTPError if failure == "http" else ValueError):
        release.verify_artifacts(receipt, dist, complete=True)
    assert state["calls"] == 1


def test_preupload_check_remains_immediate(publication):
    receipt, dist, state, _ = publication
    state["responses"] = [(404, {})]
    release.verify_artifacts(receipt, dist)
    assert state["calls"] == 1
