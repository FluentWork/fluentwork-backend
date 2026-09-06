#!/usr/bin/env bash
# Bulk create W3 backend issues from .scratch/issues/2026-09-06-W3-backend-tickets/
#
# Usage:
#   ./scripts/create-w3-issues.sh                 # dry run (print commands only)
#   ./scripts/create-w3-issues.sh --apply        # actually create issues
#
# Requires: gh CLI authenticated to FluentWork/fluentwork-backend
# Side effects: closes 2 already-fixed issues, creates 8 master + 55 sub-tickets

set -euo pipefail

SCRATCH_DIR=".scratch/issues/2026-09-06-W3-backend-tickets"
DRY_RUN=true

if [[ "${1:-}" == "--apply" ]]; then
  DRY_RUN=false
  echo "⚠️  APPLY MODE — will create real GitHub issues"
  read -p "Continue? [y/N] " -n 1 -r
  echo
  [[ $REPLY =~ ^[Yy]$ ]] || exit 0
else
  echo "ℹ️  DRY RUN — use --apply to actually create issues"
fi

run() {
  if $DRY_RUN; then
    echo "  [DRY] $*"
  else
    echo "  [RUN]  $*"
    eval "$@"
  fi
}

echo
echo "==== Phase 1: Close already-fixed P0 issues ===="
echo "#43 — keepalive (code fixed, tests green)"
run "gh issue close 43 --comment '✅ 代码修复 commit 5e3b8b0;单测 6/6 全绿。真机长时回归纳入 W3 Day 1-3 联调范围。详见 docs/40_研发流程与协作/74_V2_0_凭证与P0修复验证日志_2026-09-06.md §3.1'"
echo "#42 — BadgeEmitter (code fixed, tests green)"
run "gh issue close 42 --comment '✅ 代码修复 commit 77e0578+fde8390+64453e3;BadgeEmitter 7/7 + turn_id 2/2 全绿。真机 feedback.badge emitted 验证纳入 W3 Day 1-3。详见 74_ §3.1'"

echo
echo "==== Phase 2: Comment拆解 on existing #28 / #21 ===="
echo "#28 — 评估集 拆解评论(4 sub-tickets)"
run "gh issue comment 28 --body-file $SCRATCH_DIR/01-skill-28-eval-dataset.md"
echo "#21 — review worker 拆解评论(5 sub-tickets)"
run "gh issue comment 21 --body-file $SCRATCH_DIR/02-skill-21-review-worker.md"

echo
echo "==== Phase 3: Create 8 master issues ===="
declare -A MASTERS=(
  ["B17"]="03-skill-B17-TTS-Provider.md|backend,v2.0-blocker,priority: P0,tts|V2.0 W3"
  ["B18"]="06-skill-B18-review-eval.md|backend,v2.0-blocker,priority: P0,review,llm|V2.0 W3-W5"
  ["B19"]="04-skill-B19-B7-hit-injection.md|backend,v2.0-blocker,priority: P0,corpus,b7|V2.0 W3"
  ["B21"]="07-skill-B21-materials.md|backend,v2.0-blocker,priority: P1,materials,llm|V2.0 W3-W4"
  ["B22"]="08-skill-B22-drill-privacy.md|backend,v2.0-blocker,priority: P0,drill,privacy,migration|V2.0 W3-W4"
  ["B23"]="09-skill-B23-topic-cards.md|backend,v2.0-blocker,priority: P1,topic,llm,scheduler|V2.0 W3-W4"
  ["B24"]="10-skill-B24-session-history.md|backend,v2.0-blocker,priority: P2,sessions,api|V2.0 W3-W4"
  ["B25"]="05-skill-B25-F3-pin-favorite.md|backend,v2.0-blocker,priority: P1,corpus,api-change|V2.0 W3"
)

for code in "${!MASTERS[@]}"; do
  IFS='|' read -r file labels milestone <<< "${MASTERS[$code]}"
  echo
  echo "  → $code master"
  run "gh issue create --title '$code: see body' --label '$labels' --milestone '$milestone' --body-file <(sed -n '/^## §0 Master Issue Body/,/^## §1 /p' $SCRATCH_DIR/$file | head -60)"
done

echo
echo "==== Phase 4: Create 55 sub-tickets ===="
echo "⚠️  Sub-ticket extraction: read each file §1..§N, extract Body content + Labels/Milestone"
echo "    See function extract_tickets() for awk patterns."

extract_tickets() {
  local file="$1"
  awk '
    BEGIN { in_section = 0; section = "" }
    /^## §[0-9]+ T-/ {
      in_section = 1
      # Extract title from §N T-XXX-N — Title
      section = $0
      sub(/^## §[0-9]+ /, "", section)
      sub(/ — .*/, "", section)  # remove "— Body" suffix
      print "---SUBTICKET---"
      print "TITLE:" section
      print "FILE:" FILENAME
      next
    }
    in_section && /^\*\*Labels\*\*:/ {
      sub(/^\*\*Labels\*\*: /, "")
      print "LABELS:" $0
      next
    }
    in_section && /^\*\*Milestone\*\*:/ {
      sub(/^\*\*Milestone\*\*: /, "")
      print "MILESTONE:" $0
      next
    }
    in_section && /^### Body/ {
      in_body = 1
      print "BODY_START"
      next
    }
    in_section && in_body && /^---$/ {
      print "BODY_END"
      in_section = 0
      in_body = 0
      next
    }
    in_body { print }
  ' "$file"
}

# Skill → file mapping (only ones with new master issues)
declare -A SKILL_FILES=(
  ["B17"]="03-skill-B17-TTS-Provider.md"
  ["B18"]="06-skill-B18-review-eval.md"
  ["B19"]="04-skill-B19-B7-hit-injection.md"
  ["B21"]="07-skill-B21-materials.md"
  ["B22"]="08-skill-B22-drill-privacy.md"
  ["B23"]="09-skill-B23-topic-cards.md"
  ["B24"]="10-skill-B24-session-history.md"
  ["B25"]="05-skill-B25-F3-pin-favorite.md"
)

for code in "${!SKILL_FILES[@]}"; do
  file="${SKILL_FILES[$code]}"
  echo
  echo "  → Extract sub-tickets from $code ($file)"
  if $DRY_RUN; then
    echo "    [DRY] awk extraction — see scratch file §1..§N"
    extract_tickets "$SCRATCH_DIR/$file" | head -20
    echo "    ... (full output: 55 tickets total across 8 skills)"
  else
    # Save extracted tickets to temp file for gh bulk processing
    tmp=$(mktemp)
    extract_tickets "$SCRATCH_DIR/$file" > "$tmp"
    while IFS= read -r line; do
      case "$line" in
        ---SUBTICKET---)
          current_title=""
          current_labels=""
          current_milestone=""
          current_body=""
          in_body=0
          ;;
        TITLE:*)
          current_title="${line#TITLE:}"
          ;;
        LABELS:*)
          current_labels="${line#LABELS:}"
          ;;
        MILESTONE:*)
          current_milestone="${line#MILESTONE:}"
          ;;
        BODY_START)
          in_body=1
          ;;
        BODY_END)
          # Create issue
          body_file=$(mktemp)
          echo "$current_body" > "$body_file"
          echo "    Creating: $current_title"
          gh issue create \
            --title "$current_title" \
            --label "$current_labels" \
            --milestone "$current_milestone" \
            --body-file "$body_file" 2>&1 | sed 's/^/      /'
          rm "$body_file"
          in_body=0
          current_body=""
          ;;
        *)
          if [[ $in_body -eq 1 ]]; then
            current_body+="$line"$'\n'
          fi
          ;;
      esac
    done < "$tmp"
    rm "$tmp"
  fi
done

echo
echo "==== Done ===="
if $DRY_RUN; then
  echo "ℹ️  Re-run with --apply to execute for real"
  echo "   Will create: 8 master + 55 sub-tickets = 63 GitHub issues"
else
  echo "✅ All commands executed. Check GitHub issues."
fi
