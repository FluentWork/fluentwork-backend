#!/usr/bin/env python3
"""Continue from where create-issues.py stopped: create B24, B25 masters + 46 sub-tickets."""

import re
import sys
import subprocess
import json
from pathlib import Path

REPO = "FluentWork/fluentwork-backend"
SCRATCH_DIR = Path(".scratch/issues/2026-09-06-W3-backend-tickets")
MASTER_NUMS = {
    "B17": 44, "B18": 45, "B19": 46, "B21": 47, "B22": 48, "B23": 49
}


def gh(args, check=True):
    result = subprocess.run(
        ["gh"] + args, capture_output=True, text=True, check=check
    )
    return result.stdout, result.stderr


def create_issue(title, labels, milestone, body):
    args = ["issue", "create", "--title", title, "--body", body]
    if labels:
        args.extend(["--label", ",".join(labels)])
    if milestone:
        args.extend(["--milestone", milestone])
    out, _ = gh(args)
    url = out.strip()
    m = re.search(r"/issues/(\d+)", url)
    return int(m.group(1)) if m else None


def create_master(code, fname, title, labels, milestone):
    """Create master issue from §0 section."""
    content = (SCRATCH_DIR / fname).read_text()
    match = re.search(
        r"## §0 Master Issue Body\(整段可粘贴\)\s*\*\*Title\*\*:.*?\*\*Milestone\*\*:.*?\n```markdown\n(.*?)```",
        content, re.DOTALL
    )
    if not match:
        print(f"  ✗ Could not extract master body from {fname}")
        return None
    body = match.group(1).strip()
    num = create_issue(title, labels, milestone, body)
    print(f"  ✓ Created: {title} → #{num}")
    return num


def parse_subtickets(content):
    section_pattern = re.compile(
        r"## §(\d+) (T-[A-Z0-9-]+) — (.+?)\n"
        r"\*\*Labels\*\*: (.+?)\n"
        r"\*\*Milestone\*\*: (.+?)\n"
        r"\n### Body\n\n```markdown\n(.*?)```",
        re.DOTALL
    )
    tickets = []
    for m in section_pattern.finditer(content):
        num, ticket_id, title, labels, ms, body = m.groups()
        clean_labels = [l.strip().strip("`") for l in labels.split(",") if l.strip()]
        tickets.append({
            "id": ticket_id,
            "title": title.strip(),
            "labels": clean_labels,
            "milestone": ms.strip().strip("`"),
            "body": body.strip(),
        })
    return tickets


def create_subtickets(code, fname):
    """Create all sub-tickets for a skill."""
    content = (SCRATCH_DIR / fname).read_text()
    tickets = parse_subtickets(content)
    master_num = MASTER_NUMS.get(code)
    if not master_num:
        print(f"  ⚠️ No master for {code}")
        return []

    nums = []
    for t in tickets:
        body = t["body"].replace(f"Master: {code}", f"Master: {code} (#{master_num})")
        full_title = f"[{t['id']}] {t['title']}"
        try:
            num = create_issue(full_title, t["labels"], t["milestone"], body)
            print(f"    ✓ {t['id']} → #{num}")
            nums.append({"id": t["id"], "title": t["title"], "num": num})
        except subprocess.CalledProcessError as e:
            print(f"    ✗ {t['id']}: {e.stderr[:200]}")
    return nums


def main():
    print("=== Continuing W3 Backend Issue Creation ===\n")

    # Phase A: Create remaining master issues (B24, B25)
    print("Creating remaining master issues:")
    MASTER_NUMS["B24"] = create_master(
        "B24", "10-skill-B24-session-history.md",
        "B24: 历史回顾 API(含会话列表)",
        ["backend", "v2.0-blocker", "priority: P2", "sessions", "api"],
        "V2.0 W3-W4"
    )
    MASTER_NUMS["B25"] = create_master(
        "B25", "05-skill-B25-F3-pin-favorite.md",
        "B25: F3 收藏置顶后端(PATCH 拆分)",
        ["backend", "v2.0-blocker", "priority: P1", "corpus", "api-change"],
        "V2.0 W3"
    )

    # Phase B: Create all sub-tickets
    print("\nCreating sub-tickets:")
    subtickets = {}
    skill_files = [
        ("B17", "03-skill-B17-TTS-Provider.md"),
        ("B18", "06-skill-B18-review-eval.md"),
        ("B19", "04-skill-B19-B7-hit-injection.md"),
        ("B21", "07-skill-B21-materials.md"),
        ("B22", "08-skill-B22-drill-privacy.md"),
        ("B23", "09-skill-B23-topic-cards.md"),
        ("B24", "10-skill-B24-session-history.md"),
        ("B25", "05-skill-B25-F3-pin-favorite.md"),
    ]
    for code, fname in skill_files:
        print(f"\n  {code}:")
        subtickets[code] = create_subtickets(code, fname)

    # Save mapping
    mapping = {"masters": MASTER_NUMS, "subtickets": subtickets}
    Path(".scratch/issues/2026-09-06-W3-backend-tickets/issue-mapping.json").write_text(
        json.dumps(mapping, indent=2, ensure_ascii=False)
    )
    print(f"\n✅ Mapping saved to issue-mapping.json")
    print(f"   Masters: {list(MASTER_NUMS.values())}")
    print(f"   Sub-tickets total: {sum(len(v) for v in subtickets.values())}")


if __name__ == "__main__":
    main()
