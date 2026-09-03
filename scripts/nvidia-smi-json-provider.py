#!/usr/bin/env python3
"""Emit allowlisted NVIDIA metrics for one strict-SSH Buzz JSON provider."""

from __future__ import annotations

import argparse
import json
import math
import re
import subprocess


SAFE_HOST = re.compile(r"^(?:[A-Za-z0-9._-]+@)?[A-Za-z0-9][A-Za-z0-9._-]{0,252}$")
QUERY = "index,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw"
DEFINITIONS = (
    ("utilization_percent", "percent"),
    ("memory_used_mib", "MiB"),
    ("memory_total_mib", "MiB"),
    ("temperature_celsius", "celsius"),
    ("power_watts", "watts"),
)


def parse_nvidia_csv(text: str) -> list[dict[str, object]]:
    samples: list[dict[str, object]] = []
    lines = [line for line in text.splitlines() if line.strip()]
    if len(lines) > 16:
        raise ValueError("too many GPUs in NVIDIA response")
    for line in lines:
        fields = [field.strip() for field in line.split(",")]
        if len(fields) != 6 or not fields[0].isdigit() or int(fields[0]) > 1024:
            raise ValueError("NVIDIA response does not match the fixed query")
        gpu_index = int(fields[0])
        # The field count was validated above. Avoid zip(strict=True) so the
        # adapter also runs under the older Python shipped with macOS.
        for raw_value, (metric, unit) in zip(fields[1:], DEFINITIONS):
            normalized = raw_value.lower().strip("[] ")
            if normalized in {"n/a", "na", "not supported"}:
                continue
            value = float(raw_value)
            if not math.isfinite(value) or value < 0 or value > 10**15:
                raise ValueError("NVIDIA response contains an invalid number")
            samples.append(
                {
                    "metric_name": f"gpu.{gpu_index}.{metric}",
                    "value": value,
                    "unit": unit,
                    "measurement_quality": "exact",
                }
            )
    return samples


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", required=True)
    arguments = parser.parse_args()
    if not SAFE_HOST.fullmatch(arguments.host):
        parser.error("host must be a bounded SSH destination or alias")
    result = subprocess.run(
        [
            "/usr/bin/ssh",
            "-o",
            "BatchMode=yes",
            "-o",
            "StrictHostKeyChecking=yes",
            "-o",
            "ConnectTimeout=3",
            "--",
            arguments.host,
            "nvidia-smi",
            f"--query-gpu={QUERY}",
            "--format=csv,noheader,nounits",
        ],
        check=True,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        timeout=5,
    )
    if len(result.stdout) > 256 * 1024:
        raise ValueError("NVIDIA response exceeded the size limit")
    samples = parse_nvidia_csv(result.stdout.decode("utf-8"))
    print(json.dumps({"schema_version": 1, "samples": samples}, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
