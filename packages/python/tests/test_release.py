import hashlib
import importlib.util
import os
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

spec = importlib.util.spec_from_file_location(
    "release_policy", Path(__file__).parents[1] / "scripts" / "release.py"
)
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


@pytest.mark.parametrize("tag,version", [("v2.6.6-rc.10", "2.6.6rc10"), ("v2.6.6", "2.6.6")])
def test_shared_release_version(tag, version):
    assert release.package_version(tag) == version


@pytest.mark.parametrize(
    "tag", ["python-v0.1.0rc1", "v02.6.6", "v2.6.6-rc.01", "v2.6.6;command", "v2.6.6-beta.1"]
)
def test_release_rejects_noncanonical_tags(tag):
    with pytest.raises(ValueError):
        release.package_version(tag)


@pytest.fixture
def repository(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    for name in tuple(os.environ):
        if name.startswith("GIT_"):
            monkeypatch.delenv(name)
    monkeypatch.setenv("GIT_CONFIG_GLOBAL", os.devnull)
    monkeypatch.setenv("GIT_CONFIG_NOSYSTEM", "1")

    def git(*args):
        return subprocess.check_output(["git", *args], text=True).strip()

    git("init", "--quiet", "--initial-branch=main")
    git("config", "user.name", "Release Test")
    git("config", "user.email", "release@example.invalid")
    package = tmp_path / "packages/python"
    package.mkdir(parents=True)
    (package / "pyproject.toml").write_text('[project]\nversion = "0.0.0"\n[project.urls]\n')
    (package / "client.py").write_text("initial\n")

    def commit():
        git("add", ".")
        git("commit", "--quiet", "-m", "fixture")
        sha = git("rev-parse", "HEAD")
        git("update-ref", "refs/remotes/origin/main", sha)
        return sha

    return package, git, commit


def published(version, source, files=None):
    return {
        "info": {"project_urls": {"Source": release.SOURCE_URL + source}},
        "urls": files
        if files is not None
        else [{"filename": name, "yanked": False} for name in release.artifact_names(version)],
    }


def registry(monkeypatch, records):
    index = {"releases": {version: data["urls"] for version, data in records.items()}}
    monkeypatch.setattr(
        release,
        "fetch_json",
        lambda url, **_: (
            index if url.endswith("densemem/json") else records.get(url.split("/")[-2])
        ),
    )


def plan(tag):
    return release.prepare_release(tag, "refs/heads/main")


def test_first_publication_and_failed_upload_remain_eligible(repository, monkeypatch):
    package, git, commit = repository
    source = commit()
    git("tag", "v2.6.6-rc.10")
    registry(monkeypatch, {})
    assert plan("v2.6.6-rc.10")["should_publish"]
    partial = published("2.6.6rc10", source)
    partial["urls"].pop()
    registry(monkeypatch, {"2.6.6rc10": partial})
    assert plan("v2.6.6-rc.10")["should_publish"]
    (package / "client.py").write_text("changed\n")
    commit()
    git("tag", "v2.6.6-rc.11")
    assert plan("v2.6.6-rc.11")["should_publish"]


def test_compare_with_last_successful_python_source_not_previous_server_tag(
    repository, monkeypatch
):
    package, git, commit = repository
    source = commit()
    registry(monkeypatch, {"2.6.6rc9": published("2.6.6rc9", source)})
    (package / "client.py").write_text("unpublished change\n")
    commit()
    git("tag", "v2.6.6-rc.10")
    Path("README.md").write_text("server-only docs\n")
    commit()
    git("tag", "v2.6.6-rc.11")
    assert plan("v2.6.6-rc.11")["should_publish"]


def test_unchanged_rc_is_skipped_but_stable_promotion_is_published(repository, monkeypatch):
    _, git, commit = repository
    source = commit()
    registry(monkeypatch, {"2.6.6rc10": published("2.6.6rc10", source)})
    Path("server.go").write_text("package main\n")
    commit()
    git("tag", "v2.6.6-rc.11")
    git("tag", "v2.6.6")
    assert not plan("v2.6.6-rc.11")["should_publish"]
    assert plan("v2.6.6")["should_publish"]


def test_stable_tag_can_identify_an_earlier_merged_source(repository, monkeypatch):
    _, git, commit = repository
    source = commit()
    git("tag", "v2.6.6")
    Path("server.go").write_text("package main\n")
    commit()
    registry(monkeypatch, {})
    assert plan("v2.6.6")["source_sha"] == source


def test_reject_unmerged_source_wrong_expected_sha_and_non_main_workflow(repository, monkeypatch):
    package, git, commit = repository
    source = commit()
    git("tag", "v2.6.6")
    registry(monkeypatch, {})
    with pytest.raises(ValueError, match="trusted main"):
        release.prepare_release("v2.6.6", "refs/heads/feature")
    with pytest.raises(ValueError, match="expected source"):
        release.prepare_release("v2.6.6", "refs/heads/main", "0" * 40)
    git("switch", "--quiet", "-c", "unmerged")
    (package / "client.py").write_text("unmerged\n")
    commit()
    git("update-ref", "refs/remotes/origin/main", source)
    git("tag", "v2.6.7")
    with pytest.raises(ValueError, match="merged main"):
        plan("v2.6.7")


def test_stamp_shared_version_and_source_without_changing_other_metadata(repository):
    package, _, commit = repository
    source = commit()
    release.stamp_version(package / "pyproject.toml", "2.6.6rc10", source)
    data = release.tomllib.loads((package / "pyproject.toml").read_text())
    assert data["project"]["version"] == "2.6.6rc10"
    assert data["project"]["urls"]["Source"] == release.SOURCE_URL + source


def test_existing_version_and_partial_upload_require_identical_artifact_bytes(
    repository, monkeypatch
):
    _, _, commit = repository
    source = commit()
    dist = Path("dist")
    dist.mkdir()
    files = []
    for name in release.artifact_names("2.6.6rc10"):
        path = dist / name
        path.write_bytes(b"verified artifact")
        files.append(
            {"filename": name, "digests": {"sha256": hashlib.sha256(path.read_bytes()).hexdigest()}}
        )
    receipt = {"version": "2.6.6rc10", "source_sha": source}
    monkeypatch.setattr(release, "PUBLICATION_WAIT_SECONDS", 0)
    registry(monkeypatch, {"2.6.6rc10": published("2.6.6rc10", source, files[:1])})
    release.verify_artifacts(receipt, dist)
    with pytest.raises(ValueError, match="incomplete"):
        release.verify_artifacts(receipt, dist, complete=True)
    registry(monkeypatch, {"2.6.6rc10": published("2.6.6rc10", source, files)})
    release.verify_artifacts(receipt, dist, complete=True)
    for name in release.artifact_names("2.6.6rc10"):
        (dist / (name + ".publish.attestation")).write_text("{}")
    with pytest.raises(ValueError, match="selected wheel and sdist"):
        release.verify_artifacts(receipt, dist)
    release.verify_artifacts(receipt, dist, complete=True)
    unexpected = dist / "unexpected.whl.publish.attestation"
    unexpected.write_text("{}")
    with pytest.raises(ValueError, match="selected wheel and sdist"):
        release.verify_artifacts(receipt, dist, complete=True)
    unexpected.unlink()
    (dist / files[0]["filename"]).write_bytes(b"different artifact")
    with pytest.raises(ValueError, match="differs"):
        release.verify_artifacts(receipt, dist, complete=True)


def test_reject_published_source_outside_main(repository, monkeypatch):
    _, git, commit = repository
    commit()
    git("tag", "v2.6.6-rc.10")
    registry(monkeypatch, {"2.6.6rc9": published("2.6.6rc9", "0" * 40)})
    with pytest.raises(ValueError, match="merged main"):
        plan("v2.6.6-rc.10")


def test_registry_reads_observe_completed_uploads_and_preserve_required_http_failures():
    cache = {}
    state = {"files": 1, "status": 200}

    class Registry(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_GET(self):
            self.send_response(state["status"])
            self.end_headers()
            payload = cache.setdefault(self.path, f'{{"files": {state["files"]}}}')
            self.wfile.write(payload.encode())

    with ThreadingHTTPServer(("127.0.0.1", 0), Registry) as server:
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        url = f"http://127.0.0.1:{server.server_port}/json"
        try:
            assert release.fetch_json(url) == {"files": 1}
            state["files"] = 2
            assert release.fetch_json(url) == {"files": 2}
            state["status"] = 404
            assert release.fetch_json(url) is None
            state["status"] = 503
            with pytest.raises(release.urllib.error.HTTPError):
                release.fetch_json(url)
        finally:
            server.shutdown()
            thread.join(timeout=5)
