# Persona review CI — setup and runbook

Five reviewer "personas" (solution-architect, grumpy-engineer, sre, ux-designer,
business-analyst) leave PR review comments under their own GitHub App identity.
This doc covers what's wired up, what an operator must configure, and how to
validate it. It documents only what the workflow/script files show — gaps are
marked **operator-to-fill**.

## Overview

```
 PR event (opened/sync/reopened/ready_for_review)
        │
        ▼
 persona-review-ping.yml  (self-hosted Linux runner)
        │  POST /v1/claude_code/routines/trig_01WczQbfsKih679HbRHN58BV/fire
        │  (Authorization: Bearer CLAUDE_ROUTINE_FIRE_TOKEN)
        ▼
 Claude Code cloud routine (org-wide, outside this repo)
        │  reviews the diff as 5 personas; cannot mint GitHub App tokens
        │  from its sandbox, so it posts ONE issue comment per persona,
        │  each tagged  <!-- persona-summary:<id> -->  (+ optional
        │  <!-- persona-inline:[...] --> anchor block)
        ▼
 issue_comment: created|edited
        │
        ▼
 persona-review-promote.yml  (self-hosted Linux runner)
        │  1. unescape + parse persona id and verdict from the marker
        │  2. resolve that persona's MM_*_APP_ID / MM_*_PRIVATE_KEY secrets
        │  3. mint an installation token (actions/create-github-app-token)
        │  4. POST the body as a real PR review (pulls/{pr}/reviews) under
        │     the persona's App identity, with verdict REQUEST_CHANGES /
        │     APPROVE / COMMENT and any inline anchors as `comments[]`
        │  5. delete the original human-attributed issue comment
        ▼
 PR review visible from mm-<persona>[bot]

 ── separate, parallel path (see "Open reconciliation" below) ──

 PR event (opened/sync/reopened)
        │
        ▼
 code-review-matrix.yml  (hosted runner, permissions: contents:read,
        │                 pull-requests:write)
        ▼
 seankoji-com/.github  reusable-parallel-review.yml  (external repo)
        │  runs OCR (alibaba/open-code-review), mints the same 5 persona
        │  App tokens, and (per scripts/ comments below) is the intended
        │  caller of scripts/persona-review-post.sh + hunk-validate.py
        ▼
 PR review comments under the same mm-<persona>[bot] identities
```

## Components

| Component | File | Runner |
|---|---|---|
| Fire the review routine on PR events | `.github/workflows/persona-review-ping.yml` | `self-hosted, Linux` |
| Promote routine comments to real App reviews | `.github/workflows/persona-review-promote.yml` | `self-hosted, Linux` |
| Legacy/parallel OCR-based matrix reviewer | `.github/workflows/code-review-matrix.yml` | hosted (calls `seankoji-com/.github` reusable workflow) |
| Post OCR findings as a persona App, with dedup | `scripts/persona-review-post.sh` | invoked from the reusable workflow above (not called from any workflow in this repo) |
| Clamp/filter comment line ranges to PR diff hunks | `scripts/hunk-validate.py` | invoked by `persona-review-post.sh` |

## Setup steps

1. **Claude Code routine** — org-wide "Persona review panel" routine, id
   `trig_01WczQbfsKih679HbRHN58BV`, referenced in `persona-review-ping.yml`.
   The routine itself (prompt, persona definitions, hourly cron catch-up) is
   configured outside this repo, at `https://claude.ai/code/routines/`.
   **Operator-to-fill**: routine ownership/editing access, the hourly cron
   schedule details, and the prompt contents are not visible from this repo.

2. **Fire token** — repo secret `CLAUDE_ROUTINE_FIRE_TOKEN`, sent as
   `Authorization: Bearer` to the Anthropic API in `persona-review-ping.yml`.
   **Operator-to-fill**: how this token is minted/rotated and its scope.

3. **Five persona GitHub Apps** — each persona is a distinct GitHub App with
   its own `MM_<PERSONA>_APP_ID` / `MM_<PERSONA>_PRIVATE_KEY` secret pair,
   consumed by both `persona-review-promote.yml` and `code-review-matrix.yml`:

   | Persona id | App ID secret | Private key secret |
   |---|---|---|
   | `solution-architect` | `MM_SOLUTION_ARCHITECT_APP_ID` | `MM_SOLUTION_ARCHITECT_PRIVATE_KEY` |
   | `grumpy-engineer` | `MM_GRUMPY_ENGINEER_APP_ID` | `MM_GRUMPY_ENGINEER_PRIVATE_KEY` |
   | `sre` | `MM_SRE_APP_ID` | `MM_SRE_PRIVATE_KEY` |
   | `ux-designer` | `MM_UX_DESIGNER_APP_ID` | `MM_UX_DESIGNER_PRIVATE_KEY` |
   | `business-analyst` | `MM_BUSINESS_ANALYST_APP_ID` | `MM_BUSINESS_ANALYST_PRIVATE_KEY` |

   Each App must be installed on this repo and needs at minimum: **Pull
   requests: Read & write** (to post reviews via `pulls/{pr}/reviews`) and
   **Issues: Write** (`persona-review-promote.yml` deletes the original
   issue comment via `issues/comments/{id}`, which the plain `GITHUB_TOKEN`
   cannot do for a user-authored comment — see its inline comment).
   `code-review-matrix.yml` additionally notes `MM_SOLUTION_ARCHITECT`
   credentials are also used for `.github` private repo checkout (read-only).
   **Operator-to-fill**: exact permission set configured on each App in
   GitHub, and which org/account owns them.

4. **Secret consumption map**:
   - `CLAUDE_ROUTINE_FIRE_TOKEN` → `persona-review-ping.yml` only.
   - `MM_*_APP_ID` / `MM_*_PRIVATE_KEY` (all 5 personas) → both
     `persona-review-promote.yml` (mints token to post/delete) and
     `code-review-matrix.yml` (passed through as secrets to the reusable
     workflow).
   - `OPENCODE_GO_API_KEY`, `OPENROUTER_API_KEY` → `code-review-matrix.yml`
     only, passed to the reusable workflow. **Operator-to-fill**: what these
     keys authenticate (not shown in this repo's files).

5. **Self-hosted runner requirement** — `persona-review-ping.yml` and
   `persona-review-promote.yml` both pin `runs-on: [self-hosted, Linux]`.
   Without a registered self-hosted Linux runner online, neither job will
   pick up. See the `mac-runners` tooling for standing up Linux runners via
   Docker. `code-review-matrix.yml` runs on a normal hosted runner and calls
   out to the external `seankoji-com/.github` reusable workflow.

## Scripts and their CI consumers

- **`scripts/persona-review-post.sh`** — posts OCR review findings as a
  specific persona's GitHub App, with marker-based dedup
  (`<!-- persona:<id> -->` inline, `<!-- persona-summary:<id> -->` summary)
  so re-runs on later pushes don't repost the same finding. Reads OCR output
  from `/tmp/ocr-result.json` (or `$OCR_RESULT_PATH`), requires
  `PERSONA_ID`, `OWNER_REPO`, `PR_NUMBER`, `HEAD_SHA`, `PERSONA_TOKEN` in the
  environment. Fails closed: if posting fails, findings are written to
  `$RUNNER_TEMP/persona-<id>-findings-fallback.json` rather than posted
  under the wrong identity. Not invoked by any workflow in this repo — its
  caller is the `seankoji-com/.github` reusable workflow that
  `code-review-matrix.yml` delegates to (**operator-to-fill**: confirm the
  exact call site in that external repo).
- **`scripts/hunk-validate.py`** — called by `persona-review-post.sh` to
  check whether a comment's line range overlaps a PR diff hunk; overlapping
  comments are clamped to the hunk range, non-overlapping ones are demoted
  from inline to the review summary. Also listed in `docs/dependencies.md`
  as a non-build validation utility.

## Open reconciliation — issue #66

`persona-review-ping.yml`'s header comment states it "Replaces the retired
code-review-matrix.yml OpenCode reviewer," but `code-review-matrix.yml` is
still present and active (`on: pull_request`, not disabled). Both paths
currently mint the same five `MM_*` persona App tokens and can both post
reviews under the same `mm-<persona>[bot]` identities on the same PR. This
doc does not resolve that overlap — see **issue #66** for the open decision
on whether/how to retire `code-review-matrix.yml`.

## Validation checklist

- [ ] `CLAUDE_ROUTINE_FIRE_TOKEN` is set as a repo (or org) secret and the
      routine id `trig_01WczQbfsKih679HbRHN58BV` still resolves.
- [ ] A `self-hosted, Linux` runner is online and picks up jobs (check the
      Actions run for `persona-review-ping.yml` / `persona-review-promote.yml`
      doesn't sit queued).
- [ ] All 5 `MM_*_APP_ID` / `MM_*_PRIVATE_KEY` secret pairs exist and each
      App is installed on this repo with the permissions in step 3 above.
- [ ] Opening/updating a PR fires the routine (check for the routine's
      `<!-- persona-summary:<id> -->`-tagged issue comments appearing, then
      disappearing as `persona-review-promote.yml` promotes them to reviews).
- [ ] Promoted reviews show up under `mm-<persona>[bot]` identities, not the
      human account that triggered the routine.
- [ ] Escaped markers (`&lt;!-- persona-summary:… --&gt;`) are still promoted
      (regression covered by `persona-review-promote.yml`'s parse step).
- [ ] Inline anchor comments land on the correct diff lines; a rejected
      (422) anchor falls back to a body-only review rather than failing
      silently.
- [ ] Decide the fate of `code-review-matrix.yml` per issue #66 before
      treating this runbook as describing a single, non-overlapping pipeline.
