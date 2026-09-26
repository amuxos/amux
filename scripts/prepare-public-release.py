#!/usr/bin/env python3
"""Validate the complete platform set before publishing any stable release."""

import hashlib
import json
import re
import shutil
import sys
from pathlib import Path


def prepare(directory, version):
    if not re.fullmatch(r"v\d+\.\d+\.\d+", version):
        raise ValueError("expected a stable version tag vX.Y.Z")
    assets = {}
    platforms = {
        "darwin_arm64",
        "darwin_amd64",
        "linux_arm64",
        "linux_amd64",
        "windows_amd64",
    }
    for manifest_file in sorted(directory.glob("manifest_*.json")):
        manifest = json.loads(manifest_file.read_text())
        if manifest["version"] != version:
            raise ValueError("mixed release versions")
        for platform, asset in manifest["assets"].items():
            if platform not in platforms or platform in assets:
                raise ValueError("unexpected or duplicate platform")
            if platform == "windows_amd64":
                setup_name = f"amux_{version}_{platform}_Setup.exe"
                package_name = f"amux_{version}_{platform}.tar.gz"
                expected_setup_url = (
                    f"https://github.com/amuxos/amux-releases/releases/download/{version}/{setup_name}"
                )
                expected_package_url = (
                    f"https://github.com/amuxos/amux-releases/releases/download/{version}/{package_name}"
                )
                with (directory / setup_name).open("rb") as setup:
                    setup_digest = hashlib.file_digest(setup, "sha256").hexdigest()
                with (directory / package_name).open("rb") as archive:
                    package_digest = hashlib.file_digest(archive, "sha256").hexdigest()
                if asset.get("url") != expected_setup_url or asset.get("sha256") != setup_digest:
                    raise ValueError(f"invalid release asset: {platform} Setup.exe")
                if asset.get("package_url") != expected_package_url or asset.get("package_sha256") != package_digest:
                    raise ValueError(f"invalid release asset: {platform} package")
                (directory / f"{setup_name}.sha256").write_text(f"{setup_digest}  {setup_name}\n")
                (directory / f"{package_name}.sha256").write_text(f"{package_digest}  {package_name}\n")
                assets[platform] = asset
                continue

            name = f"amux_{version}_{platform}.tar.gz"
            expected_url = f"https://github.com/amuxos/amux-releases/releases/download/{version}/{name}"
            with (directory / name).open("rb") as archive:
                digest = hashlib.file_digest(archive, "sha256").hexdigest()
            if asset["url"] != expected_url or asset["sha256"] != digest:
                raise ValueError(f"invalid release asset: {platform}")
            (directory / f"{name}.sha256").write_text(f"{digest}  {name}\n")
            assets[platform] = asset
    if set(assets) != platforms:
        raise ValueError("missing platform packages")
    (directory / "latest.json").write_text(json.dumps({"version": version, "assets": assets}, indent=2) + "\n")
    (directory / "version.txt").write_text(version + "\n")
    shutil.copyfile(Path(__file__).parent / "install.sh", directory / "install.sh")
    (directory / "notes.md").write_text(
        f"amux {version}\n\n"
        "Prebuilt packages for macOS and Linux on amd64 and arm64, and Windows on amd64. "
        "Packages include the Claude SDK sidecar and license notices.\n\n"
        "Install an authenticated Codex, Claude, or Pi agent first.\n\n"
        "macOS / Linux:\n\n"
        "```sh\n"
        "curl -fsSL https://github.com/amuxos/amux-releases/releases/latest/download/install.sh -o /tmp/amux-install.sh\n"
        "bash /tmp/amux-install.sh --runtime codex\n"
        "```\n\n"
        "Windows:\n\n"
        "1. Download `amux_<version>_windows_amd64_Setup.exe` and the matching "
        "`amux_<version>_windows_amd64.tar.gz` from the release assets.\n"
        "2. Extract the tar.gz next to Setup.exe (so amux.exe / amuxd.exe / amuxlet.exe "
        "sit beside it), or extract everything and run Setup.exe from that folder.\n"
        "3. Run Setup.exe as Administrator with e.g. "
        "`Setup.exe --runtime codex` (Feishu QR/browser bind is required in phase 1).\n\n"
        "Update with `amux update --restart` (Unix) or re-run Setup after extracting a newer package (Windows). "
        "Checksums are verified before installation.\n"
    )


if __name__ == "__main__":
    prepare(Path(sys.argv[1]), sys.argv[2])
