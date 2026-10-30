export const meta = {
  name: 'multi-angle-review',
  description: 'Read-only 3-angle review of a risky PR (Sonnet), each angle verified adversarially (Opus); max 6 agents, 3 parallel',
  phases: [{ title: 'Review' }, { title: 'Verify' }],
}

// Returns {confirmed, unverified, dropped, angles}; angles[].error is 'review' or 'verify' when a subagent died; errored lists those angles, and a slot with found null never ran cleanly.
// args: {pr, base, spec_task}. Run by the namespaced name <plugin>:multi-angle-review (reviewer-skills, qa-skills, coder-skills); the plugin dir is the main checkout, never a PR worktree.
// Coder mode (args.mode === 'coder', also needs pr; optional task, acceptance): a coder's self-review of its own PR.
// Angles security/spec/tests, all Sonnet, NO verifier agents (3 agents in total); the findings come back in
// 'unverified' for the coder to check itself; 'confirmed' is always empty. The default path is unchanged.
const { pr, base = 'origin/main', spec_task } = args
const coder = args.mode === 'coder'

const RULES = `HARD RULES (read-only agent). You must not edit or write any file. You must not run git write commands (commit, push, checkout, reset, merge, rebase, stash, branch -D, worktree). You must not run gh pr merge, gh pr review or gh pr comment. You must not run any factory command (no 'factory ...' at all, not even task show). Read only: use 'gh pr diff ${pr}', 'gh pr view ${pr}', 'git diff ${base}...HEAD', 'git log', grep and file reads in the current working directory (the current worktree). If a command is denied, do not retry it in another form; report it.`

const SPEC = coder
  ? (args.acceptance ? `Task ${args.task || '(unnamed)'} acceptance text, verbatim:\n${args.acceptance}` : `No acceptance text was given; say so and review against the diff alone.`)
  : spec_task
  ? `The task spec and numbered acceptance items are in the reviewer's task ${spec_task}; you cannot run factory, so read the PR body ('gh pr view ${pr}') and the files in the diff for the acceptance text, and say so if an item cannot be checked.`
  : `No task id was given; use the PR body ('gh pr view ${pr}') as the spec.`

const angle = (key, focus) => ({ key, focus })
const CODER_NOTE = ' Also never run gh pr edit, and never edit any file.'
const CODER_ANGLES = [
  angle('security', `Security: hooks, deny rules, settings, identity, git safety, CLI refusals, path handling, injection, secrets. For every guard or refusal changed, check both the block path and the allow path, and try at least 3 ways around the change.${CODER_NOTE}`),
  angle('spec', `Spec fit: go through each numbered acceptance item and decide for each one met / not met / cannot tell against the diff. Also report anything the diff does beyond the spec.${CODER_NOTE}`),
  angle('tests', `Tests: do they assert outcomes (not internals), are they deterministic, does every new or changed guard have a rejecting and an accepting test, is the test count not lowered, are any def test_ methods removed or renamed.${CODER_NOTE}`),
]

const REVIEW_ANGLES = [
  {
    key: 'correctness',
    focus: `Correctness: logic errors, edge cases, error handling, concurrency, data loss, and whether the tests really assert the behaviour (outcomes, not internals; deterministic; test count not lowered; no removed or renamed def test_ methods).`,
  },
  {
    key: 'guard-security',
    focus: `Guard and security: hooks, deny rules, settings, identity, git safety, CLI refusals, path handling, injection, secrets. For every guard or refusal changed, check both the block path and the allow path, and try at least 3 ways around the change.`,
  },
  {
    key: 'spec-fit',
    focus: `Spec fit: go through each numbered acceptance item of the task and decide for each one met / not met / cannot tell against the diff. Also report anything the diff does beyond the spec.`,
  },
]

const FINDINGS = {
  type: 'object',
  properties: {
    findings: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          file: { type: 'string' },
          line: { type: 'number' },
          severity: { type: 'string', enum: ['blocking', 'nit'] },
          summary: { type: 'string' },
          evidence: { type: 'string' },
        },
        required: ['file', 'line', 'severity', 'summary', 'evidence'],
      },
    },
  },
  required: ['findings'],
}

const VERDICTS = {
  type: 'object',
  properties: {
    verdicts: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          index: { type: 'number' },
          confirmed: { type: 'boolean' },
          evidence: { type: 'string' },
        },
        required: ['index', 'confirmed', 'evidence'],
      },
    },
  },
  required: ['verdicts'],
}

const ANGLES = coder ? CODER_ANGLES : REVIEW_ANGLES

// Review: exactly 3 agents, 3 in parallel.
phase('Review')
const reviews = await parallel(ANGLES.map(a => () =>
  agent(
    `${RULES}\n\nReview PR ${pr} (base ${base}) from ONE angle only.\n${a.focus}\n${SPEC}\nReport only findings you verified in the code, each with file, line, severity (blocking = fixing changes behaviour, a contract or an acceptance item; nit = presentation only), a one-sentence summary, and evidence (the command you ran or the lines you read). An empty list is a valid answer.`,
    { label: `review:${a.key}`, phase: 'Review', model: 'sonnet', schema: FINDINGS },
  )
))

// Verify: at most 3 agents (one per angle with findings), 3 in parallel.
// a dead subagent makes the agent call return null: a dead reviewer is an errored angle (not a clean one),
// a dead verifier leaves its findings unverified (not dropped). A thunk that dies outside the agent call, also after a
// successful review, lands in the final fallback below: found null, errored, never a clean 0.
phase('Verify')
const checked = await parallel(ANGLES.map((a, i) => async () => {
  if (!reviews[i] || !Array.isArray(reviews[i].findings)) return { angle: a.key, found: 0, kept: [], unverified: [], error: 'review' }
  const found = reviews[i].findings
  if (coder) return { angle: a.key, found: found.length, kept: [], unverified: found }
  if (found.length === 0) return { angle: a.key, found: 0, kept: [], unverified: [] }
  const out = await agent(
    `${RULES}\n\nAdversarially verify these findings on PR ${pr} (angle ${a.key}). For each one, read the cited code yourself and try to prove it wrong. Confirm only a finding that is real and reproducible from the code; give the evidence either way.\n\nFINDINGS (index: json):\n${found.map((f, j) => `${j}: ${JSON.stringify(f)}`).join('\n')}`,
    { label: `verify:${a.key}`, phase: 'Verify', model: 'opus', schema: VERDICTS },
  )
  if (!out || !Array.isArray(out.verdicts)) return { angle: a.key, found: found.length, kept: [], unverified: found, error: 'verify' }
  const ok = new Set(out.verdicts.filter(v => v.confirmed).map(v => v.index))
  return { angle: a.key, found: found.length, kept: found.filter((_, j) => ok.has(j)), unverified: [] }
}))

const results = checked.map((c, i) => c || { angle: ANGLES[i].key, found: null, kept: [], unverified: [], error: 'review', errored: true })
const confirmed = results.flatMap(c => c.kept)
const unverified = results.flatMap(c => c.unverified)
const total = results.reduce((n, c) => n + (c.found || 0), 0)
return {
  confirmed,
  unverified,
  dropped: total - confirmed.length - unverified.length,
  errored: results.filter(c => c.error).map(c => c.angle),
  angles: results.map(c => ({
    angle: c.angle, found: c.found, confirmed: c.kept.length, unverified: c.unverified.length,
    ...(c.error ? { error: c.error } : {}),
  })),
}
