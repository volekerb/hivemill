---
name: audit-round
description: Goal-end audit procedure for the lead: audit task, pm spawn, gaps rounds, director sign-off. Use when `factory task ready --goal G` is empty and every build task is done or cancelled.
---

# Audit at goal end (lead)

5. **Audit at goal end.** When `factory task ready --goal G` is empty and every build task is done or cancelled, do not close the goal yourself. Create an audit task and spawn the pm: `factory task add --goal G --team <team> --role pm --title "Audit G-xxx (round n)" --spec -` (spec: the audit checklist from the pm role, the goal's success criteria and any known findings), then `factory spawn pm --task <T> --goal G --team <team>`.
   - If the audit is `--gaps`, the pm has filed follow-ups or reopened tasks: run the build loop for them, then start the next audit round.
   - **At most 2 audit rounds.** A third round goes to the director with the full history, as with the 3-attempt rule.
   - If the audit is `--ready`, ask the director for the sign-off: `factory msg director "sign-off G-xxx audit vN" "audit is in factory goal show; evidence: …" --kind request --goal G`. Only the director runs `factory goal signoff`. After it, the pm (or the director) runs `factory goal close`. You do not close goals: the old status-to-done shortcut is refused for you.
   - Tell the director the goal is done, with evidence: merged PRs and how each criterion was verified.
