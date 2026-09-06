#!/usr/bin/env python3
"""
Create all W3 backend issues from .scratch/issues/2026-09-06-W3-backend-tickets/

Phases:
  1. Close already-fixed #43 and #42
  2. Comment skills-to-ticket breakdown on #28 and #21
  3. Create 8 master issues (B17/B18/B19/B21/B22/B23/B24/B25)
  4. Create 55 sub-tickets across 8 skills

Usage:
  python3 create-issues.py --dry-run   # default
  python3 create-issues.py --apply
"""

import re
import sys
import subprocess
import json
import time
from pathlib import Path

REPO = "FluentWork/fluentwork-backend"
SCRATCH_DIR = Path(".scratch/issues/2026-09-06-W3-backend-tickets")
APPLY = "--apply" in sys.argv


def run(cmd, capture=True, check=True):
    """Run shell command, return stdout."""
    result = subprocess.run(
        cmd, shell=True, capture_output=capture, text=True, check=check
    )
    return result.stdout.strip()


def gh(cmd_args, check=True):
    """Run gh command, return (stdout, stderr)."""
    cmd = ["gh"] + cmd_args
    result = subprocess.run(cmd, capture_output=True, text=True, check=check)
    return result.stdout, result.stderr


def close_issue(num, comment):
    """Close issue with comment."""
    if APPLY:
        full_comment = comment.strip() + "\n\n" + (
            "详见 docs/40_研发流程与协作/74_V2_0_凭证与P0修复验证日志_2026-09-06.md §3.1"
            if "keepalive" in comment.lower() or "5e3b8b0" in comment
            else "详见 74_ §3.1"
        )
        out, err = gh([
            "issue", "close", str(num),
            "--comment", full_comment
        ])
        print(f"  ✓ Closed #{num}: {out.splitlines()[-1] if out else 'ok'}")
    else:
        print(f"  [DRY] gh issue close {num} --comment '...'")


def comment_issue(num, body_file):
    """Post comment to existing issue."""
    if APPLY:
        out, _ = gh(["issue", "comment", str(num), "--body-file", str(body_file)])
        print(f"  ✓ Commented on #{num}: {out.splitlines()[-1] if out else 'ok'}")
    else:
        print(f"  [DRY] gh issue comment {num} --body-file {body_file}")


def create_issue(title, labels, milestone, body):
    """Create issue, return issue number."""
    args = [
        "issue", "create",
        "--title", title,
        "--body", body,
    ]
    if labels:
        args.extend(["--label", ",".join(labels)])
    if milestone:
        args.extend(["--milestone", milestone])

    if APPLY:
        out, _ = gh(args)
        # Parse URL from output
        url = out.strip()
        # URL format: https://github.com/owner/repo/issues/N
        m = re.search(r"/issues/(\d+)", url)
        num = int(m.group(1)) if m else None
        print(f"  ✓ Created: {title} → #{num}")
        return num
    else:
        print(f"  [DRY] gh issue create --title '{title}' --label '{','.join(labels)}' --milestone '{milestone}'")
        return None


# ========== Phase 1: Close fixed P0 issues ==========

def phase1_close():
    print("=" * 60)
    print("Phase 1: Close already-fixed P0 issues")
    print("=" * 60)

    close_issue(43, """✅ 代码修复 commit 5e3b8b0;单测 6/6 全绿(`TestHandleClientAudio_ProbeOnIdle` 等)。
真机长时回归纳入 W3 Day 1-3 联调范围,联调结果回写到 74_ §3.2。
详见 docs/40_研发流程与协作/74_V2_0_凭证与P0修复验证日志_2026-09-06.md §3.1""")

    close_issue(42, """✅ 代码修复 commit 77e0578+fde8390+64453e3;BadgeEmitter 7/7 + turn_id 2/2 全绿。
真机 `feedback.badge emitted` 验证纳入 W3 Day 1-3 联调范围,联调结果回写到 74_ §3.3。
详见 docs/40_研发流程与协作/74_V2_0_凭证与P0修复验证日志_2026-09-06.md §3.1""")


# ========== Phase 2: Comment breakdown on #28 / #21 ==========

def phase2_comment():
    print()
    print("=" * 60)
    print("Phase 2: Comment skills-to-ticket breakdown")
    print("=" * 60)

    # Extract just the §0 comment draft from each file (the master issue comment)
    for num, fname, label in [
        (28, "01-skill-28-eval-dataset.md", "#28 评估集"),
        (21, "02-skill-21-review-worker.md", "#21 review worker"),
    ]:
        body_file = SCRATCH_DIR / fname
        # Extract just the master comment section (§0)
        content = body_file.read_text()
        match = re.search(
            r"## §0 Master Issue 评论草稿\(贴 #\d+\)\s*```markdown\n(.*?)```",
            content, re.DOTALL
        )
        if match:
            comment = match.group(1).strip()
            tmp_file = Path(f"/tmp/comment-{num}.md")
            tmp_file.write_text(comment)
            comment_issue(num, tmp_file)
            if APPLY:
                tmp_file.unlink()
        else:
            print(f"  ✗ Could not extract comment from {fname}")


# ========== Phase 3: Create 8 master issues ==========

MASTERS = [
    # (code, file, title, labels, milestone)
    ("B17", "03-skill-B17-TTS-Provider.md",
     "B17: TTS Provider + 流式集成",
     ["backend", "v2.0-blocker", "priority: P0", "tts"],
     "V2.0 W3"),
    ("B18", "06-skill-B18-review-eval.md",
     "B18: review eval 真实 LLM 接入",
     ["backend", "v2.0-blocker", "priority: P0", "review", "llm"],
     "V2.0 W3-W5"),
    ("B19", "04-skill-B19-B7-hit-injection.md",
     "B19: B7 命中信号 → LLM 上下文注入",
     ["backend", "v2.0-blocker", "priority: P0", "corpus", "b7"],
     "V2.0 W3"),
    ("B21", "07-skill-B21-materials.md",
     "B21: 素材模块(A1/A2 后端)",
     ["backend", "v2.0-blocker", "priority: P1", "materials", "llm"],
     "V2.0 W3-W4"),
    ("B22", "08-skill-B22-drill-privacy.md",
     "B22: 闪测模块 + A4 隐私删除",
     ["backend", "v2.0-blocker", "priority: P0", "drill", "privacy", "migration"],
     "V2.0 W3-W4"),
    ("B23", "09-skill-B23-topic-cards.md",
     "B23: 话题卡生成(每日调度)",
     ["backend", "v2.0-blocker", "priority: P1", "topic", "llm", "scheduler"],
     "V2.0 W3-W4"),
    ("B24", "10-skill-B24-session-history.md",
     "B24: 历史回顾 API(含会话列表)",
     ["backend", "v2.0-blocker", "priority: P2", "sessions", "api"],
     "V2.0 W3-W4"),
    ("B25", "05-skill-B25-F3-pin-favorite.md",
     "B25: F3 收藏置顶后端(PATCH 拆分)",
     ["backend", "v2.0-blocker", "priority: P1", "corpus", "api-change"],
     "V2.0 W3"),
]


def phase3_masters():
    print()
    print("=" * 60)
    print("Phase 3: Create 8 master issues")
    print("=" * 60)

    master_nums = {}
    for code, fname, title, labels, milestone in MASTERS:
        body_file = SCRATCH_DIR / fname
        content = body_file.read_text()

        # Extract §0 Master Issue Body section (the master body)
        match = re.search(
            r"## §0 Master Issue Body\(整段可粘贴\)\s*\*\*Title\*\*:.*?\*\*Milestone\*\*:.*?\n```markdown\n(.*?)```",
            content, re.DOTALL
        )
        if not match:
            print(f"  ✗ Could not extract master body from {fname}")
            continue

        body = match.group(1).strip()
        num = create_issue(title, labels, milestone, body)
        master_nums[code] = num

    return master_nums


# ========== Phase 4: Create 55 sub-tickets ==========

def parse_subtickets(content):
    """Parse all sub-ticket sections from a skill file."""
    tickets = []

    # Pattern: ## §N T-XXX-M — Title (sub-ticket header)
    # Each sub-ticket has: Labels, Milestone, ### Body
    section_pattern = re.compile(
        r"## §(\d+) (T-[A-Z0-9-]+) — (.+?)\n"
        r"\*\*Labels\*\*: (.+?)\n"
        r"\*\*Milestone\*\*: (.+?)\n"
        r"\n### Body\n\n```markdown\n(.*?)```",
        re.DOTALL
    )

    for match in section_pattern.finditer(content):
        num, ticket_id, title, labels, milestone, body = match.groups()
        # Strip backticks and whitespace
        clean_labels = [l.strip().strip("`") for l in labels.split(",") if l.strip()]
        tickets.append({
            "num": num,
            "id": ticket_id,
            "title": title.strip(),
            "labels": clean_labels,
            "milestone": milestone.strip().strip("`"),
            "body": body.strip(),
        })

    return tickets


def phase4_subtickets(master_nums):
    print()
    print("=" * 60)
    print("Phase 4: Create sub-tickets")
    print("=" * 60)

    subticket_nums = {}
    for code, fname, _, _, _ in MASTERS:
        body_file = SCRATCH_DIR / fname
        content = body_file.read_text()
        tickets = parse_subtickets(content)

        print(f"\n  {code}: {len(tickets)} sub-tickets from {fname}")
        master_num = master_nums.get(code)
        if not master_num and APPLY:
            print(f"    ⚠️  No master issue number for {code}, skipping")
            continue

        subticket_nums[code] = []
        for t in tickets:
            # Append master reference to body
            body_with_master = t["body"]
            if APPLY and master_num:
                # Update the Master: B17 line to actual issue number
                body_with_master = body_with_master.replace(
                    f"Master: {code}",
                    f"Master: {code} (#{master_num})"
                )

            full_title = f"[{t['id']}] {t['title']}"
            num = create_issue(full_title, t["labels"], t["milestone"], body_with_master)
            subticket_nums[code].append({"id": t["id"], "title": t["title"], "num": num})

    return subticket_nums


# ========== Main ==========

def main():
    print(f"\n=== W3 Backend Issues Creation ===")
    print(f"Mode: {'APPLY' if APPLY else 'DRY-RUN'}")
    print(f"Scratch dir: {SCRATCH_DIR}")

    phase1_close()
    phase2_comment()
    master_nums = phase3_masters()
    subticket_nums = phase4_subtickets(master_nums)

    # Print summary
    print()
    print("=" * 60)
    print("Summary")
    print("=" * 60)
    print(f"Masters: {sum(1 for v in master_nums.values() if v)} created")
    print(f"Sub-tickets: {sum(len(v) for v in subticket_nums.values())} planned")
    for code, tickets in subticket_nums.items():
        nums = [str(t['num']) for t in tickets if t['num']]
        print(f"  {code}: master={master_nums.get(code)}, sub-tickets={','.join(nums) if nums else 'pending'}")

    # Save mapping for git commit
    if APPLY:
        mapping = {
            "masters": master_nums,
            "subtickets": {
                code: [{"id": t["id"], "title": t["title"], "num": t["num"]} for t in tickets]
                for code, tickets in subticket_nums.items()
            }
        }
        Path(".scratch/issues/2026-09-06-W3-backend-tickets/issue-mapping.json").write_text(
            json.dumps(mapping, indent=2, ensure_ascii=False)
        )
        print(f"\nMapping saved to .scratch/issues/2026-09-06-W3-backend-tickets/issue-mapping.json")


if __name__ == "__main__":
    main()
