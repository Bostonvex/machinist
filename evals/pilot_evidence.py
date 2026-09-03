"""Capture auditable Buzz and Machinist cutover measurements.

The cutover evaluator deliberately accepts a small portable JSONL contract. This
module builds those records from operational evidence without treating missing
token usage as zero or inferring acceptance from process exit alone.
"""

from __future__ import annotations

import argparse
from collections import defaultdict
from contextlib import closing
from datetime import datetime, timezone
import json
import math
import os
from pathlib import Path
import sqlite3
import sys
import tempfile
from typing import Any, Iterable, Sequence
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


class PilotEvidenceError(ValueError):
    """Raised when source evidence is missing, ambiguous, or unsafe to compare."""


TERMINAL_JOB_STATES = frozenset(("succeeded", "failed", "cancelled"))


def _integer(value: Any, field: str) -> int:
    if isinstance(value, bool):
        raise PilotEvidenceError(f"{field} must be a non-negative integer")
    if isinstance(value, str):
        try:
            value = int(value)
        except ValueError as error:
            raise PilotEvidenceError(f"{field} must be a non-negative integer") from error
    if not isinstance(value, int) or value < 0:
        raise PilotEvidenceError(f"{field} must be a non-negative integer")
    return value


def _timestamp(value: Any, field: str) -> datetime:
    if not isinstance(value, str) or not value.strip():
        raise PilotEvidenceError(f"{field} must be an ISO-8601 timestamp")
    raw = value.strip()
    if raw.endswith("Z"):
        raw = raw[:-1] + "+00:00"
    try:
        result = datetime.fromisoformat(raw)
    except ValueError as error:
        raise PilotEvidenceError(f"{field} must be an ISO-8601 timestamp") from error
    if result.tzinfo is None:
        raise PilotEvidenceError(f"{field} must include a timezone")
    return result.astimezone(timezone.utc)


def _recorded_at() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def _canonical_timestamp(value: str, field: str) -> str:
    return _timestamp(value, field).isoformat().replace("+00:00", "Z")


def _validate_human_fields(
    *,
    task_id: str,
    accepted: bool,
    repair_attempts: int,
    operator_touches: int,
    unattended: bool,
) -> None:
    if not task_id.strip():
        raise PilotEvidenceError("task_id must not be empty")
    _integer(repair_attempts, "repair_attempts")
    _integer(operator_touches, "operator_touches")
    if unattended and operator_touches != 0:
        raise PilotEvidenceError(
            "unattended evidence cannot contain operator touches"
        )
    if not isinstance(accepted, bool) or not isinstance(unattended, bool):
        raise PilotEvidenceError("accepted and unattended must be booleans")


def _token_value(attributes: Any) -> int | None:
    if not isinstance(attributes, dict):
        return None
    try:
        input_tokens = _integer(attributes.get("input_tokens"), "input_tokens")
        output_tokens = _integer(attributes.get("output_tokens"), "output_tokens")
    except PilotEvidenceError:
        return None
    return input_tokens + output_tokens


def _read_payload(raw: Any) -> dict[str, Any] | None:
    if not isinstance(raw, str):
        return None
    try:
        value = json.loads(raw)
    except json.JSONDecodeError:
        return None
    return value if isinstance(value, dict) else None


def _open_buzz(path: Path) -> sqlite3.Connection:
    resolved = path.expanduser().resolve()
    if not resolved.is_file():
        raise PilotEvidenceError(f"Buzz telemetry database does not exist: {resolved}")
    try:
        connection = sqlite3.connect(f"{resolved.as_uri()}?mode=ro", uri=True)
    except sqlite3.Error as error:
        raise PilotEvidenceError(f"open Buzz telemetry database: {error}") from error
    connection.row_factory = sqlite3.Row
    return connection


def _turn_rows(
    connection: sqlite3.Connection,
    *,
    turn_ids: Sequence[str] | None = None,
    since: str | None = None,
    until: str | None = None,
) -> list[sqlite3.Row]:
    conditions: list[str] = []
    values: list[str] = []
    if turn_ids is not None:
        if not turn_ids:
            return []
        conditions.append("t.id IN (" + ",".join("?" for _ in turn_ids) + ")")
        values.extend(turn_ids)
    if since is not None:
        conditions.append("t.started_at >= ?")
        values.append(_canonical_timestamp(since, "since"))
    if until is not None:
        conditions.append("t.started_at <= ?")
        values.append(_canonical_timestamp(until, "until"))
    where = " WHERE " + " AND ".join(conditions) if conditions else ""
    try:
        return connection.execute(
            "SELECT t.id,t.agent_id,a.display_name,t.session_id,t.started_at,"
            "t.ended_at,t.outcome,t.duration_ms,t.tool_count,t.measurement_quality,"
            "t.harness,t.model,t.endpoint_id "
            "FROM turns t JOIN agents a ON a.id=t.agent_id"
            + where
            + " ORDER BY t.started_at,t.id",
            values,
        ).fetchall()
    except sqlite3.Error as error:
        raise PilotEvidenceError(f"read Buzz turns: {error}") from error


def _event_evidence(
    connection: sqlite3.Connection, turn_ids: Sequence[str]
) -> tuple[dict[str, list[int | None]], dict[str, int]]:
    tokens: dict[str, list[int | None]] = defaultdict(list)
    occupancy: dict[str, int] = {}
    if not turn_ids:
        return tokens, occupancy
    placeholders = ",".join("?" for _ in turn_ids)
    try:
        rows = connection.execute(
            "SELECT turn_id,event_type,safe_payload_json FROM events "
            f"WHERE turn_id IN ({placeholders}) "
            "AND event_type IN ('model.completed','usage.updated')",
            turn_ids,
        )
        for row in rows:
            payload = _read_payload(row["safe_payload_json"])
            attributes = payload.get("attributes") if payload is not None else None
            if row["event_type"] == "model.completed":
                tokens[row["turn_id"]].append(_token_value(attributes))
                continue
            if not isinstance(attributes, dict):
                continue
            if (
                attributes.get("token_kind") == "context_occupancy"
                and isinstance(attributes.get("value"), int)
                and not isinstance(attributes.get("value"), bool)
                and attributes["value"] >= 0
            ):
                occupancy[row["turn_id"]] = max(
                    occupancy.get(row["turn_id"], 0), attributes["value"]
                )
    except sqlite3.Error as error:
        raise PilotEvidenceError(f"read Buzz usage events: {error}") from error
    return tokens, occupancy


def buzz_inventory(
    database: Path, *, since: str | None = None, until: str | None = None
) -> list[dict[str, Any]]:
    """Return task-unbound Buzz turn evidence for later human correlation."""

    with closing(_open_buzz(database)) as connection:
        turns = _turn_rows(connection, since=since, until=until)
        token_events, occupancy = _event_evidence(
            connection, [row["id"] for row in turns]
        )
    inventory: list[dict[str, Any]] = []
    for row in turns:
        values = token_events.get(row["id"], [])
        exact_tokens = sum(value for value in values if value is not None)
        token_usage = exact_tokens if values and all(value is not None for value in values) else None
        duration = row["duration_ms"]
        inventory.append(
            {
                "kind": "buzz_turn_inventory",
                "turn_id": row["id"],
                "session_id": row["session_id"],
                "agent_id": row["agent_id"],
                "agent": row["display_name"],
                "harness": row["harness"],
                "model": row["model"],
                "endpoint_id": row["endpoint_id"],
                "started_at": row["started_at"],
                "ended_at": row["ended_at"],
                "outcome": row["outcome"],
                "elapsed_seconds": (
                    float(duration) / 1000.0 if duration is not None else None
                ),
                "tool_count": row["tool_count"],
                "measurement_quality": row["measurement_quality"],
                "model_request_count": len(values),
                "token_usage": token_usage,
                "token_source": (
                    "model.completed.aggregate" if token_usage is not None else None
                ),
                "max_context_occupancy": occupancy.get(row["id"]),
                "eligible_for_pairing": False,
            }
        )
    return inventory


def buzz_measurement(
    database: Path,
    *,
    turn_ids: Sequence[str],
    task_id: str,
    elapsed_seconds: float,
    accepted: bool,
    repair_attempts: int,
    operator_touches: int,
    unattended: bool,
) -> dict[str, Any]:
    """Bind explicitly selected Buzz turns to one human-validated task."""

    _validate_human_fields(
        task_id=task_id,
        accepted=accepted,
        repair_attempts=repair_attempts,
        operator_touches=operator_touches,
        unattended=unattended,
    )
    if not turn_ids or any(not value.strip() for value in turn_ids):
        raise PilotEvidenceError("at least one non-empty Buzz turn ID is required")
    if len(set(turn_ids)) != len(turn_ids):
        raise PilotEvidenceError("Buzz turn IDs must be unique")
    if not math.isfinite(elapsed_seconds) or elapsed_seconds < 0:
        raise PilotEvidenceError("elapsed_seconds must be finite and non-negative")
    with closing(_open_buzz(database)) as connection:
        turns = _turn_rows(connection, turn_ids=turn_ids)
        by_id = {row["id"]: row for row in turns}
        missing = [turn_id for turn_id in turn_ids if turn_id not in by_id]
        if missing:
            raise PilotEvidenceError(
                "Buzz telemetry is missing turn IDs: " + ", ".join(missing)
            )
        token_events, occupancy = _event_evidence(connection, turn_ids)
    values = [value for turn_id in turn_ids for value in token_events.get(turn_id, [])]
    token_usage = (
        sum(value for value in values if value is not None)
        if values and all(value is not None for value in values)
        else None
    )
    ordered = [by_id[turn_id] for turn_id in turn_ids]
    return {
        "task_id": task_id.strip(),
        "system": "buzz",
        "accepted": accepted,
        "elapsed_seconds": float(elapsed_seconds),
        "token_usage": token_usage,
        "repair_attempts": repair_attempts,
        "operator_touches": operator_touches,
        "unattended": unattended,
        "evidence": {
            "recorded_at": _recorded_at(),
            "token_source": (
                "model.completed.aggregate" if token_usage is not None else None
            ),
            "turns": [
                {
                    "turn_id": row["id"],
                    "session_id": row["session_id"],
                    "agent": row["display_name"],
                    "started_at": row["started_at"],
                    "ended_at": row["ended_at"],
                    "outcome": row["outcome"],
                    "harness": row["harness"],
                    "model": row["model"],
                    "model_request_count": len(token_events.get(row["id"], [])),
                    "max_context_occupancy": occupancy.get(row["id"]),
                }
                for row in ordered
            ],
        },
    }


def fetch_machinist_status(endpoint: str, timeout: float = 10.0) -> dict[str, Any]:
    url = endpoint.rstrip("/") + "/api/v1/status"
    request = Request(url, headers={"Accept": "application/json"})
    try:
        with urlopen(request, timeout=timeout) as response:
            body = response.read()
    except (HTTPError, URLError, OSError) as error:
        raise PilotEvidenceError(f"read Machinist status from {url}: {error}") from error
    try:
        value = json.loads(body)
    except json.JSONDecodeError as error:
        raise PilotEvidenceError("Machinist status is not valid JSON") from error
    if not isinstance(value, dict):
        raise PilotEvidenceError("Machinist status must be an object")
    return value


def machinist_measurement(
    status: dict[str, Any],
    *,
    endpoint: str,
    job_id: str,
    task_id: str,
    accepted: bool,
    repair_attempts: int,
    operator_touches: int,
    unattended: bool,
) -> dict[str, Any]:
    """Build a candidate record, aggregating usage across every attempt."""

    _validate_human_fields(
        task_id=task_id,
        accepted=accepted,
        repair_attempts=repair_attempts,
        operator_touches=operator_touches,
        unattended=unattended,
    )
    jobs = status.get("jobs")
    if not isinstance(jobs, list):
        raise PilotEvidenceError("Machinist status does not contain a jobs list")
    matches = [job for job in jobs if isinstance(job, dict) and job.get("id") == job_id]
    if len(matches) != 1:
        raise PilotEvidenceError(f"Machinist status does not contain job {job_id!r}")
    job = matches[0]
    state = job.get("state")
    if state not in TERMINAL_JOB_STATES:
        raise PilotEvidenceError(f"Machinist job {job_id!r} is not terminal: {state!r}")
    if accepted and state != "succeeded":
        raise PilotEvidenceError("a failed or cancelled Machinist job cannot be accepted")
    created = _timestamp(job.get("created_at"), "job.created_at")
    updated = _timestamp(job.get("updated_at"), "job.updated_at")
    elapsed = (updated - created).total_seconds()
    if elapsed < 0:
        raise PilotEvidenceError("Machinist job update precedes its creation")
    runs = job.get("runs")
    if not isinstance(runs, list) or not runs:
        raise PilotEvidenceError(f"Machinist job {job_id!r} contains no runs")
    attempts: list[dict[str, Any]] = []
    for run in runs:
        if not isinstance(run, dict) or not isinstance(run.get("attempts"), list):
            raise PilotEvidenceError("Machinist run is missing attempt evidence")
        if any(not isinstance(attempt, dict) for attempt in run["attempts"]):
            raise PilotEvidenceError("Machinist attempt evidence must contain objects")
        attempts.extend(run["attempts"])
    if not attempts:
        raise PilotEvidenceError(f"Machinist job {job_id!r} contains no attempts")
    token_values: list[int | None] = []
    for attempt in attempts:
        raw = attempt.get("token_usage")
        token_values.append(None if raw is None else _integer(raw, "attempt.token_usage"))
    token_usage = (
        sum(value for value in token_values if value is not None)
        if all(value is not None for value in token_values)
        else None
    )
    return {
        "task_id": task_id.strip(),
        "system": "machinist",
        "accepted": accepted,
        "elapsed_seconds": elapsed,
        "token_usage": token_usage,
        "repair_attempts": repair_attempts,
        "operator_touches": operator_touches,
        "unattended": unattended,
        "evidence": {
            "recorded_at": _recorded_at(),
            "endpoint": endpoint.rstrip("/"),
            "job_id": job_id,
            "job_state": state,
            "repository": job.get("repository"),
            "command": job.get("command"),
            "runs": [run.get("id") for run in runs if isinstance(run, dict)],
            "attempts": [
                {
                    "attempt_id": attempt.get("id"),
                    "number": attempt.get("number"),
                    "state": attempt.get("state"),
                    "profile": attempt.get("profile"),
                    "harness": attempt.get("harness"),
                    "provider": attempt.get("provider"),
                    "model": attempt.get("model"),
                    "worker": attempt.get("worker_name"),
                    "token_usage": attempt.get("token_usage"),
                }
                for attempt in attempts
            ],
            "token_source": (
                "machinist.attempts.aggregate" if token_usage is not None else None
            ),
        },
    }


def _atomic_jsonl(path: Path, records: Iterable[dict[str, Any]]) -> None:
    destination = path.expanduser().resolve()
    destination.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(
        prefix=destination.name + ".", suffix=".tmp", dir=destination.parent
    )
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            for record in records:
                stream.write(json.dumps(record, sort_keys=True, separators=(",", ":")))
                stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, destination)
    except BaseException:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise


def write_measurement(path: Path, record: dict[str, Any], *, replace: bool = False) -> None:
    """Atomically append or explicitly replace one task/system record."""

    destination = path.expanduser().resolve()
    records: list[dict[str, Any]] = []
    if destination.exists():
        try:
            lines = destination.read_text(encoding="utf-8").splitlines()
        except OSError as error:
            raise PilotEvidenceError(f"read {destination}: {error}") from error
        for line_number, raw in enumerate(lines, start=1):
            if not raw.strip() or raw.lstrip().startswith("#"):
                continue
            try:
                value = json.loads(raw)
            except json.JSONDecodeError as error:
                raise PilotEvidenceError(
                    f"{destination}:{line_number}: invalid JSON"
                ) from error
            if not isinstance(value, dict):
                raise PilotEvidenceError(
                    f"{destination}:{line_number}: record must be an object"
                )
            records.append(value)
    key = (record.get("task_id"), record.get("system"))
    indexes = [
        index
        for index, existing in enumerate(records)
        if (existing.get("task_id"), existing.get("system")) == key
    ]
    if indexes and not replace:
        raise PilotEvidenceError(
            f"measurement for task {key[0]!r} and system {key[1]!r} already exists"
        )
    if len(indexes) > 1:
        raise PilotEvidenceError(
            f"measurement file already contains duplicate task/system key {key!r}"
        )
    if indexes:
        records[indexes[0]] = record
    else:
        records.append(record)
    _atomic_jsonl(destination, records)


def _human_flags(command: argparse.ArgumentParser) -> None:
    command.add_argument("--task-id", required=True)
    command.add_argument("--repair-attempts", required=True, type=int)
    command.add_argument("--operator-touches", required=True, type=int)
    quality = command.add_mutually_exclusive_group(required=True)
    quality.add_argument("--accepted", action="store_true")
    quality.add_argument("--rejected", action="store_true")
    attendance = command.add_mutually_exclusive_group(required=True)
    attendance.add_argument("--unattended", action="store_true")
    attendance.add_argument("--attended", action="store_true")
    command.add_argument("--output", required=True, type=Path)
    command.add_argument("--replace", action="store_true")


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="action", required=True)

    inventory = commands.add_parser(
        "buzz-inventory", help="export task-unbound Buzz turn evidence"
    )
    inventory.add_argument("--database", required=True, type=Path)
    inventory.add_argument("--since")
    inventory.add_argument("--until")
    inventory.add_argument("--output", required=True, type=Path)

    buzz = commands.add_parser(
        "record-buzz", help="bind selected Buzz turns to one benchmark task"
    )
    buzz.add_argument("--database", required=True, type=Path)
    buzz.add_argument("--turn-id", required=True, action="append")
    buzz.add_argument("--elapsed-seconds", required=True, type=float)
    _human_flags(buzz)

    machinist = commands.add_parser(
        "record-machinist", help="capture all attempts for one terminal Machinist job"
    )
    machinist.add_argument("--endpoint", default="http://127.0.0.1:7331")
    machinist.add_argument("--job-id", required=True)
    machinist.add_argument("--timeout", type=float, default=10.0)
    _human_flags(machinist)
    return result


def main(argv: Sequence[str] | None = None) -> int:
    arguments = parser().parse_args(argv)
    try:
        if arguments.action == "buzz-inventory":
            records = buzz_inventory(
                arguments.database, since=arguments.since, until=arguments.until
            )
            _atomic_jsonl(arguments.output, records)
            print(f"wrote {len(records)} Buzz turns to {arguments.output}")
            return 0
        common = {
            "task_id": arguments.task_id,
            "accepted": arguments.accepted,
            "repair_attempts": arguments.repair_attempts,
            "operator_touches": arguments.operator_touches,
            "unattended": arguments.unattended,
        }
        if arguments.action == "record-buzz":
            record = buzz_measurement(
                arguments.database,
                turn_ids=arguments.turn_id,
                elapsed_seconds=arguments.elapsed_seconds,
                **common,
            )
        else:
            status = fetch_machinist_status(arguments.endpoint, arguments.timeout)
            record = machinist_measurement(
                status,
                endpoint=arguments.endpoint,
                job_id=arguments.job_id,
                **common,
            )
        write_measurement(arguments.output, record, replace=arguments.replace)
        print(
            f"recorded {record['system']} measurement for {record['task_id']} "
            f"in {arguments.output}"
        )
        return 0
    except PilotEvidenceError as error:
        print(error, file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
