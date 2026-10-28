---
name: improve-codebase-architecture
description: Scan a codebase for deepening opportunities, present them as a factory report, then discuss whichever one you pick.
disable-model-invocation: true
---

<!-- Source: mattpocock/skills skills/engineering/improve-codebase-architecture @ 5beb35b040e3de97e54fa9e386d7e42d15c295c2, MIT (see ../../LICENSE-mattpocock). Modified: HTML report and CDN steps removed, output goes to factory report; grilling skill call and GLOSSARY.md/ADR file writes replaced by factory msg discussion and text proposals in the report. -->

# Improve Codebase Architecture

Surface architectural friction and propose **deepening opportunities**: refactors that turn shallow modules into deep ones. The aim is testability and AI-navigability.

This command is _informed_ by the project's domain model and built on a shared design vocabulary:

- Call the Skill tool with "codebase-design" for the architecture vocabulary (**module**, **interface**, **depth**, **seam**, **adapter**, **leverage**, **locality**) and its principles (the deletion test, "the interface is the test surface", "one adapter = hypothetical seam, two = real"). Use these terms exactly in every suggestion, and don't drift into "component," "service," "API," or "boundary."
- The domain language in `GLOSSARY.md` gives names to good seams; ADRs in `docs/adr/` record decisions this command should not re-litigate.

## Process

### 1. Explore

**Scope before you scan: YAGNI.** Deepening a module pays off by making future changes to it easier, so put extra weight on the parts of the codebase that have recently changed. Decide *where* to look before you look:

- If the user named a direction (a module, a subsystem, a pain point), take it, and skip the inference below.
- Otherwise, walk back a good stretch of the commit history (`git log --oneline`) to find the codebase's hot spots, the files and areas that keep coming up, and let those paths pull your attention first. If the changes are scattered with no clear hot spot, widen the net.

Read the project's domain glossary (`GLOSSARY.md`) and any ADRs in the area you're touching first.

Then spawn a sub-agent to walk the codebase. Don't follow rigid heuristics; explore organically and note where you experience friction:

- Where does understanding one concept require bouncing between many small modules?
- Where are modules **shallow**, with an interface nearly as complex as the implementation?
- Where have pure functions been extracted just for testability, but the real bugs hide in how they're called (no **locality**)?
- Where do tightly-coupled modules leak across their seams?
- Which parts of the codebase are untested, or hard to test through their current interface?

Apply the **deletion test** to anything you suspect is shallow: would deleting it concentrate complexity, or just move it? A "yes, concentrates" is the signal you want.

### 2. Present candidates as a factory report

Do not write any file and do not use HTML, CDN assets or diagrams that need a renderer. Deliver the report with the factory CLI, the body in plain markdown on stdin (pass the goal id of your task):

```
factory report "<headline: N deepening candidates in <area>>" - --goal G-NNN <<'EOF'
<markdown body>
EOF
```

Where a picture helps, use small ASCII or markdown before/after sketches.

For each candidate, write a markdown section with:

- **Files**: which files/modules are involved
- **Problem**: why the current architecture is causing friction
- **Solution**: plain English description of what would change
- **Benefits**: explained in terms of locality and leverage, and how tests would improve
- **Before / After sketch**: side-by-side ASCII or markdown, illustrating the shallowness and the deepening
- **Recommendation strength**: one of `Strong`, `Worth exploring`, `Speculative`, stated in bold

End the report with a **Top recommendation** section: which candidate you'd tackle first and why.

**Use GLOSSARY.md vocabulary for the domain, and the `/codebase-design` vocabulary for the architecture.** If `GLOSSARY.md` defines "Order," talk about "the Order intake module," not "the FooBarHandler," and not "the Order service."

**ADR conflicts**: if a candidate contradicts an existing ADR, only surface it when the friction is real enough to warrant revisiting the ADR. Mark it clearly in the candidate (e.g. a warning callout: _"contradicts ADR-0007, but worth reopening because…"_). Don't list every theoretical refactor an ADR forbids.

Do NOT propose interfaces yet. After `factory report` has run, ask: "Which of these would you like to explore?"

### 3. Discussion loop

Once the requester picks a candidate, discuss the open questions with them via `factory msg` to walk the decision tree: constraints, dependencies, the shape of the deepened module, what sits behind the seam, what tests survive. Do not write or edit any file.

Return domain-model changes as text proposals in the `factory report` body:

- **Naming a deepened module after a concept not in `GLOSSARY.md`?** Propose the new term and its definition as a glossary proposal.
- **Sharpening a fuzzy term during the conversation?** Propose the sharper wording as a glossary proposal.
- **Requester rejects the candidate with a load-bearing reason?** Offer an ADR proposal (text only), framed as: _"Want me to include this as an ADR proposal so future architecture reviews don't re-suggest it?"_ Only offer when the reason would actually be needed by a future explorer to avoid re-suggesting the same thing; skip ephemeral reasons ("not worth it right now") and self-evident ones.
- **Want to explore alternative interfaces for the deepened module?** Call the Skill tool with "codebase-design" and use its design-it-twice pattern (at most 3 parallel sub-agents).

Any change to `GLOSSARY.md` or an ADR file is a proposed coder task for the lead, never done by you.
