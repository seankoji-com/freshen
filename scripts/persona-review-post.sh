#!/usr/bin/env bash
# persona-review-post.sh — post OCR review findings as a specific persona GitHub App,
# with marker-based dedup to prevent reposting on subsequent pushes.
#
# This script replaces alibaba/open-code-review's built-in posting (which can't
# use per-persona App tokens because its dedup calls GET /user, failing for
# installation tokens). OCR still generates the findings; this script reads
# them from /tmp/ocr-result.json and posts under the persona's own identity.
#
# Inputs (environment):
#   PERSONA_ID        — persona identifier (e.g. "sre", "grumpy-engineer")
#   OWNER_REPO        — owner/repo (e.g. "seankoji-com/foxsports-prototype")
#   PR_NUMBER         — pull request number
#   HEAD_SHA          — PR head commit SHA (for the review's commit_id)
#   OCR_RESULT_PATH   — path to OCR's JSON output (default: /tmp/ocr-result.json)
#   PERSONA_TOKEN     — persona's App installation token (minted by create-github-app-token)
#
# Marker format:
#   Inline comments: <!-- persona:<id> -->
#   Summary comment: <!-- persona-summary:<id> -->
#
# Dedup logic (mirrors OCR's own incremental mode, scoped to this persona's marker):
#   - Single-line comments: exact match on (path, line)
#   - Multi-line comments: IoU > 0.95 on (path, start_line, end_line)
#   - Single-line vs multi-line never match (same as OCR's behavior)
#
# Schema normalization: OCR v1.7.17 emits comment text in a "content" field;
# this script normalizes it to "body" so the downstream pipeline handles both
# schemas — the "body"-based pipeline predates the v1.7.17 format change.
#
# Diff-hunk validation: GitHub review comments must anchor on lines within a
# diff hunk. OCR findings may reference full-file line numbers outside the PR
# diff. Comments whose line ranges are not fully contained in a hunk are
# demoted to the summary ("no line info") section — never dropped, just
# moved from inline to the review summary body.
#
# Fail-closed: if PERSONA_TOKEN is missing or API calls fail, findings are
# recorded to a file for manual review — never silently posted under a
# different identity.
set -euo pipefail

: "${PERSONA_ID:?persona id required}"
: "${OWNER_REPO:?owner/repo required}"
: "${PR_NUMBER:?pr number required}"
: "${HEAD_SHA:?head sha required}"
: "${PERSONA_TOKEN:?persona app token required}"

OCR_RESULT_PATH="${OCR_RESULT_PATH:-/tmp/ocr-result.json}"
FALLBACK_DIR="${RUNNER_TEMP:-/tmp}"
FALLBACK_FILE="${FALLBACK_DIR}/persona-${PERSONA_ID}-findings-fallback.json"
MARKER="<!-- persona:${PERSONA_ID} -->"
SUMMARY_MARKER="<!-- persona-summary:${PERSONA_ID} -->"
OVERLAP_THRESHOLD="0.95"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

export GH_TOKEN="$PERSONA_TOKEN"

# ---- Validate OCR output ----
if [ ! -f "$OCR_RESULT_PATH" ]; then
  echo "::error::OCR result not found at $OCR_RESULT_PATH"
  exit 1
fi

if ! jq -e '.comments' "$OCR_RESULT_PATH" >/dev/null 2>&1; then
  if jq -e '.message' "$OCR_RESULT_PATH" >/dev/null 2>&1; then
    echo "OCR produced no comments: $(jq -r '.message // "no message"' "$OCR_RESULT_PATH")"
    SUMMARY_BODY="${SUMMARY_MARKER}

✅ **${PERSONA_ID}**: $(jq -r '.message // "No comments generated. Looks good to me."' "$OCR_RESULT_PATH")"
    jq -n --arg body "$SUMMARY_BODY" '{body: $body}' |
      gh api "repos/${OWNER_REPO}/issues/${PR_NUMBER}/comments" --input - >/dev/null 2>&1 || true
    exit 0
  fi
  echo "::error::OCR result has no 'comments' field and no 'message' — unparseable output"
  cp "$OCR_RESULT_PATH" "$FALLBACK_FILE"
  echo "::warning::OCR result saved to $FALLBACK_FILE for manual review"
  exit 1
fi

COMMENT_COUNT=$(jq '.comments | length' "$OCR_RESULT_PATH")
echo "OCR produced $COMMENT_COUNT comment(s)"

if [ "$COMMENT_COUNT" -eq 0 ]; then
  echo "No findings to post"
  exit 0
fi

# ---- Fetch existing PR review comments and filter to this persona's marker ----
echo "Fetching existing PR review comments for dedup..."

# Fetch all existing review comments, filter locally for the marker
EXISTING_JSON=$(gh api "repos/${OWNER_REPO}/pulls/${PR_NUMBER}/comments" \
  --paginate 2>/dev/null || echo "[]")

# Filter to comments carrying this persona's marker, extract line-span info
EXISTING_MARKED=$(echo "$EXISTING_JSON" | jq --arg marker "$MARKER" '
  [.[] | select(.body | contains($marker)) | {
    path: .path,
    side: .side,
    span: (
      if .start_line != null and .line != null and .start_line != .line then
        {start: ([.start_line, .line] | min), end: ([.start_line, .line] | max), multiline: true}
      elif .line != null and .line >= 1 then
        {start: .line, end: .line, multiline: false}
      elif .original_line != null and .original_line >= 1 then
        {start: .original_line, end: .original_line, multiline: false}
      else null end
    )
  } | select(.span != null)]
')

EXISTING_COUNT=$(echo "$EXISTING_MARKED" | jq 'length')
echo "Found $EXISTING_COUNT existing comment(s) with persona marker"

# ---- Dedup: filter OCR findings against existing marked comments ----
echo "Filtering findings against existing comments..."

DEDUP_RESULT=$(jq --argjson existing "$EXISTING_MARKED" --argjson threshold "$OVERLAP_THRESHOLD" '
  # Resolve a comment into a line span
  def line_span:
    if .start_line != null and .end_line != null and .start_line != .end_line then
      {start: ([.start_line, .end_line] | min), end: ([.start_line, .end_line] | max), multiline: true}
    elif .end_line != null and .end_line >= 1 then
      {start: .end_line, end: .end_line, multiline: false}
    elif .start_line != null and .start_line >= 1 then
      {start: .start_line, end: .start_line, multiline: false}
    else null end;

  # Check if two spans overlap (same rules as OCR incremental mode)
  def spans_overlap($a; $b; $thresh):
    if $a.multiline != $b.multiline then false
    elif ($a.multiline | not) then $a.start == $b.start
    else
      # tonumber? guards: jq min/max/arithmetic requires numeric JSON values.
      # OCR and API responses may surface numeric strings or null in edge cases.
      ([($a.end | tonumber? // 0), ($b.end | tonumber? // 0)] | min) -
       ([($a.start | tonumber? // 0), ($b.start | tonumber? // 0)] | max) + 1 as $overlap |
      if $overlap <= 0 then false
      else
        (($a.end | tonumber? // 0) - ($a.start | tonumber? // 0) + 1 +
         ($b.end | tonumber? // 0) - ($b.start | tonumber? // 0) + 1 - $overlap) as $union |
        if $union <= 0 then false
        else ($overlap / $union) > $thresh
        end
      end
    end;

  # Normalize: OCR v1.7.17 emits "content"; downstream pipeline expects "body"
  [.comments[] | if .body == null and .content != null then . + {body: .content} | del(.content) else . end] as $comments_normal |

  # Map comments without valid line info to line 1 if they have a valid path
  [$comments_normal[] |
    if (((.start_line // 0) < 1) and ((.end_line // 0) < 1) and (.path != null and .path != "")) then
      . + {start_line: 1, end_line: 1, body: ("⚠️ **[File-level finding]** " + .body)}
    else . end
  ] as $all_comments |

  # Partition comments into those with/without valid line info
  [$all_comments[] | select(
    ((.start_line // 0) >= 1) or ((.end_line // 0) >= 1)
  )] as $with_lines |
  [$all_comments[] | select(
    ((.start_line // 0) < 1) and ((.end_line // 0) < 1)
  )] as $without_lines |

  # Filter $with_lines: keep only those that do NOT overlap existing
  [$with_lines[] |
    . as $c |
    line_span as $span |
    if $span == null then $c
    elif ([$existing[] | select(.path == $c.path and .side == "RIGHT") |
           spans_overlap($span; .span; $threshold)] | any) then
      empty  # overlaps existing — skip
    else
      $c
    end
  ] as $filtered |

  {new_with_lines: $filtered, new_without_lines: $without_lines}
' "$OCR_RESULT_PATH")

NEW_WITH_LINES=$(echo "$DEDUP_RESULT" | jq '.new_with_lines')
NEW_WITHOUT_LINES=$(echo "$DEDUP_RESULT" | jq '.new_without_lines')
NEW_COUNT=$(echo "$NEW_WITH_LINES" | jq 'length')
NO_LINE_COUNT=$(echo "$NEW_WITHOUT_LINES" | jq 'length')
SKIPPED=$((COMMENT_COUNT - NEW_COUNT - NO_LINE_COUNT))

echo "$NEW_COUNT raw inline comment(s), $NO_LINE_COUNT without line info, $SKIPPED deduped"

# ---- Validate inline comments against PR diff hunks ----
# GitHub review comments must anchor on lines within diff hunks.
# OCR findings may reference lines outside the diff (LLM sees full file).
# Out-of-hunk comments are demoted to the summary "no line info" section.
echo "Validating comment positions against PR diff hunks..."

HUNKS_FILE="${RUNNER_TEMP:-/tmp}/pr-${PR_NUMBER}-hunks.json"
trap "rm -f ${HUNKS_FILE}" EXIT
if ! gh api "repos/${OWNER_REPO}/pulls/${PR_NUMBER}/files" --paginate --slurp \
  --jq '[.[][] | {filename, patch: (.patch // "")}]' \
  >"$HUNKS_FILE" 2>/dev/null; then
  echo "[]" >"$HUNKS_FILE"
  echo "::warning::Failed to fetch PR diff hunks from API — all inline comments may be demoted to summary"
fi

HUNK_VALIDATED=$(echo "$NEW_WITH_LINES" | python3 "${SCRIPT_DIR}/hunk-validate.py" "$HUNKS_FILE")

PRE_HUNK_COUNT="$NEW_COUNT"
NEW_WITH_LINES=$(echo "$HUNK_VALIDATED" | jq '.in_hunk')
OUT_OF_HUNK_COMMENTS=$(echo "$HUNK_VALIDATED" | jq '.out_of_hunk')
NEW_COUNT=$(echo "$NEW_WITH_LINES" | jq 'length')
OUT_OF_HUNK_COUNT=$(echo "$OUT_OF_HUNK_COMMENTS" | jq 'length')

echo "$NEW_COUNT inline comment(s) in diff hunks, $OUT_OF_HUNK_COUNT out of diff hunks (of $PRE_HUNK_COUNT total)"

# Demote out-of-hunk comments to the summary "no line info" section
# Recompute SKIPPED before NO_LINE_COUNT absorbs out-of-hunk demotions
SKIPPED=$((COMMENT_COUNT - PRE_HUNK_COUNT - NO_LINE_COUNT))
if [ "$OUT_OF_HUNK_COUNT" -gt 0 ]; then
  NO_LINE_COUNT=$((NO_LINE_COUNT + OUT_OF_HUNK_COUNT))
fi

# ---- Build inline comments for review payload ----
INLINE_COMMENTS=$(echo "$NEW_WITH_LINES" | jq --arg marker "$MARKER" '
  [.[] |
    . as $c |
    {path: .path, body: (.body + "\n\n" + $marker), side: "RIGHT"} +
    (if .start_line != null and .end_line != null and .start_line != .end_line then
      {start_line: ([.start_line, .end_line] | min), line: ([.start_line, .end_line] | max), start_side: "RIGHT"}
    elif .end_line != null and .end_line >= 1 then
      {line: .end_line}
    elif .start_line != null and .start_line >= 1 then
      {line: .start_line}
    else empty end)
  ]
')

INLINE_COUNT=$(echo "$INLINE_COMMENTS" | jq 'length')

# ---- Build summary body ----
SUMMARY_TEXT=$(jq -r 'if .message then .message elif (.comments | length) > 0 then "Review complete" else "No findings" end' "$OCR_RESULT_PATH")

NO_LINE_SUMMARY=""
if [ "$NO_LINE_COUNT" -gt 0 ]; then
  # Original findings without line info
  if echo "$NEW_WITHOUT_LINES" | jq -e 'length > 0' >/dev/null 2>&1; then
    NO_LINE_SUMMARY=$(echo "$NEW_WITHOUT_LINES" | jq -r '
      "\n\n---\n\n### Additional findings (no line information)\n\n" +
      ([.[] | "**\(.path // "general")**: \(.body // .content // "")"] | join("\n\n"))
    ')
  fi
  # Out-of-hunk findings (demoted from inline)
  if [ "$OUT_OF_HUNK_COUNT" -gt 0 ]; then
    OUT_OF_HUNK_SECTION=$(echo "$OUT_OF_HUNK_COMMENTS" | jq -r '
      "\n\n---\n\n### Findings anchored outside the PR diff (included as summary)\n\n" +
      ([.[] | "**\(.path // "general")**: \(.body // .content // "")"] | join("\n\n"))
    ')
    NO_LINE_SUMMARY="${NO_LINE_SUMMARY}${OUT_OF_HUNK_SECTION}"
  fi
fi

SUMMARY_BODY="${SUMMARY_MARKER}

### ${PERSONA_ID} review

${SUMMARY_TEXT}
${NO_LINE_SUMMARY}"

# ---- Post review (batched if needed) ----
if [ "$INLINE_COUNT" -gt 0 ]; then
  echo "Posting review with $INLINE_COUNT inline comment(s) as persona ${PERSONA_ID}..."

  BATCH_SIZE=50
  OFFSET=0

  while [ "$OFFSET" -lt "$INLINE_COUNT" ]; do
    BATCH_COMMENTS=$(echo "$INLINE_COMMENTS" | jq --argjson off "$OFFSET" --argjson sz "$BATCH_SIZE" '.[$off:$off+$sz]')
    BATCH_COUNT=$(echo "$BATCH_COMMENTS" | jq 'length')

    if [ "$BATCH_COUNT" -eq 0 ]; then
      break
    fi

    # First batch carries the summary body; subsequent batches have empty body
    if [ "$OFFSET" -eq 0 ]; then
      BODY="$SUMMARY_BODY"
    else
      BODY=""
    fi

    PAYLOAD=$(jq -n \
      --arg body "$BODY" \
      --argjson comments "$BATCH_COMMENTS" \
      --arg commit_id "$HEAD_SHA" \
      '{body: $body, event: "COMMENT", commit_id: $commit_id, comments: $comments}')

    if ! echo "$PAYLOAD" | gh api "repos/${OWNER_REPO}/pulls/${PR_NUMBER}/reviews" --input - >/dev/null 2>&1; then
      echo "::error::Failed to post review batch (offset=$OFFSET) as persona ${PERSONA_ID}"
      # Fail-closed: save full payload for manual review
      jq -n \
        --arg body "$SUMMARY_BODY" \
        --argjson comments "$INLINE_COMMENTS" \
        --arg commit_id "$HEAD_SHA" \
        '{body: $body, event: "COMMENT", commit_id: $commit_id, comments: $comments}' \
        >"$FALLBACK_FILE"
      echo "::warning::Findings saved to $FALLBACK_FILE for manual review"
      exit 1
    fi

    OFFSET=$((OFFSET + BATCH_COUNT))
    echo "Posted batch of $BATCH_COUNT comment(s) ($OFFSET/$INLINE_COUNT total)"
  done
else
  echo "No inline comments to post"
  # Post summary-only review if we have no-line findings
  if [ "$NO_LINE_COUNT" -gt 0 ]; then
    echo "Posting summary-only review..."
    PAYLOAD=$(jq -n \
      --arg body "$SUMMARY_BODY" \
      --arg commit_id "$HEAD_SHA" \
      '{body: $body, event: "COMMENT", commit_id: $commit_id}')
    echo "$PAYLOAD" | gh api "repos/${OWNER_REPO}/pulls/${PR_NUMBER}/reviews" --input - >/dev/null 2>&1 ||
      echo "::warning::Failed to post summary-only review"
  fi
fi

# ---- Post/update summary as issue comment ----
echo "Handling summary issue comment..."

# Check for existing summary comment with this persona's marker
EXISTING_SUMMARY_ID=$(gh api "repos/${OWNER_REPO}/issues/${PR_NUMBER}/comments" \
  --paginate 2>/dev/null |
  jq --arg marker "$SUMMARY_MARKER" '[.[] | select(.body | contains($marker))] | last | .id // empty' \
    2>/dev/null || echo "")

if [ -n "$EXISTING_SUMMARY_ID" ]; then
  echo "Updating existing summary comment $EXISTING_SUMMARY_ID"
  gh api "repos/${OWNER_REPO}/issues/comments/${EXISTING_SUMMARY_ID}" \
    -X PATCH \
    -f body="$SUMMARY_BODY" \
    >/dev/null 2>&1 || echo "::warning::Failed to update summary comment"
else
  echo "Posting new summary comment"
  jq -n --arg body "$SUMMARY_BODY" '{body: $body}' |
    gh api "repos/${OWNER_REPO}/issues/${PR_NUMBER}/comments" --input - \
      >/dev/null 2>&1 || echo "::warning::Failed to post summary comment"
fi

echo "Persona ${PERSONA_ID} review complete ($INLINE_COUNT inline, $NO_LINE_COUNT summary-only, $SKIPPED deduped)"
