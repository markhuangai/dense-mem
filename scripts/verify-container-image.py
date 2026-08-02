#!/usr/bin/env python3
"""Verify Dense-Mem image platforms, size, metadata, and runtime contents."""

from __future__ import annotations

import argparse
import io
import json
import os
from pathlib import PurePosixPath
import subprocess
import sys
import tarfile
import tempfile
from typing import Any, BinaryIO


PLATFORM_MACHINES = {
    "linux/amd64": ("amd64", 62),
    "linux/arm64": ("arm64", 183),
}

VARIANTS = {
    "release": {
        "binary": "app/server",
        "control_bundle": True,
    },
    "demo": {
        "binary": "app/demo-server",
        "control_bundle": False,
    },
}

BANNED_APP_PATHS = {
    "app/migrate",
    "app/provision-profile",
    "app/provision-team",
    "app/list-profiles",
    "app/list-teams",
    "app/delete-profile",
    "app/delete-team",
    "app/list-keys",
    "app/list-team-profiles",
    "app/delete-key",
    "app/delete-team-profile",
    "app/rotate-key",
    "app/rotate-team-profile-key",
    "app/review-conflicts",
}

COMMAND_TIMEOUT_SECONDS = 300
EXPORT_TIMEOUT_SECONDS = 300
REMOVE_TIMEOUT_SECONDS = 60


class VerificationError(RuntimeError):
    pass


def command(*args: str, timeout: float = COMMAND_TIMEOUT_SECONDS) -> str:
    completed = subprocess.run(
        args,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        timeout=timeout,
    )
    return completed.stdout


def normalize_path(raw: str) -> str:
    while raw.startswith("./"):
        raw = raw[2:]
    path = PurePosixPath(raw)
    if path.is_absolute() or ".." in path.parts:
        raise VerificationError(f"unsafe archive path: {raw}")
    return str(path)


def apply_filesystem_tar(
    archive: tarfile.TarFile,
    files: dict[str, tuple[str, int, bytes]],
) -> None:
    for member in archive:
        path = normalize_path(member.name)
        if path in {"", "."}:
            continue

        parent, _, name = path.rpartition("/")
        if name == ".wh..wh..opq":
            prefix = parent + "/" if parent else ""
            for existing in list(files):
                if existing.startswith(prefix):
                    del files[existing]
            continue
        if name.startswith(".wh."):
            target = f"{parent}/{name[4:]}" if parent else name[4:]
            files.pop(target, None)
            continue

        if member.isfile():
            extracted = archive.extractfile(member)
            header = extracted.read(20) if extracted is not None else b""
            files[path] = ("file", member.mode, header)
        elif member.issym():
            files[path] = ("symlink", member.mode, member.linkname.encode())
        elif member.islnk():
            files[path] = ("hardlink", member.mode, member.linkname.encode())


def verify_filesystem(
    files: dict[str, tuple[str, int, bytes]],
    variant: str,
    platform: str,
) -> None:
    expected_binary = VARIANTS[variant]["binary"]
    elf_paths = sorted(
        path
        for path, (_, _, header) in files.items()
        if path.startswith("app/") and header.startswith(b"\x7fELF")
    )
    if elf_paths != [expected_binary]:
        raise VerificationError(
            f"{variant} {platform} project ELF files were {elf_paths}, "
            f"expected [{expected_binary!r}]"
        )

    kind, mode, header = files[expected_binary]
    if kind != "file" or mode & 0o111 == 0:
        raise VerificationError(f"{expected_binary} is not an executable file")
    if len(header) < 20:
        raise VerificationError(f"{expected_binary} has a truncated ELF header")
    byte_order = "little" if header[5] == 1 else "big"
    machine = int.from_bytes(header[18:20], byte_order)
    expected_machine = PLATFORM_MACHINES[platform][1]
    if machine != expected_machine:
        raise VerificationError(
            f"{expected_binary} ELF machine {machine} does not match "
            f"{platform} ({expected_machine})"
        )

    present_banned = sorted(BANNED_APP_PATHS.intersection(files))
    if present_banned:
        raise VerificationError(f"removed command paths remain: {present_banned}")
    if not any(
        path.startswith("app/migrations/postgres/") and path.endswith(".sql")
        for path in files
    ):
        raise VerificationError("PostgreSQL migrations are missing")
    if not any(
        path.startswith("app/web/user-dist/") and path.endswith(".html")
        for path in files
    ):
        raise VerificationError("user portal static bundle is missing")

    has_control_bundle = any(
        path.startswith("app/web/dist/") and path.endswith(".html")
        for path in files
    )
    if has_control_bundle != VARIANTS[variant]["control_bundle"]:
        expected = "present" if VARIANTS[variant]["control_bundle"] else "absent"
        raise VerificationError(f"control portal static bundle must be {expected}")


def verify_metadata(
    config: dict[str, Any],
    platform: str,
    revision: str | None,
) -> None:
    expected_arch = PLATFORM_MACHINES[platform][0]
    if config.get("Os", config.get("os")) != "linux":
        raise VerificationError(f"image OS does not match {platform}")
    if config.get("Architecture", config.get("architecture")) != expected_arch:
        raise VerificationError(f"image architecture does not match {platform}")
    if revision is None:
        return
    nested_config = config.get("Config", config.get("config", {})) or {}
    labels = nested_config.get("Labels", nested_config.get("labels", {})) or {}
    actual = labels.get("org.opencontainers.image.revision")
    if actual != revision:
        raise VerificationError(
            f"revision label {actual!r} does not match expected {revision!r}"
        )


class OCIArchive:
    def __init__(self, path: str) -> None:
        self.archive = tarfile.open(path, "r:*")

    def close(self) -> None:
        self.archive.close()

    def bytes(self, path: str) -> bytes:
        member = self.archive.getmember(path)
        extracted = self.archive.extractfile(member)
        if extracted is None:
            raise VerificationError(f"OCI archive member is unreadable: {path}")
        return extracted.read()

    def blob(self, digest: str) -> bytes:
        algorithm, separator, value = digest.partition(":")
        if separator != ":" or algorithm != "sha256":
            raise VerificationError(f"unsupported OCI digest: {digest}")
        return self.bytes(f"blobs/{algorithm}/{value}")

    def json_blob(self, digest: str) -> dict[str, Any]:
        return json.loads(self.blob(digest))


def descriptor_platform(
    archive: OCIArchive,
    descriptor: dict[str, Any],
    manifest: dict[str, Any],
) -> str:
    declared = descriptor.get("platform", {})
    os_name = declared.get("os")
    architecture = declared.get("architecture")
    if os_name and architecture:
        return f"{os_name}/{architecture}"
    config = archive.json_blob(manifest["config"]["digest"])
    return f"{config.get('os')}/{config.get('architecture')}"


def oci_manifests(archive: OCIArchive) -> dict[str, dict[str, Any]]:
    index = json.loads(archive.bytes("index.json"))
    found: dict[str, dict[str, Any]] = {}

    def visit(descriptor: dict[str, Any]) -> None:
        document = archive.json_blob(descriptor["digest"])
        if "layers" in document and "config" in document:
            platform = descriptor_platform(archive, descriptor, document)
            found[platform] = document
            return
        for child in document.get("manifests", []):
            visit(child)

    for descriptor in index.get("manifests", []):
        visit(descriptor)
    return found


def open_layer(blob: bytes) -> tarfile.TarFile:
    try:
        return tarfile.open(fileobj=io.BytesIO(blob), mode="r:*")
    except tarfile.ReadError as error:
        raise VerificationError("unsupported or invalid OCI layer compression") from error


def verify_oci_archive(args: argparse.Namespace) -> None:
    archive = OCIArchive(args.oci_archive)
    try:
        manifests = oci_manifests(archive)
        supported = set(manifests).intersection(PLATFORM_MACHINES)
        if supported != set(args.platform):
            raise VerificationError(
                f"OCI platforms were {sorted(supported)}, expected {sorted(args.platform)}"
            )
        for platform in args.platform:
            manifest = manifests.get(platform)
            if manifest is None:
                raise VerificationError(f"OCI archive is missing {platform}")
            compressed_size = sum(layer["size"] for layer in manifest["layers"])
            if compressed_size > args.max_compressed_bytes:
                raise VerificationError(
                    f"{platform} compressed layers are {compressed_size} bytes; "
                    f"limit is {args.max_compressed_bytes}"
                )
            config = archive.json_blob(manifest["config"]["digest"])
            verify_metadata(config, platform, args.revision)
            files: dict[str, tuple[str, int, bytes]] = {}
            for layer in manifest["layers"]:
                with open_layer(archive.blob(layer["digest"])) as layer_archive:
                    apply_filesystem_tar(layer_archive, files)
            verify_filesystem(files, args.variant, platform)
            print(f"{args.variant} {platform}: {compressed_size} compressed bytes")
    finally:
        archive.close()


def remote_manifests(image: str) -> dict[str, dict[str, Any]]:
    index = json.loads(command("docker", "buildx", "imagetools", "inspect", "--raw", image))
    manifests: dict[str, dict[str, Any]] = {}
    for descriptor in index.get("manifests", []):
        declared = descriptor.get("platform", {})
        platform = f"{declared.get('os')}/{declared.get('architecture')}"
        if platform in PLATFORM_MACHINES:
            manifests[platform] = descriptor
    return manifests


def repository_name(image: str) -> str:
    name = image.split("@", 1)[0]
    last_slash = name.rfind("/")
    last_colon = name.rfind(":")
    if last_colon > last_slash:
        return name[:last_colon]
    return name


def export_rootfs(image: str, platform: str, destination: str) -> None:
    container_id = command("docker", "create", "--platform", platform, image).strip()
    try:
        with open(destination, "wb") as output:
            subprocess.run(
                ("docker", "export", container_id),
                check=True,
                stdout=output,
                timeout=EXPORT_TIMEOUT_SECONDS,
            )
    finally:
        subprocess.run(
            ("docker", "rm", container_id),
            check=False,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=REMOVE_TIMEOUT_SECONDS,
        )


def verify_remote_image(args: argparse.Namespace) -> None:
    descriptors = remote_manifests(args.image)
    if set(descriptors) != set(args.platform):
        raise VerificationError(
            f"published platforms were {sorted(descriptors)}, expected {sorted(args.platform)}"
        )
    for platform in args.platform:
        descriptor = descriptors.get(platform)
        if descriptor is None:
            raise VerificationError(f"published image is missing {platform}")
        digest_ref = f"{repository_name(args.image)}@{descriptor['digest']}"
        manifest = json.loads(
            command("docker", "buildx", "imagetools", "inspect", "--raw", digest_ref)
        )
        compressed_size = sum(layer["size"] for layer in manifest["layers"])
        if compressed_size > args.max_compressed_bytes:
            raise VerificationError(
                f"{platform} compressed layers are {compressed_size} bytes; "
                f"limit is {args.max_compressed_bytes}"
            )

        command("docker", "pull", "--platform", platform, digest_ref)
        inspected = json.loads(command("docker", "image", "inspect", digest_ref))[0]
        verify_metadata(inspected, platform, args.revision)
        with tempfile.TemporaryDirectory(prefix="dense-mem-image-") as temp_dir:
            rootfs_path = os.path.join(temp_dir, "rootfs.tar")
            export_rootfs(digest_ref, platform, rootfs_path)
            files: dict[str, tuple[str, int, bytes]] = {}
            with tarfile.open(rootfs_path, "r:") as rootfs:
                apply_filesystem_tar(rootfs, files)
            verify_filesystem(files, args.variant, platform)
        print(f"{args.variant} {platform}: {compressed_size} compressed bytes")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--oci-archive")
    source.add_argument("--image")
    parser.add_argument("--variant", choices=sorted(VARIANTS), required=True)
    parser.add_argument("--platform", action="append", choices=sorted(PLATFORM_MACHINES), required=True)
    parser.add_argument("--max-compressed-bytes", type=int, required=True)
    parser.add_argument("--revision")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.max_compressed_bytes <= 0:
        raise VerificationError("compressed-size limit must be positive")
    if len(args.platform) != len(set(args.platform)):
        raise VerificationError("platforms must not be repeated")
    if args.oci_archive:
        verify_oci_archive(args)
    else:
        verify_remote_image(args)
    return 0


def cli() -> int:
    try:
        return main()
    except (
        VerificationError,
        subprocess.CalledProcessError,
        subprocess.TimeoutExpired,
    ) as error:
        print(f"image verification failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(cli())
