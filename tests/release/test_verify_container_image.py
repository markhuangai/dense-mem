from __future__ import annotations

from contextlib import redirect_stderr
import importlib.util
import io
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock


SCRIPT_PATH = Path(__file__).resolve().parents[2] / "scripts" / "verify-container-image.py"
SPEC = importlib.util.spec_from_file_location("verify_container_image", SCRIPT_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError(f"cannot load {SCRIPT_PATH}")
VERIFY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VERIFY)


def elf_header(machine: int) -> bytes:
    header = bytearray(20)
    header[:4] = b"\x7fELF"
    header[5] = 1
    header[18:20] = machine.to_bytes(2, "little")
    return bytes(header)


def release_files() -> dict[str, tuple[str, int, bytes]]:
    return {
        "app/server": ("file", 0o755, elf_header(62)),
        "app/migrations/postgres/0001.sql": ("file", 0o644, b"SELECT 1"),
        "app/web/dist/index.html": ("file", 0o644, b"<html>"),
        "app/web/user-dist/index.html": ("file", 0o644, b"<html>"),
    }


class VerifyContainerImageTest(unittest.TestCase):
    def test_apply_filesystem_tar_honors_whiteouts(self) -> None:
        files = {
            "app/data/delete": ("file", 0o644, b"old"),
            "app/data/keep": ("file", 0o644, b"old"),
            "app/cache/stale": ("file", 0o644, b"old"),
        }
        layer = io.BytesIO()
        with tarfile.open(fileobj=layer, mode="w") as archive:
            for name, contents in (
                ("app/data/.wh.delete", b""),
                ("app/cache/.wh..wh..opq", b""),
                ("app/cache/current", b"new"),
            ):
                member = tarfile.TarInfo(name)
                member.size = len(contents)
                archive.addfile(member, io.BytesIO(contents))
        layer.seek(0)

        with tarfile.open(fileobj=layer, mode="r:") as archive:
            VERIFY.apply_filesystem_tar(archive, files)

        self.assertNotIn("app/data/delete", files)
        self.assertIn("app/data/keep", files)
        self.assertNotIn("app/cache/stale", files)
        self.assertIn("app/cache/current", files)

    def test_verify_filesystem_accepts_release_contract(self) -> None:
        VERIFY.verify_filesystem(release_files(), "release", "linux/amd64")

    def test_verify_filesystem_rejects_banned_path(self) -> None:
        files = release_files()
        files["app/review-conflicts"] = ("file", 0o755, b"command")

        with self.assertRaisesRegex(VERIFY.VerificationError, "removed command paths"):
            VERIFY.verify_filesystem(files, "release", "linux/amd64")

    def test_verify_filesystem_rejects_wrong_architecture(self) -> None:
        files = release_files()
        files["app/server"] = ("file", 0o755, elf_header(183))

        with self.assertRaisesRegex(VERIFY.VerificationError, "does not match"):
            VERIFY.verify_filesystem(files, "release", "linux/amd64")

    def test_verify_metadata_checks_platform_and_revision(self) -> None:
        config = {
            "os": "linux",
            "architecture": "amd64",
            "config": {"Labels": {"org.opencontainers.image.revision": "abc123"}},
        }
        VERIFY.verify_metadata(config, "linux/amd64", "abc123")

        with self.assertRaisesRegex(VERIFY.VerificationError, "revision label"):
            VERIFY.verify_metadata(config, "linux/amd64", "different")

    def test_command_and_export_are_bounded(self) -> None:
        completed = mock.Mock(stdout="ok")
        with mock.patch.object(VERIFY.subprocess, "run", return_value=completed) as run:
            self.assertEqual(VERIFY.command("docker", "version"), "ok")
        self.assertEqual(run.call_args.kwargs["timeout"], VERIFY.COMMAND_TIMEOUT_SECONDS)

        with tempfile.TemporaryDirectory() as temp_dir:
            destination = str(Path(temp_dir) / "rootfs.tar")
            with (
                mock.patch.object(VERIFY, "command", return_value="container-id\n"),
                mock.patch.object(VERIFY.subprocess, "run") as export_run,
            ):
                VERIFY.export_rootfs("example:tag", "linux/amd64", destination)
        self.assertEqual(export_run.call_args_list[0].kwargs["timeout"], VERIFY.EXPORT_TIMEOUT_SECONDS)
        self.assertEqual(export_run.call_args_list[1].kwargs["timeout"], VERIFY.REMOVE_TIMEOUT_SECONDS)

    def test_cli_converts_timeout_to_verification_failure(self) -> None:
        stderr = io.StringIO()
        timeout = subprocess.TimeoutExpired(cmd=("docker", "pull"), timeout=1)
        with mock.patch.object(VERIFY, "main", side_effect=timeout), redirect_stderr(stderr):
            self.assertEqual(VERIFY.cli(), 1)
        self.assertIn("image verification failed", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
