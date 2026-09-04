"""Regression tests for session-isolated Herdr plugin worker state."""

import os
import signal
import subprocess
import tempfile
import time
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "plugins" / "herdr-machinist" / "scripts"


class HerdrPluginWorkerTests(unittest.TestCase):
    def test_named_sessions_keep_distinct_worker_pid_files(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state = root / "state"
            configs = root / "configs"
            configs.mkdir()
            (configs / "claude.toml").write_text("name='claude'\n", encoding="utf-8")
            (configs / "codex.toml").write_text("name='codex'\n", encoding="utf-8")
            fake = root / "machinist-fake.sh"
            fake.write_text(
                "#!/bin/sh\ntrap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n",
                encoding="utf-8",
            )
            fake.chmod(0o755)
            environments = {
                name: {
                    **os.environ,
                    "HERDR_PLUGIN_STATE_DIR": str(state),
                    "HERDR_SOCKET_PATH": str(root / "sessions" / name / "herdr.sock"),
                    "MACHINIST_HERDR_CONFIG_DIR": str(configs),
                    "MACHINIST_BIN": str(fake),
                }
                for name in ("claude", "codex")
            }
            pids = []
            try:
                for environment in environments.values():
                    subprocess.run([str(SCRIPTS / "start-worker.sh")], env=environment, check=True)
                claude_pid_file = state / "sessions" / "claude" / "worker.pid"
                codex_pid_file = state / "sessions" / "codex" / "worker.pid"
                self.assertTrue(claude_pid_file.is_file())
                self.assertTrue(codex_pid_file.is_file())
                pids = [int(claude_pid_file.read_text()), int(codex_pid_file.read_text())]
                self.assertNotEqual(pids[0], pids[1])

                subprocess.run([str(SCRIPTS / "stop-worker.sh")], env=environments["claude"], check=True)
                self.assertFalse(claude_pid_file.exists())
                self.assertTrue(codex_pid_file.exists())
            finally:
                for environment in environments.values():
                    subprocess.run([str(SCRIPTS / "stop-worker.sh")], env=environment, check=False)
                for pid in pids:
                    try:
                        os.kill(pid, signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                time.sleep(0.05)


if __name__ == "__main__":
    unittest.main()
