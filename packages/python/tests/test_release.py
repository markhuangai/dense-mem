import importlib.util
from pathlib import Path

import pytest

spec = importlib.util.spec_from_file_location(
    "release_policy", Path(__file__).parents[1] / "scripts" / "release.py"
)
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


def test_release_matches_metadata_and_current_merged_main():
    release.validate_release("python-v0.1.0rc1", "0.1.0rc1", "sha", "sha", "refs/heads/main")


@pytest.mark.parametrize(
    "tag,version,source,main,ref",
    [
        ("python-v0.1.0rc1", "0.1.0rc1", "sha", "sha", "refs/heads/unreviewed"),
        ("python-v0.1.0rc1", "0.1.0rc1", "old", "new", "refs/heads/main"),
        ("python-v0.1.0rc1", "0.1.0rc2", "sha", "sha", "refs/heads/main"),
        ("v0.1.0rc1", "0.1.0rc1", "sha", "sha", "refs/heads/main"),
        ("python-v0.1.0rc1;command", "0.1.0rc1", "sha", "sha", "refs/heads/main"),
    ],
)
def test_release_rejects_wrong_ref_source_version_or_tag(tag, version, source, main, ref):
    with pytest.raises(ValueError):
        release.validate_release(tag, version, source, main, ref)
