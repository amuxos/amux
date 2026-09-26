import hashlib
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("release", Path(__file__).with_name("prepare-public-release.py"))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class PublicReleaseTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for platform in ("darwin_arm64", "darwin_amd64", "linux_arm64", "linux_amd64"):
            name = f"amux_v1.2.3_{platform}.tar.gz"
            payload = platform.encode()
            (self.root / name).write_bytes(payload)
            manifest = {"version": "v1.2.3", "assets": {platform: {
                "url": f"https://github.com/amuxos/amux-releases/releases/download/v1.2.3/{name}",
                "sha256": hashlib.sha256(payload).hexdigest(),
            }}}
            (self.root / f"manifest_{platform}.json").write_text(json.dumps(manifest))

        setup_name = "amux_v1.2.3_windows_amd64_Setup.exe"
        package_name = "amux_v1.2.3_windows_amd64.tar.gz"
        setup_payload = b"setup-exe"
        package_payload = b"windows-package"
        (self.root / setup_name).write_bytes(setup_payload)
        (self.root / package_name).write_bytes(package_payload)
        (self.root / "manifest_windows_amd64.json").write_text(json.dumps({
            "version": "v1.2.3",
            "assets": {
                "windows_amd64": {
                    "url": f"https://github.com/amuxos/amux-releases/releases/download/v1.2.3/{setup_name}",
                    "sha256": hashlib.sha256(setup_payload).hexdigest(),
                    "package_url": f"https://github.com/amuxos/amux-releases/releases/download/v1.2.3/{package_name}",
                    "package_sha256": hashlib.sha256(package_payload).hexdigest(),
                    "installer": "Setup.exe",
                }
            },
        }))

    def test_complete_release(self):
        release.prepare(self.root, "v1.2.3")
        self.assertEqual(len(json.loads((self.root / "latest.json").read_text())["assets"]), 5)
        self.assertEqual((self.root / "version.txt").read_text(), "v1.2.3\n")
        self.assertTrue((self.root / "install.sh").is_file())
        self.assertIn("Windows", (self.root / "notes.md").read_text())

    def test_missing_platform_cannot_publish(self):
        (self.root / "manifest_linux_arm64.json").unlink()
        with self.assertRaisesRegex(ValueError, "missing platform"):
            release.prepare(self.root, "v1.2.3")
        self.assertFalse((self.root / "latest.json").exists())

    def test_modified_artifact_cannot_publish(self):
        (self.root / "amux_v1.2.3_linux_amd64.tar.gz").write_bytes(b"corrupt")
        with self.assertRaisesRegex(ValueError, "invalid release asset"):
            release.prepare(self.root, "v1.2.3")
        self.assertFalse((self.root / "latest.json").exists())

    def test_mixed_versions_cannot_publish(self):
        manifest = self.root / "manifest_darwin_arm64.json"
        manifest.write_text(manifest.read_text().replace("v1.2.3", "v1.2.4"))
        with self.assertRaisesRegex(ValueError, "mixed release versions"):
            release.prepare(self.root, "v1.2.3")

    def test_missing_windows_setup_cannot_publish(self):
        (self.root / "amux_v1.2.3_windows_amd64_Setup.exe").unlink()
        with self.assertRaisesRegex(ValueError, "invalid release asset|No such file|not found"):
            try:
                release.prepare(self.root, "v1.2.3")
            except FileNotFoundError as err:
                raise ValueError("not found") from err


if __name__ == "__main__":
    unittest.main()
