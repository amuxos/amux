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
    platforms = {"darwin_arm64", "darwin_amd64", "linux_arm64", "linux_amd64"}
    for manifest_file in sorted(directory.glob("manifest_*.json")):
        manifest = json.loads(manifest_file.read_text())
        if manifest["version"] != version:
            raise ValueError("mixed release versions")
        for platform, asset in manifest["assets"].items():
            if platform not in platforms or platform in assets:
                raise ValueError("unexpected or duplicate platform")
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
        "Prebuilt packages for macOS and Linux on amd64 and arm64. "
        "Packages include the Claude SDK sidecar and license notices.\n\n"
        "Install an authenticated Codex, Claude, or Pi agent first, then:\n\n"
        "```sh\n"
        "curl -fsSL https://github.com/amuxos/amux-releases/releases/latest/download/install.sh -o /tmp/amux-install.sh\n"
        "bash /tmp/amux-install.sh --runtime codex\n"
        "```\n\n"
        "Update with `amux update --restart`. Checksums are verified before installation.\n"
    )


if __name__ == "__main__":
    prepare(Path(sys.argv[1]), sys.argv[2])
