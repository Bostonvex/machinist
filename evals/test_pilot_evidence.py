"""Tests for fail-closed cutover evidence capture."""

from __future__ import annotations

import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

from evals.pilot_evidence import (
    PilotEvidenceError,
    buzz_inventory,
    buzz_measurement,
    machinist_measurement,
    write_measurement,
)


class PilotEvidenceTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.database = self.root / "telemetry.sqlite3"
        connection = sqlite3.connect(self.database)
        connection.executescript(
            """
            CREATE TABLE agents (
                id TEXT PRIMARY KEY,
                display_name TEXT NOT NULL
            );
            CREATE TABLE turns (
                id TEXT PRIMARY KEY,
                agent_id TEXT NOT NULL,
                session_id TEXT,
                started_at TEXT NOT NULL,
                ended_at TEXT,
                outcome TEXT,
                duration_ms REAL,
                tool_count INTEGER,
                measurement_quality TEXT,
                harness TEXT,
                model TEXT,
                endpoint_id TEXT
            );
            CREATE TABLE events (
                event_id TEXT PRIMARY KEY,
                event_type TEXT NOT NULL,
                turn_id TEXT,
                safe_payload_json TEXT NOT NULL
            );
            INSERT INTO agents(id,display_name) VALUES('agent-1','Builder');
            INSERT INTO turns VALUES(
                'turn-1','agent-1','session-1','2026-09-01T00:00:00Z',
                '2026-09-01T00:00:10Z','completed',10000,2,'exact',
                'deepseek','ds-0731','local'
            );
            INSERT INTO turns VALUES(
                'turn-2','agent-1','session-1','2026-09-01T00:01:00Z',
                '2026-09-01T00:01:05Z','completed',5000,1,'exact',
                'qwen-code','ds-0731','local'
            );
            """
        )
        connection.execute(
            "INSERT INTO events VALUES(?,?,?,?)",
            (
                "event-1",
                "model.completed",
                "turn-1",
                json.dumps({"attributes": {"input_tokens": 100, "output_tokens": 20}}),
            ),
        )
        connection.execute(
            "INSERT INTO events VALUES(?,?,?,?)",
            (
                "event-2",
                "model.completed",
                "turn-1",
                json.dumps({"attributes": {"input_tokens": 80, "output_tokens": 10}}),
            ),
        )
        connection.execute(
            "INSERT INTO events VALUES(?,?,?,?)",
            (
                "event-3",
                "usage.updated",
                "turn-2",
                json.dumps(
                    {
                        "attributes": {
                            "token_kind": "context_occupancy",
                            "value": 500,
                        }
                    }
                ),
            ),
        )
        connection.commit()
        connection.close()

    def test_inventory_aggregates_requests_but_not_context_occupancy(self) -> None:
        records = buzz_inventory(
            self.database,
            since="2026-08-31T20:00:00-04:00",
            until="2026-09-01T00:02:00Z",
        )
        self.assertEqual(records[0]["token_usage"], 210)
        self.assertEqual(records[0]["model_request_count"], 2)
        self.assertIsNone(records[1]["token_usage"])
        self.assertEqual(records[1]["max_context_occupancy"], 500)
        self.assertFalse(records[0]["eligible_for_pairing"])

        self.assertEqual(
            buzz_inventory(self.database, since="2026-09-01T00:02:00Z"), []
        )

    def test_buzz_measurement_requires_explicit_turn_binding(self) -> None:
        record = buzz_measurement(
            self.database,
            turn_ids=["turn-1"],
            task_id="change-1",
            elapsed_seconds=12.5,
            accepted=True,
            repair_attempts=1,
            operator_touches=2,
            unattended=False,
        )
        self.assertEqual(record["token_usage"], 210)
        self.assertEqual(record["evidence"]["turns"][0]["turn_id"], "turn-1")
        with self.assertRaisesRegex(PilotEvidenceError, "missing turn IDs"):
            buzz_measurement(
                self.database,
                turn_ids=["missing"],
                task_id="change-1",
                elapsed_seconds=1,
                accepted=False,
                repair_attempts=0,
                operator_touches=0,
                unattended=True,
            )

    def test_machinist_measurement_aggregates_all_attempts(self) -> None:
        status = {
            "jobs": [
                {
                    "id": "job-1",
                    "state": "succeeded",
                    "created_at": "2026-09-01T00:00:00Z",
                    "updated_at": "2026-09-01T00:00:30Z",
                    "repository": "machinist",
                    "command": "implement",
                    "runs": [
                        {
                            "id": "run-1",
                            "attempts": [
                                {"id": "attempt-1", "token_usage": "100"},
                                {"id": "attempt-2", "token_usage": "60"},
                            ],
                        }
                    ],
                }
            ]
        }
        record = machinist_measurement(
            status,
            endpoint="http://127.0.0.1:7331/",
            job_id="job-1",
            task_id="change-1",
            accepted=True,
            repair_attempts=1,
            operator_touches=0,
            unattended=True,
        )
        self.assertEqual(record["elapsed_seconds"], 30)
        self.assertEqual(record["token_usage"], 160)
        self.assertEqual(len(record["evidence"]["attempts"]), 2)

        status["jobs"][0]["runs"][0]["attempts"][1]["token_usage"] = None
        record = machinist_measurement(
            status,
            endpoint="http://127.0.0.1:7331",
            job_id="job-1",
            task_id="change-1",
            accepted=True,
            repair_attempts=1,
            operator_touches=0,
            unattended=True,
        )
        self.assertIsNone(record["token_usage"])

    def test_measurement_write_rejects_implicit_overwrite(self) -> None:
        path = self.root / "measurements.jsonl"
        record = {"task_id": "change-1", "system": "buzz"}
        write_measurement(path, record)
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        with self.assertRaisesRegex(PilotEvidenceError, "already exists"):
            write_measurement(path, record)
        replacement = {**record, "accepted": True}
        write_measurement(path, replacement, replace=True)
        self.assertEqual(json.loads(path.read_text())["accepted"], True)

    def test_unattended_record_cannot_have_touches(self) -> None:
        with self.assertRaisesRegex(PilotEvidenceError, "operator touches"):
            buzz_measurement(
                self.database,
                turn_ids=["turn-1"],
                task_id="change-1",
                elapsed_seconds=1,
                accepted=True,
                repair_attempts=0,
                operator_touches=1,
                unattended=True,
            )

    def test_buzz_elapsed_must_be_finite(self) -> None:
        with self.assertRaisesRegex(PilotEvidenceError, "finite"):
            buzz_measurement(
                self.database,
                turn_ids=["turn-1"],
                task_id="change-1",
                elapsed_seconds=float("nan"),
                accepted=True,
                repair_attempts=0,
                operator_touches=0,
                unattended=True,
            )


if __name__ == "__main__":
    unittest.main()
