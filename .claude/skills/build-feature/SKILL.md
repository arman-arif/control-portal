---
name: build-feature
description: Runs the full plan → build → review pipeline for a feature or bug fix using the planner, implementer, and reviewer subagents, looping until the review is approved. Use when the user types /build-feature or asks to plan, build, and review a feature end to end.
argument-hint: <feature or bug description>
disable-model-invocation: true
---
Build feature: $ARGUMENTS
You are the orchestrator. You do NOT write plans, code, or reviews yourself. You delegate each stage to the matching subagent and pass results between them.
Stage 1 — Plan

1. Delegate to the planner subagent with this task: "$ARGUMENTS".
2. When it finishes, show the user a short summary of `.claude/work/PLAN.md` (goal, number of steps, files touched).
3. If the planner listed open questions that would change the design, stop and ask the user before continuing. Otherwise continue.

Stage 2 — Build

4. Delegate to the implementer subagent: "Implement .claude/work/PLAN.md."
5. If the implementer reports it could not finish or checks are failing, report that to the user and stop.

Stage 3 — Review loop

6. Delegate to the reviewer subagent: "Review the changes against .claude/work/PLAN.md."
7. Read the `Status:` line in `.claude/work/REVIEW.md`.
   * APPROVED → go to Stage 4.
   * CHANGES REQUESTED → delegate to the implementer: "Fix every blocker and should-fix item in .claude/work/REVIEW.md." Then run the reviewer again.
8. Allow at most 3 review rounds. If still not approved after round 3, stop and show the user the remaining blockers.

Stage 4 — Report

9. Give the user a final summary:
   * What was built (1–3 sentences)
   * Files changed
   * Checks run and results
   * Review rounds taken
   * Any deviations from the plan or out-of-scope issues the agents noted
10. Do not commit or push. Tell the user the changes are ready for their review.

Rules

* Run the stages strictly in order; never run two subagents at the same time.
* Pass work between agents only through `.claude/work/PLAN.md` and `.claude/work/REVIEW.md`.
* If any subagent is missing, tell the user which file is missing from `.claude/agents/` and stop.
