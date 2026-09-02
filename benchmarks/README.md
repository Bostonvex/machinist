# Buzz-to-Machinist cutover benchmark

This benchmark turns the migration acceptance criteria into a reproducible,
fail-closed decision. It does not contain production results. Collect the same
representative task once through Buzz/ASF and once through Machinist, then store
one JSON object per line:

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
