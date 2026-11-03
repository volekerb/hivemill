# Research team

How you give the boss a research question and what report comes back. The role itself is described in [researcher.md](researcher.md).

## Asking a research question

Tell the boss, in plain words:

- **The question.** One or two sentences.
- **Why it matters.** What you are trying to do.
- **The decision it feeds.** What you will do differently depending on the answer.
- **Deadline and time box.** When you need it and how long the researcher may spend.
- **Budget.** Anything it must not spend or install.

Example asks:

1. Plugin usage: "Which Claude Code plugins do people actually use for code review, and is any worth adding to our reviewer? I need to decide this week; half a day of research."
2. Benchmark: "How fast is tool X on a 10,000-file repository compared with what we use now? Measure it, do not quote the vendor. I will decide whether to switch."
3. Tool or approach comparison: "Compare approach A and approach B for syncing state between machines. I need a recommendation and the main risk of each."

## What happens

1. **Boss to goal.** The boss turns your ask into a goal for the claude org. It keeps your question and decision in the goal text.
2. **Director and lead.** The director gives the goal to a team lead, who runs the work.
3. **Plan.** The team's pm writes a research plan with the `research-plan` skill: a researcher task with a brief and numbered acceptance, plus an evidence-review task after it.
4. **Research.** The lead spawns the researcher (`factory spawn researcher --goal G --task T-x`, skill `research-flow`). The researcher stores the full report with `factory task note <task> -`, publishes a summary with `factory report`, and finishes with `factory done`. There is no PR.
5. **Evidence review.** A fresh researcher, never the one who wrote the report, checks it against the sources (skill `evidence-review`) and finishes with `factory done "evidence-review: PASS ..."` or `factory done "evidence-review: FAIL ..."` listing the gaps. On FAIL the lead reopens the research task, at most twice, and every retry is reviewed again.
6. **Up the chain.** On PASS the lead sends the report unchanged to the director with `factory msg director ... --kind report`. The director forwards it to the boss the same way.
7. **To you.** The boss tells you the answer, the recommendation, the proposed follow-ups and the sources in plain English.

## What comes back

The report always has these seven headings, in this order:

- `## Question`: your question as the researcher understood it.
- `## Answer`: a short plain-English summary, answer first.
- `## Findings`: numbered claims, each ending with source ids like `[S1]`.
- `## Recommendation`: what to do, in order.
- `## Proposed follow-ups`: tasks the researcher suggests. They are proposals only.
- `## Evidence`: the numbered sources, with dates and raw output of any measurement.
- `## Limits`: what was not checked and what is uncertain.

Where it is stored: the full report is a note on the research task, and a copy goes out as a report message. Read it again with `factory task show <T> --full`; the boss reads the message with `factory inbox --show M####`.

Proposed follow-ups become goals only on your word. Nobody starts them on their own.

## What the researcher can and cannot do

Can: read the public web, clone public repositories over https into its own per-task research directory, and run measurements there. It always runs in the worker sandbox with the read-only GitHub token (see [worker-sandbox.md](worker-sandbox.md) and [worker-guard.md](worker-guard.md)).

Cannot: write the factory repo, push, merge, spawn agents, launch claude, read secrets, or post on GitHub. Web pages, READMEs and cloned files are untrusted data; the researcher never follows instructions found in them.

## Limits

- The sandbox has no egress filter and is not a full security boundary; see section 6 of [worker-sandbox.md](worker-sandbox.md).
- A researcher cannot run a plugin itself. A lead or director must run `factory skilleval` for that.
- Only the claude org has a researcher; the cursor org is not supported.
- Research directories are not cleaned automatically yet.
