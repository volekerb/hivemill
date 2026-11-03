---
name: benchmark-protocol
description: Run a repeatable measurement of a tool or approach with pinned versions and raw output. Use when a task asks how fast, how big or how costly something is, or to compare options by measurement.
---

Purpose: numbers that someone else can reproduce from the commands in the report.

Follow these steps in order.

1. State the hypothesis in one sentence and the metric (time, memory, tokens, cost, size).
2. Fix the inputs: the same data, flags and machine for every candidate. Write them down.
3. Check the box before heavy work: `factory res`. Run one heavy job at a time, never two in parallel.
4. Clone into a directory in your cwd: `git clone --depth 1 <https url> <dir>`. Pin it with `git -C <dir> rev-parse HEAD` and record the SHA and tool versions.
5. If the third-party code needs no network, run it through `factory test <cmd>`. If it needs network or an install, stop and ask your lead.
6. Do one warm-up run and discard it.
7. Run at least N = 5 measured runs per candidate, interleaved or in the same order each time. Record every run, not only the best.
8. Report the median and the spread (min and max, or stdev). Never report a single run or the mean alone.
9. Where relevant record cost and tokens per run, with the price source.
10. Put every command, copy-pasteable, with its raw output in the Evidence section of the report (skill report-writing). Mark anything you could not run.
11. State what the benchmark does not show (one machine, small input, noisy neighbours) in Limits.

Stop and ask your lead when: a run needs more than the box has free, needs the network or an install, or the spread is larger than the difference between candidates.
