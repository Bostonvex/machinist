"""Compatibility tests for the thin DeepCode process and Herdr adapters."""

import json
import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "plugins" / "herdr-machinist" / "scripts"
SESSION_HELPER = SCRIPTS / "deepcode-session.mjs"
PROCESS_WRAPPER = SCRIPTS / "run-deepcode.sh"


class DeepCodeBridgeTests(unittest.TestCase):
    def test_project_code_matches_deepcode_long_path_contract(self) -> None:
        output = subprocess.check_output(
            ["node", str(SESSION_HELPER), "project-code", "/a/very/long/project/path/" + "x" * 100],
            text=True,
        ).strip()
        self.assertLessEqual(len(output), 64)
        self.assertRegex(output, r"^x+-[0-9a-f]{16}$")

    def test_process_wrapper_forwards_prompt_and_reports_usage(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            project = root / "project"
            project.mkdir()
            state_root = root / "state"
            token_path = root / "tokens"
            invocation_path = root / "invocation.json"
            project_code = subprocess.check_output(
                ["node", str(SESSION_HELPER), "project-code", str(project.resolve())], text=True
            ).strip()
            index_path = state_root / "projects" / project_code / "sessions-index.json"
            fake = root / "deepcode-fake.mjs"
            fake.write_text(
                """#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
const invocation = process.env.MACHINIST_TEST_INVOCATION;
fs.writeFileSync(invocation, JSON.stringify({ argv: process.argv.slice(2), model: process.env.DEEPCODE_MODEL }));
const indexPath = process.env.MACHINIST_TEST_INDEX;
fs.mkdirSync(path.dirname(indexPath), { recursive: true });
fs.writeFileSync(indexPath, JSON.stringify({ entries: [{
  id: "session-new", status: "completed", createTime: new Date().toISOString(),
  updateTime: new Date().toISOString(), usage: { total_tokens: 123 }
}] }));
process.stdout.write("fake completion\\n");
""",
                encoding="utf-8",
            )
            fake.chmod(0o755)
            environment = os.environ.copy()
            environment.update(
                {
                    "DEEPCODE_BIN_PATH": str(fake),
                    "MACHINIST_DEEPCODE_HOME": str(state_root),
                    "MACHINIST_TOKEN_USAGE_PATH": str(token_path),
                    "MACHINIST_TEST_INDEX": str(index_path),
                    "MACHINIST_TEST_INVOCATION": str(invocation_path),
                }
            )
            result = subprocess.run(
                [str(PROCESS_WRAPPER), "--model=ds-test"],
                cwd=project,
                env=environment,
                input="perform the task\n",
                text=True,
                capture_output=True,
                check=True,
            )
            invocation = json.loads(invocation_path.read_text(encoding="utf-8"))
            self.assertEqual(result.stdout, "fake completion\n")
            self.assertEqual(invocation["argv"], ["--exec", "--prompt", "perform the task"])
            self.assertEqual(invocation["model"], "ds-test")
            self.assertEqual(token_path.read_text(encoding="utf-8"), "123")

    def test_observer_maps_deepcode_states_to_herdr(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            project = root / "project"
            project.mkdir()
            state_root = root / "state"
            calls = root / "herdr-calls.txt"
            fake_herdr = root / "herdr-fake.sh"
            fake_herdr.write_text(
                '#!/bin/sh\nprintf "%s\\n" "$*" >> "$MACHINIST_TEST_HERDR_CALLS"\n',
                encoding="utf-8",
            )
            fake_herdr.chmod(0o755)
            project_code = subprocess.check_output(
                ["node", str(SESSION_HELPER), "project-code", str(project.resolve())], text=True
            ).strip()
            index_path = state_root / "projects" / project_code / "sessions-index.json"
            index_path.parent.mkdir(parents=True)
            index_path.write_text('{"entries": []}', encoding="utf-8")
            environment = os.environ.copy()
            environment.update(
                {
                    "HERDR_ENV": "1",
                    "HERDR_PANE_ID": "w1:p1",
                    "HERDR_BIN_PATH": str(fake_herdr),
                    "MACHINIST_DEEPCODE_HOME": str(state_root),
                    "MACHINIST_TEST_HERDR_CALLS": str(calls),
                    "DEEPCODE_MODEL": "ds-test",
                }
            )
            process = subprocess.Popen(
                ["node", str(SESSION_HELPER), "observe", str(project.resolve())], env=environment
            )
            try:
                time.sleep(0.4)
                self._write_session(index_path, "processing")
                time.sleep(0.4)
                self._write_session(index_path, "ask_permission")
                time.sleep(0.4)
                self._write_session(index_path, "completed")
                time.sleep(0.4)
            finally:
                process.terminate()
                process.wait(timeout=3)
            body = calls.read_text(encoding="utf-8")
            self.assertIn("--state idle", body)
            self.assertIn("--state working", body)
            self.assertIn("--state blocked", body)
            self.assertIn("--agent-session-id session-1", body)
            self.assertIn("pane release-agent w1:p1", body)

    @staticmethod
    def _write_session(index_path: Path, status: str) -> None:
        now = time.time_ns()
        index_path.write_text(
            json.dumps(
                {
                    "entries": [
                        {
                            "id": "session-1",
                            "status": status,
                            "createTime": "2026-01-01T00:00:00Z",
                            "updateTime": f"2026-01-01T00:00:00.{now}Z",
                        }
                    ]
                }
            ),
            encoding="utf-8",
        )


if __name__ == "__main__":
    unittest.main()
