"""Tests for the second-node Buzz JSON telemetry adapter."""

import importlib.util
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "nvidia-smi-json-provider.py"
SPEC = importlib.util.spec_from_file_location("nvidia_smi_json_provider", SCRIPT)
assert SPEC and SPEC.loader
PROVIDER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PROVIDER)


class NvidiaSmiJsonProviderTests(unittest.TestCase):
    def test_parses_fixed_nvidia_query(self) -> None:
        samples = PROVIDER.parse_nvidia_csv("0, 73, 1024, 2048, 51, 23.5\n")
        self.assertEqual(len(samples), 5)
        self.assertEqual(samples[0]["metric_name"], "gpu.0.utilization_percent")
        self.assertEqual(samples[-1]["value"], 23.5)

    def test_omits_unsupported_measurements(self) -> None:
        samples = PROVIDER.parse_nvidia_csv("0, 73, [N/A], N/A, 51, [Not Supported]\n")
        self.assertEqual(
            [sample["metric_name"] for sample in samples],
            ["gpu.0.utilization_percent", "gpu.0.temperature_celsius"],
        )

    def test_rejects_shape_and_numeric_abuse(self) -> None:
        for value in ("0,1\n", "0, nan, 1, 2, 3, 4\n", "0, -1, 1, 2, 3, 4\n"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                PROVIDER.parse_nvidia_csv(value)


if __name__ == "__main__":
    unittest.main()
