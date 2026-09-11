#!/usr/bin/env python3
"""判定「一个练习会话里, 上游 duplex 被开了几次」。

上游 duplex 会话是**有状态**的: 它持有这一场对话的历史。所以

    一个 app 会话窗口内出现第二次 voice.duplex.open
    = 第二段之后的所有轮次都跑在一个**没有历史**的会话里

用户看到的现象是"AI 变笨了" —— 上一轮说过的东西, 这一轮它不认得。而日志里
那只是一行 INFO。(P0-9)

  ./scripts/check-duplex-lifetime.py /tmp/fluentwork-backend.log
  ./scripts/check-duplex-lifetime.py --gaps /tmp/fluentwork-backend.log

退出码: 0 = 每个会话窗口只开了一次; 1 = 发现重开(上下文很可能已丢)。

== 读这个日志前必须知道的一件事 ==

`session_id` 这个字段在本仓的日志里**指两个不同的东西**:

  voice.duplex.*                  → **厂商侧**会话 id
  turn result captured / 重开那行  → **我们的 app** 会话 id

两者毫无关系, 都是 uuid 形状, 而且都叫 session_id。本脚本按这个区分来分组;
手工读日志时若当成一回事, 会得出完全错误的结论(比如以为"provider 换了"就是
"用户换了会话")。这也正是这个脚本存在的原因 —— 下一次不用再推一遍。
"""

import argparse
import json
import re
import sys
from collections import defaultdict

# 会话窗口的边界。我们的 app 会话开始/结束。
LIFECYCLE = "voice.session_lifecycle"
# 上游 duplex 的开启。`session_id` 是**厂商侧** id。
DUPLEX_OPEN = "voice.duplex.open"
# 显式的重开信号 —— 这一行说的是"旧的写不进去了, 我用同一个 ticket 又开了一个"。
REOPEN = "provider reopened after audio forward failure"
# 一轮结束。用来算"轮与轮之间的空闲"。
TURN_DONE = "turn result captured"

TIME_RE = re.compile(r"^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2}\.\d+)")


def parse_ts(raw):
    """'2026-09-11T23:04:05.618123+08:00' → 当天的秒数。跨天不处理, 一天够用。"""
    m = TIME_RE.match(raw or "")
    if not m:
        return None
    h, mi, s = m.group(2).split(":")
    return int(h) * 3600 + int(mi) * 60 + float(s)


def fmt_gap(seconds):
    if seconds is None:
        return "?"
    if seconds >= 60:
        return f"{int(seconds // 60)}m{seconds % 60:.0f}s"
    return f"{seconds:.1f}s"


def load(paths):
    """按时间顺序读出所有相关事件。多份日志按文件顺序拼接。"""
    events = []
    for path in paths:
        try:
            handle = open(path, encoding="utf-8", errors="replace")
        except OSError as exc:
            print(f"cannot read {path}: {exc}", file=sys.stderr)
            continue
        with handle:
            for line in handle:
                line = line.strip()
                if not line.startswith("{"):
                    continue
                try:
                    row = json.loads(line)
                except json.JSONDecodeError:
                    continue
                msg = str(row.get("msg", ""))
                ts = parse_ts(row.get("time", ""))
                if ts is None:
                    continue
                events.append((ts, msg, row))
    events.sort(key=lambda e: e[0])
    return events


def analyze(events):
    """把一个日志切成若干个「会话窗口」, 每个窗口里数 duplex 开了几次。"""
    windows = []
    current = None

    for ts, msg, row in events:
        if msg.startswith(LIFECYCLE) and msg.endswith("start"):
            current = {"start": ts, "opens": [], "turns": [], "reopens": []}
            windows.append(current)
            continue
        if current is None:
            # 日志是从中途开始的(常见的: 只截了一段)。那就自己开一个窗口,
            # 免得整段被丢掉 —— 丢掉的那一段往往正是要看的那一段。
            current = {"start": ts, "opens": [], "turns": [], "reopens": []}
            windows.append(current)

        if msg.startswith(DUPLEX_OPEN) and msg.endswith("done"):
            current["opens"].append((ts, str(row.get("session_id"))[:8]))
        elif msg.startswith(REOPEN):
            current["reopens"].append((ts, str(row.get("session_id"))[:8], str(row.get("original_err", ""))[:80]))
        elif msg.startswith(TURN_DONE):
            current["turns"].append(ts)

    # 轮与轮之间的空闲 —— P0-9 的自变量。
    #
    # 每个间隔都带上它跨的是什么, 因为这两类间隔含义完全不同:
    #   open → turn_end  是**轮内**耗时(业务本身花掉的)
    #   turn_end → open  才是**轮间静默**(上游在这段时间里收不到任何东西)
    # 只报数字会让人把前者读成后者, 而那正好会把结论读反。
    for win in windows:
        win["gaps"] = []
        anchors = sorted(
            [(t, "duplex.open") for t, _ in win["opens"]] + [(t, "turn.end") for t in win["turns"]]
        )
        for i in range(1, len(anchors)):
            before, after = anchors[i - 1], anchors[i]
            win["gaps"].append((before[1], after[1], after[0] - before[0], after[0]))
    return windows


def report(windows, show_gaps):
    problems = 0
    for index, win in enumerate(windows, start=1):
        opens = win["opens"]
        if not opens:
            # 只有 lifecycle 没有 duplex: 会话开了没连上, 或者这段日志被截了。
            # 不当作问题 —— 它不是本脚本要判定的东西。
            continue

        clock = f"{int(win['start'] // 3600):02d}:{int(win['start'] % 3600 // 60):02d}:{win['start'] % 60:05.2f}"
        print(f"会话窗口 #{index}  自 {clock}   duplex 开了 {len(opens)} 次")

        previous = None
        for ts, provider in opens:
            gap = "" if previous is None else f"   距上次开启 {fmt_gap(ts - previous)}"
            stamp = f"{int(ts // 3600):02d}:{int(ts % 3600 // 60):02d}:{ts % 60:05.2f}"
            print(f"  {stamp}  open.done  provider={provider}{gap}")
            previous = ts

        for ts, app_session, err in win["reopens"]:
            stamp = f"{int(ts // 3600):02d}:{int(ts % 3600 // 60):02d}:{ts % 60:05.2f}"
            print(f"  {stamp}  ⚠️ 重开 (app={app_session})  {err}")

        if len(opens) > 1:
            problems += 1
            print(
                f"  ⚠️ 这一场对话里上游会话换了 {len(opens) - 1} 次 —— "
                f"第 2 次之后的每一轮都在**没有历史**的会话里跑"
            )

        if show_gaps and win["gaps"]:
            print("  间隔 (跨越什么 → 多长):")
            reopen_at = [r[0] for r in win["reopens"]]
            for before, after, gap, after_ts in win["gaps"]:
                # 只有「轮间静默」是实验要读的那一列: 上游在那段时间里收不到任何东西。
                label = "轮间静默" if (before == "turn.end" and after == "duplex.open") else "轮内"
                caused = any(abs(after_ts - ts) < 1 for ts in reopen_at)
                print(f"    {fmt_gap(gap):>7}  {label}   ({before} → {after}){'   ← 这次之后重开' if caused else ''}")
        print()

    return problems


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("logs", nargs="+", help="后端日志 (dev-up.sh 的 stdout)")
    parser.add_argument(
        "--gaps",
        action="store_true",
        help="同时列出轮间空闲 —— 判别实验读的就是这一列与它上面那行的关系",
    )
    args = parser.parse_args()

    windows = analyze(load(args.logs))
    if not windows:
        print("日志里没有可解析的事件 (是 dev-up.sh 的 stdout 吗?)", file=sys.stderr)
        return 1

    problems = report(windows, args.gaps)
    if problems:
        print(f"exit 1: {problems} 个会话窗口里上游会话被重开过 (P0-9)")
        return 1
    print("exit 0: 每个会话窗口只开了一次上游会话")
    return 0


if __name__ == "__main__":
    sys.exit(main())
