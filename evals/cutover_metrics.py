"""Evaluate paired Buzz and Machinist cutover measurements."""

from __future__ import annotations

import argparse
from dataclasses import asdict, dataclass
import json
import math
from pathlib import Path
import statistics
import sys
from typing import Any, Iterable, Sequence


class CutoverDataError(ValueError):
    """Raised when benchmark evidence is incomplete or malformed."""


@dataclass(frozen=True)
class Thresholds:
    minimum_paired_tasks: int = 10
    minimum_speed_reduction_percent: float = 30.0
    minimum_token_reduction_percent: float = 40.0
    minimum_rework_reduction_percent: float = 30.0
    minimum_token_coverage_percent: float = 95.0
    minimum_unattended_rate_percent: float = 90.0


@dataclass(frozen=True)
class Measurement:
    task_id: str
    system: str
    accepted: bool
    elapsed_seconds: float
    token_usage: int | None
    repair_attempts: int
    operator_touches: int
    unattended: bool


def _number(value: Any, field: str, line: int) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise CutoverDataError(f"line {line}: {field} must be a number")
    result = float(value)
    if not math.isfinite(result) or result < 0:
        raise CutoverDataError(f"line {line}: {field} must be finite and non-negative")
    return result


def _integer(value: Any, field: str, line: int) -> int:
    number = _number(value, field, line)
    if not number.is_integer():
        raise CutoverDataError(f"line {line}: {field} must be an integer")
    return int(number)


def parse_measurement(value: Any, line: int) -> Measurement:
    if not isinstance(value, dict):
        raise CutoverDataError(f"line {line}: record must be an object")
    task_id = value.get("task_id")
    system = value.get("system")
    if not isinstance(task_id, str) or not task_id.strip():
        raise CutoverDataError(f"line {line}: task_id must be a non-empty string")
    if not isinstance(system, str) or not system.strip():
        raise CutoverDataError(f"line {line}: system must be a non-empty string")
    for field in ("accepted", "unattended"):
        if not isinstance(value.get(field), bool):
            raise CutoverDataError(f"line {line}: {field} must be a boolean")
    raw_tokens = value.get("token_usage")
    token_usage = None
    if raw_tokens is not None:
        token_usage = _integer(raw_tokens, "token_usage", line)
    return Measurement(
        task_id=task_id.strip(),
        system=system.strip(),
        accepted=value["accepted"],
        elapsed_seconds=_number(value.get("elapsed_seconds"), "elapsed_seconds", line),
        token_usage=token_usage,
        repair_attempts=_integer(value.get("repair_attempts"), "repair_attempts", line),
        operator_touches=_integer(value.get("operator_touches"), "operator_touches", line),
        unattended=value["unattended"],
    )


def load_jsonl(path: Path) -> list[Measurement]:
    records: list[Measurement] = []
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as error:
        raise CutoverDataError(f"read {path}: {error}") from error
    for line_number, raw in enumerate(lines, start=1):
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        try:
            value = json.loads(raw)
        except json.JSONDecodeError as error:
            raise CutoverDataError(f"line {line_number}: invalid JSON: {error.msg}") from error
        records.append(parse_measurement(value, line_number))
    if not records:
        raise CutoverDataError("benchmark contains no records")
    return records


def pair_measurements(
    records: Iterable[Measurement], baseline: str, candidate: str
) -> list[tuple[Measurement, Measurement]]:
    if baseline == candidate:
        raise CutoverDataError("baseline and candidate system names must differ")
    indexed: dict[tuple[str, str], Measurement] = {}
    task_ids: set[str] = set()
    for record in records:
        if record.system not in (baseline, candidate):
            raise CutoverDataError(
                f"task {record.task_id!r}: unexpected system {record.system!r}"
            )
        key = (record.task_id, record.system)
        if key in indexed:
            raise CutoverDataError(
                f"task {record.task_id!r}: duplicate {record.system!r} measurement"
            )
        indexed[key] = record
        task_ids.add(record.task_id)
    pairs: list[tuple[Measurement, Measurement]] = []
    for task_id in sorted(task_ids):
        missing = [
            system for system in (baseline, candidate) if (task_id, system) not in indexed
        ]
        if missing:
            raise CutoverDataError(
                f"task {task_id!r}: missing measurement for {', '.join(missing)}"
            )
        pairs.append((indexed[(task_id, baseline)], indexed[(task_id, candidate)]))
    return pairs


def _median(values: Iterable[float | int]) -> float | None:
    collected = list(values)
    return float(statistics.median(collected)) if collected else None


def _mean(values: Iterable[float | int]) -> float | None:
    collected = list(values)
    return float(statistics.fmean(collected)) if collected else None


def _percent(numerator: int, denominator: int) -> float:
    return 100.0 * numerator / denominator if denominator else 0.0


def _reduction(baseline: float | None, candidate: float | None) -> float | None:
    if baseline is None or candidate is None or baseline == 0:
        return None
    return 100.0 * (baseline - candidate) / baseline


def _reduction_gate(
    baseline: float | None, candidate: float | None, reduction: float | None, minimum: float
) -> bool:
    if baseline is None or candidate is None:
        return False
    if baseline == 0:
        return candidate == 0
    return reduction is not None and reduction >= minimum


def _system_summary(records: list[Measurement]) -> dict[str, Any]:
    accepted = [record for record in records if record.accepted]
    tokens = [record.token_usage for record in accepted if record.token_usage is not None]
    unattended = [
        record
        for record in accepted
        if record.unattended and record.operator_touches == 0
    ]
    return {
        "task_count": len(records),
        "accepted_count": len(accepted),
        "acceptance_rate_percent": _percent(len(accepted), len(records)),
        "median_elapsed_seconds": _median(record.elapsed_seconds for record in accepted),
        "median_token_usage": _median(tokens),
        "token_coverage_percent": _percent(len(tokens), len(accepted)),
        "mean_repair_attempts": _mean(record.repair_attempts for record in accepted),
        "mean_operator_touches": _mean(record.operator_touches for record in accepted),
        "unattended_rate_percent": _percent(len(unattended), len(accepted)),
    }


def evaluate(
    records: Iterable[Measurement],
    thresholds: Thresholds = Thresholds(),
    *,
    baseline: str = "buzz",
    candidate: str = "machinist",
) -> dict[str, Any]:
    validate_thresholds(thresholds)
    pairs = pair_measurements(records, baseline, candidate)
    baseline_records = [pair[0] for pair in pairs]
    candidate_records = [pair[1] for pair in pairs]
    successful_pairs = [pair for pair in pairs if pair[0].accepted and pair[1].accepted]
    token_pairs = [
        pair
        for pair in successful_pairs
        if pair[0].token_usage is not None and pair[1].token_usage is not None
    ]

    baseline_elapsed = _median(pair[0].elapsed_seconds for pair in successful_pairs)
    candidate_elapsed = _median(pair[1].elapsed_seconds for pair in successful_pairs)
    baseline_tokens = _median(pair[0].token_usage for pair in token_pairs)
    candidate_tokens = _median(pair[1].token_usage for pair in token_pairs)
    baseline_repairs = _mean(pair[0].repair_attempts for pair in successful_pairs)
    candidate_repairs = _mean(pair[1].repair_attempts for pair in successful_pairs)
    speed_reduction = _reduction(baseline_elapsed, candidate_elapsed)
    token_reduction = _reduction(baseline_tokens, candidate_tokens)
    rework_reduction = _reduction(baseline_repairs, candidate_repairs)
    token_coverage = _percent(len(token_pairs), len(successful_pairs))
    candidate_accepted = [record for record in candidate_records if record.accepted]
    unattended_accepted = [
        record
        for record in candidate_accepted
        if record.unattended and record.operator_touches == 0
    ]
    unattended_rate = _percent(len(unattended_accepted), len(candidate_accepted))
    baseline_acceptance = _percent(
        sum(record.accepted for record in baseline_records), len(baseline_records)
    )
    candidate_acceptance = _percent(
        sum(record.accepted for record in candidate_records), len(candidate_records)
    )

    gates = {
        "minimum_paired_tasks": len(successful_pairs)
        >= thresholds.minimum_paired_tasks,
        "acceptance_non_regression": candidate_acceptance >= baseline_acceptance,
        "speed_reduction": _reduction_gate(
            baseline_elapsed,
            candidate_elapsed,
            speed_reduction,
            thresholds.minimum_speed_reduction_percent,
        ),
        "token_coverage": token_coverage
        >= thresholds.minimum_token_coverage_percent,
        "token_reduction": _reduction_gate(
            baseline_tokens,
            candidate_tokens,
            token_reduction,
            thresholds.minimum_token_reduction_percent,
        ),
        "rework_reduction": _reduction_gate(
            baseline_repairs,
            candidate_repairs,
            rework_reduction,
            thresholds.minimum_rework_reduction_percent,
        ),
        "unattended_rate": unattended_rate
        >= thresholds.minimum_unattended_rate_percent,
    }
    return {
        "passed": all(gates.values()),
        "baseline": baseline,
        "candidate": candidate,
        "thresholds": asdict(thresholds),
        "samples": {
            "paired_tasks": len(pairs),
            "paired_accepted_tasks": len(successful_pairs),
            "token_comparable_tasks": len(token_pairs),
        },
        "systems": {
            baseline: _system_summary(baseline_records),
            candidate: _system_summary(candidate_records),
        },
        "comparison": {
            "baseline_median_elapsed_seconds": baseline_elapsed,
            "candidate_median_elapsed_seconds": candidate_elapsed,
            "speed_reduction_percent": speed_reduction,
            "baseline_median_token_usage": baseline_tokens,
            "candidate_median_token_usage": candidate_tokens,
            "token_reduction_percent": token_reduction,
            "token_coverage_percent": token_coverage,
            "baseline_mean_repair_attempts": baseline_repairs,
            "candidate_mean_repair_attempts": candidate_repairs,
            "rework_reduction_percent": rework_reduction,
            "candidate_unattended_rate_percent": unattended_rate,
            "acceptance_rate_delta_points": candidate_acceptance - baseline_acceptance,
        },
        "gates": gates,
    }


def validate_thresholds(thresholds: Thresholds) -> None:
    if thresholds.minimum_paired_tasks < 1:
        raise CutoverDataError("minimum paired tasks must be positive")
    percentages = {
        "minimum speed reduction": thresholds.minimum_speed_reduction_percent,
        "minimum token reduction": thresholds.minimum_token_reduction_percent,
        "minimum rework reduction": thresholds.minimum_rework_reduction_percent,
        "minimum token coverage": thresholds.minimum_token_coverage_percent,
        "minimum unattended rate": thresholds.minimum_unattended_rate_percent,
    }
    for name, value in percentages.items():
        if not math.isfinite(value) or value < 0 or value > 100:
            raise CutoverDataError(f"{name} must be between 0 and 100")


def _display(value: Any, suffix: str = "") -> str:
    if value is None:
        return "N/A"
    if isinstance(value, float):
        return f"{value:.1f}{suffix}"
    return f"{value}{suffix}"


def markdown(report: dict[str, Any]) -> str:
    comparison = report["comparison"]
    gates = report["gates"]
    rows = [
        ("Paired accepted tasks", report["samples"]["paired_accepted_tasks"], "", gates["minimum_paired_tasks"]),
        ("Acceptance delta", comparison["acceptance_rate_delta_points"], " pp", gates["acceptance_non_regression"]),
        ("Cycle-time reduction", comparison["speed_reduction_percent"], "%", gates["speed_reduction"]),
        ("Token coverage", comparison["token_coverage_percent"], "%", gates["token_coverage"]),
        ("Token reduction", comparison["token_reduction_percent"], "%", gates["token_reduction"]),
        ("Repair reduction", comparison["rework_reduction_percent"], "%", gates["rework_reduction"]),
        ("Candidate unattended rate", comparison["candidate_unattended_rate_percent"], "%", gates["unattended_rate"]),
    ]
    lines = [
        "# Cutover benchmark",
        "",
        f"Overall: **{'PASS' if report['passed'] else 'FAIL'}**",
        "",
        "| Gate | Observed | Result |",
        "|---|---:|:---:|",
    ]
    lines.extend(
        f"| {name} | {_display(value, suffix)} | {'PASS' if passed else 'FAIL'} |"
        for name, value, suffix, passed in rows
    )
    return "\n".join(lines) + "\n"


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("measurements", type=Path)
    result.add_argument("--baseline", default="buzz")
    result.add_argument("--candidate", default="machinist")
    result.add_argument("--format", choices=("json", "markdown"), default="markdown")
    result.add_argument("--minimum-paired-tasks", type=int, default=10)
    result.add_argument("--minimum-speed-reduction-percent", type=float, default=30.0)
    result.add_argument("--minimum-token-reduction-percent", type=float, default=40.0)
    result.add_argument("--minimum-rework-reduction-percent", type=float, default=30.0)
    result.add_argument("--minimum-token-coverage-percent", type=float, default=95.0)
    result.add_argument("--minimum-unattended-rate-percent", type=float, default=90.0)
    return result


def main(argv: Sequence[str] | None = None) -> int:
    arguments = parser().parse_args(argv)
    thresholds = Thresholds(
        minimum_paired_tasks=arguments.minimum_paired_tasks,
        minimum_speed_reduction_percent=arguments.minimum_speed_reduction_percent,
        minimum_token_reduction_percent=arguments.minimum_token_reduction_percent,
        minimum_rework_reduction_percent=arguments.minimum_rework_reduction_percent,
        minimum_token_coverage_percent=arguments.minimum_token_coverage_percent,
        minimum_unattended_rate_percent=arguments.minimum_unattended_rate_percent,
    )
    try:
        report = evaluate(
            load_jsonl(arguments.measurements),
            thresholds,
            baseline=arguments.baseline,
            candidate=arguments.candidate,
        )
    except CutoverDataError as error:
        print(error, file=sys.stderr)
        return 1
    if arguments.format == "json":
        print(json.dumps(report, indent=2, sort_keys=True))
    else:
        print(markdown(report), end="")
    return 0 if report["passed"] else 2


if __name__ == "__main__":
    raise SystemExit(main())
