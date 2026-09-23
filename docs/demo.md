# Demo: one human prompt, two peer agents

This is a condensed trace from a successful v0.2 run against a pet-hospital application.

## Task

The user asked for a restrained modernization of an existing UI: make it feel less dated without a large redesign.

Only Austin received the user task.

## Independent peer analysis

Austin inspected the implementation and sent Tony a live peer message asking for an independent assessment rather than asking Tony to simply confirm Austin's ideas.

Tony disagreed with part of Austin's initial framing. Tony argued that typography and badges were already strong and that the dated feeling came mainly from decorative SaaS-template patterns: a dot-grid background, corner ornaments, inconsistent radius tiers and heavy shadows.

Austin accepted the useful parts of that critique and published a shared Plan limited to four restrained CSS changes.

## Plan sign-off

```text
Austin updated shared plan → v1
Austin ready ✓
Tony   ready ✓
PLAN → EXECUTE
```

## Execution

The agreed division of labor did not force both agents to edit code.

Austin implemented the four CSS changes and committed them in the Austin worktree.

Tony stayed read-only and acted as reviewer. This is intentional: Duo lets the Plan determine the useful division of labor rather than requiring symmetric editing.

## Review

Tony reviewed Austin's exact commit and found no blocking issue. Tony did note that some small component-specific radii remained, but explicitly judged that further normalization would violate the user's request to stay restrained.

```text
EXECUTE → REVIEW
REVIEW → INTEGRATE
```

## Integration

Because Tony had no code delta in this run, the integrated Austin HEAD remained Austin's implementation commit. Both agents signed the same integrated HEAD.

```text
Austin integrated HEAD ✓
Tony   integrated HEAD ✓
INTEGRATE → DONE
```

## Result

The original user branch was not modified automatically. Duo left the integrated result on the Austin Duo branch for human inspection and merge.

## What this demo validates

- single human entry;
- autonomous peer wake-up;
- genuine disagreement and correction;
- versioned shared Plan;
- evidence-backed phase transitions;
- role asymmetry when useful;
- Git worktree isolation;
- cross-review;
- human-controlled final merge.
