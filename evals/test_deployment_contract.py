"""Static safety contracts for the Linux fleet deployment assets."""

import subprocess
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


class DeploymentContractTests(unittest.TestCase):
    def test_vm_bootstrap_is_valid_bash_and_role_aware(self) -> None:
        script = ROOT / "scripts" / "setup-vm.sh"
        subprocess.run(["bash", "-n", str(script)], check=True)
        body = script.read_text(encoding="utf-8")
        self.assertIn("MACHINIST_REPOSITORY", body)
        self.assertIn("MACHINIST_NODE_ROLE", body)
        self.assertIn("control-plane | worker", body)

    def test_macos_bootstrap_and_launch_agents_are_valid(self) -> None:
        script = ROOT / "scripts" / "setup-macos.sh"
        subprocess.run(["bash", "-n", str(script)], check=True)
        body = script.read_text(encoding="utf-8")
        self.assertIn("MACHINIST_BINARY", body)
        self.assertIn("MACHINIST_DGX_SSH_HOST", body)
        self.assertIn("StrictHostKeyChecking=yes", body)
        for path in sorted((ROOT / "deploy" / "launchd").glob("*.plist.in")):
            ET.parse(path)

    def test_macos_dgx_tunnel_is_loopback_only_and_verified(self) -> None:
        body = (
            ROOT / "deploy" / "launchd" / "sh.machinist.dgx-tunnel.plist.in"
        ).read_text(encoding="utf-8")
        self.assertIn("StrictHostKeyChecking=yes", body)
        self.assertIn("ExitOnForwardFailure=yes", body)
        self.assertIn(
            "127.0.0.1:__DGX_LOCAL_PORT__:127.0.0.1:__DGX_REMOTE_PORT__", body
        )
        self.assertNotIn("0.0.0.0", body)
        self.assertNotIn("StrictHostKeyChecking=no", body)

    def test_remote_worker_does_not_require_a_local_control_plane(self) -> None:
        body = (ROOT / "deploy" / "systemd" / "machinist-worker.service").read_text(
            encoding="utf-8"
        )
        self.assertNotIn("Requires=machinist-control-plane.service", body)
        self.assertNotIn("After=machinist-control-plane.service", body)

    def test_fleet_tunnel_is_loopback_only_and_fails_closed(self) -> None:
        body = (
            ROOT / "deploy" / "systemd" / "machinist-fleet-tunnel@.service"
        ).read_text(encoding="utf-8")
        for required in (
            "BatchMode=yes",
            "StrictHostKeyChecking=yes",
            "ExitOnForwardFailure=yes",
            "-L 127.0.0.1:7331:127.0.0.1:7331",
            "-L 127.0.0.1:7900:127.0.0.1:7900",
            "NoNewPrivileges=true",
        ):
            self.assertIn(required, body)
        self.assertNotIn("StrictHostKeyChecking=no", body)
        self.assertNotIn("0.0.0.0", body)


if __name__ == "__main__":
    unittest.main()
