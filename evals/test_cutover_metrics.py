"""Deterministic tests for cutover measurement gates."""

import unittest

from evals.cutover_metrics import (
    CutoverDataError,
    Measurement,
    Thresholds,
    evaluate,
    markdown,
    pair_measurements,
)


def record(
    task: int,
    system: str,
    *,
    accepted: bool = True,
    elapsed: float | None = None,
    tokens: int | None = None,
    repairs: int | None = None,
    touches: int | None = None,
    unattended: bool | None = None,
) -> Measurement:
    baseline = system == "buzz"
    return Measurement(
        task_id=f"task-{task:02d}",
        system=system,
        accepted=accepted,
        elapsed_seconds=elapsed if elapsed is not None else (100 if baseline else 60),
        token_usage=tokens if tokens is not None else (1000 if baseline else 600),
        repair_attempts=repairs if repairs is not None else (2 if baseline else 1),
        operator_touches=touches if touches is not None else (3 if baseline else 0),
        unattended=unattended if unattended is not None else not baseline,
    )


def passing_records() -> list[Measurement]:
    return [
        measurement
        for task in range(1, 11)
        for measurement in (record(task, "buzz"), record(task, "machinist"))
    ]


class CutoverMetricTests(unittest.TestCase):
    def test_passes_default_cutover_targets(self) -> None:
        report = evaluate(passing_records())
        self.assertTrue(report["passed"])
        self.assertEqual(report["samples"]["paired_accepted_tasks"], 10)
        self.assertEqual(report["comparison"]["speed_reduction_percent"], 40.0)
        self.assertEqual(report["comparison"]["token_reduction_percent"], 40.0)
        self.assertEqual(report["comparison"]["rework_reduction_percent"], 50.0)
        self.assertIn("Overall: **PASS**", markdown(report))

    def test_fails_closed_on_insufficient_token_coverage(self) -> None:
        records = passing_records()
        for index, measurement in enumerate(records):
            if measurement.system == "machinist" and measurement.task_id == "task-01":
                records[index] = Measurement(**{**measurement.__dict__, "token_usage": None})
        report = evaluate(records)
        self.assertEqual(report["comparison"]["token_coverage_percent"], 90.0)
        self.assertFalse(report["gates"]["token_coverage"])
        self.assertFalse(report["passed"])

    def test_acceptance_regression_and_unpaired_success_reduce_readiness(self) -> None:
        records = passing_records()
        for index, measurement in enumerate(records):
            if measurement.system == "machinist" and measurement.task_id == "task-10":
                records[index] = Measurement(**{**measurement.__dict__, "accepted": False})
        report = evaluate(records)
        self.assertFalse(report["gates"]["acceptance_non_regression"])
        self.assertFalse(report["gates"]["minimum_paired_tasks"])

    def test_zero_repair_baseline_passes_only_without_candidate_repairs(self) -> None:
        records = [
            record(task, system, repairs=0)
            for task in range(1, 11)
            for system in ("buzz", "machinist")
        ]
        report = evaluate(records)
        self.assertIsNone(report["comparison"]["rework_reduction_percent"])
        self.assertTrue(report["gates"]["rework_reduction"])

        records[-1] = Measurement(**{**records[-1].__dict__, "repair_attempts": 1})
        report = evaluate(records)
        self.assertFalse(report["gates"]["rework_reduction"])

    def test_rejects_duplicates_and_missing_pairs(self) -> None:
        with self.assertRaisesRegex(CutoverDataError, "duplicate"):
            pair_measurements(
                [record(1, "buzz"), record(1, "buzz"), record(1, "machinist")],
                "buzz",
                "machinist",
            )
        with self.assertRaisesRegex(CutoverDataError, "missing measurement"):
            pair_measurements([record(1, "buzz")], "buzz", "machinist")

    def test_custom_thresholds_are_applied(self) -> None:
        report = evaluate(
            passing_records(),
            Thresholds(minimum_speed_reduction_percent=50.0),
        )
        self.assertFalse(report["gates"]["speed_reduction"])

    def test_rejects_invalid_thresholds(self) -> None:
        with self.assertRaisesRegex(CutoverDataError, "between 0 and 100"):
            evaluate(
                passing_records(),
                Thresholds(minimum_token_coverage_percent=101.0),
            )


if __name__ == "__main__":
    unittest.main()
