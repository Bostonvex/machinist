# Buzz-to-Machinist cutover benchmark

This benchmark turns the migration acceptance criteria into a reproducible,
fail-closed decision. The synthetic fixture does not contain production results.
Collect the same representative task once through Buzz/ASF and once through
Machinist, then store one JSON object per line:

The first measured offline shadow is recorded separately in
[the 2026-09-02 pilot report](pilot-2026-09-02.md); it is one pair and therefore
does not satisfy the cutover gate.

```json
{"task_id":"change-001","system":"buzz","accepted":true,"elapsed_seconds":1800,"token_usage":52000,"repair_attempts":2,"operator_touches":3,"unattended":false}
{"task_id":"change-001","system":"machinist","accepted":true,"elapsed_seconds":900,"token_usage":26000,"repair_attempts":1,"operator_touches":0,"unattended":true}
```

`accepted` means the same repository quality gate passed for both paths.
`elapsed_seconds` runs from admission to accepted handoff. `token_usage` is the
aggregate reported usage across every attempt, including failed attempts; use
`null` when a harness does not expose usage. Missing usage is not zero.
`repair_attempts` counts semantic or test-driven repair loops after the first
implementation. `operator_touches` counts interventions needed to keep the task
moving. `unattended` is true only when no intervention was needed.

Run the evaluator with:

```sh
python3 -m evals.cutover_metrics benchmarks/cutover.synthetic.jsonl
python3 -m evals.cutover_metrics measurements.jsonl --format=json
```

Capture evidence with `evals.pilot_evidence`. The capture path deliberately
requires a human-supplied acceptance decision, semantic repair count, operator
touch count, and attended/unattended classification. A successful process is not
automatically an accepted change.

First export a task-unbound Buzz inventory. This is useful for correlating
`FACTORY:RUN` and GitHub timestamps to exact telemetry turn IDs, but inventory
rows are explicitly marked ineligible for pairing:

```sh
python3 -m evals.pilot_evidence buzz-inventory \
  --database ~/.local/share/buzz-agent-observability/telemetry.sqlite3 \
  --since 2026-09-01T00:00:00Z \
  --output ~/.machinist/pilot/buzz-turns.jsonl
```

After verifying which turns belong to one task, bind all of them to the Buzz
record. `elapsed-seconds` is admission through accepted handoff, not merely the
sum of active turn durations:

```sh
python3 -m evals.pilot_evidence record-buzz \
  --database ~/.local/share/buzz-agent-observability/telemetry.sqlite3 \
  --turn-id TURN_1 --turn-id TURN_2 \
  --task-id change-001 --elapsed-seconds 1800 \
  --accepted --repair-attempts 2 --operator-touches 3 --attended \
  --output ~/.machinist/pilot/measurements.jsonl
```

For Machinist, capture a terminal job by ID. Usage is summed across every
attempt; one attempt without structured usage makes the task's usage `null`.

```sh
python3 -m evals.pilot_evidence record-machinist \
  --endpoint http://127.0.0.1:7331 --job-id JOB_ID \
  --task-id change-001 --accepted --repair-attempts 0 \
  --operator-touches 0 --unattended \
  --output ~/.machinist/pilot/measurements.jsonl
```

Capture files are written atomically with mode `0600`. A duplicate
`task_id`/`system` pair is rejected unless `--replace` is explicit. Evidence
metadata includes IDs, profiles, providers, workers, and timestamps, but never
copies a prompt or result. Buzz `context_occupancy` updates are preserved as a
diagnostic and are never misreported as aggregate token usage.

The first command demonstrates the format with synthetic data. It is not
migration evidence. The evaluator exits 0 only when all gates pass, 2 when valid
evidence fails a gate, and 1 when the evidence is malformed or incomplete.

Default gates require:

- 10 paired accepted tasks (use at least 20 for the production decision);
- no acceptance-rate regression;
- at least 30% lower median elapsed time;
- at least 40% lower median reported token usage;
- at least 30% fewer mean repair attempts;
- at least 95% comparable token coverage; and
- at least 90% of accepted Machinist tasks completed unattended with zero
  operator touches.

Every task must have exactly one record for each named system. Comparisons for
time, tokens, and repair are limited to pairs accepted through both paths so a
quality regression cannot manufacture an apparent efficiency gain. Keep raw
measurements and the generated JSON report with the release evidence.
