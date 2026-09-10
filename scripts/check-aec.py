#!/usr/bin/env python3
"""从后端日志判定 AEC 是否生效。

AEC（回声消除）失效的判据是**可观测的**，不需要"听起来有没有回声"：

  1. 用户的转录里出现 AI 自己刚说过的话 —— 麦克风把扬声器放的声音录了进去
  2. AI 还在说话时被自己的声音触发了新一轮（自激）

做法：真机上让 AI 说一段话，**用户全程不出声**。然后跑这个脚本。

  ./scripts/check-aec.py /tmp/fluentwork-backend.log
  ./scripts/check-aec.py --session 2db38e02 /tmp/*.log

日志来源：dev-up.sh 的 stdout（重定向到文件即可）。判定依赖
`turn result captured` 行里的 `transcript` 与 `assistant_text` 两个字段。

退出码：0 = 本次没有自激证据；1 = 发现自激（AEC 很可能未生效）。
"""

import argparse
import glob
import json
import re
import sys
from collections import defaultdict

# 转录与上一轮 AI 文本重合到多少算"自激"。0.5 是保守值：真自激通常整个短语
# 都被转出来，重合率远高于此；而正常对话里用户复述 AI 用词也会有一点重合，
# 所以宁可漏报不可错报。
OVERLAP_RATIO = 0.5
MIN_OVERLAP_WORDS = 3
# 短于这个词数的转录不参与判定 —— "yes"/"ok" 这种和谁都能撞。
# 词数按 tokens() 计：ASCII 词 + 中文二元组。
MIN_TRANSCRIPT_WORDS = 4


def words(text):
    """Tokens for overlap: ASCII words plus CJK bigrams.

    The bigrams matter here. Sessions in this product are mixed-language — a
    learner asks in Chinese and the assistant answers in Chinese — so an
    ASCII-only tokenizer scores every Chinese transcript as empty and skips it.
    That is the majority of the traffic, and it would have made this check
    silently useless on exactly the sessions it exists to judge. Bigrams rather
    than single characters because single hanzi collide on 的/是/了 and would
    report an echo in any two Chinese sentences.
    """
    tokens = {w for w in re.findall(r"[a-zA-Z']+", text.lower()) if len(w) > 3}
    cjk = re.findall(r"[一-鿿]", text)
    tokens |= {"".join(pair) for pair in zip(cjk, cjk[1:])}
    return tokens


def load(paths):
    sessions = defaultdict(list)
    for path in paths:
        try:
            handle = open(path, encoding="utf-8", errors="replace")
        except OSError as error:
            print(f"跳过 {path}: {error}", file=sys.stderr)
            continue
        with handle:
            for line in handle:
                if "turn result captured" not in line:
                    continue
                try:
                    record = json.loads(line)
                except ValueError:
                    continue
                sessions[record.get("session_id", "?")].append({
                    "turn": record.get("active_turn_id", "?"),
                    "transcript": (record.get("transcript") or "").strip(),
                    "assistant": (record.get("assistant_text") or "").strip(),
                })
    return sessions


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("logs", nargs="+", help="后端日志文件（可用通配符）")
    parser.add_argument("--session", help="只看某个 session_id 前缀")
    args = parser.parse_args()

    paths = [p for pattern in args.logs for p in glob.glob(pattern)] or args.logs
    sessions = load(paths)
    if args.session:
        sessions = {k: v for k, v in sessions.items() if k.startswith(args.session)}

    if not sessions:
        print("没有找到任何 turn。日志路径对吗？后端跑起来了吗？", file=sys.stderr)
        return 2

    echoes = 0
    silent = 0
    for session_id, turns in sessions.items():
        for index in range(1, len(turns)):
            user = turns[index]["transcript"]
            previous = turns[index - 1]["assistant"]
            user_words = words(user)
            if not user:
                silent += 1
                continue
            if len(user_words) < MIN_TRANSCRIPT_WORDS:
                continue
            overlap = user_words & words(previous)
            ratio = len(overlap) / len(user_words)
            if ratio >= OVERLAP_RATIO and len(overlap) >= MIN_OVERLAP_WORDS:
                echoes += 1
                print(f"✘ 自激 [{session_id[:8]} {turns[index]['turn']}] "
                      f"重合 {ratio:.0%} {sorted(overlap)}")
                print(f"    用户转录: {user[:100]}")
                print(f"    上一轮 AI: {previous[:100]}")

    print()
    print(f"会话 {len(sessions)} 个，自激 {echoes} 处，空转录轮次 {silent} 处。")
    if echoes:
        print("→ 有自激证据。AEC 很可能未生效，见 meta 77_ §3.2 与 §3.4 的退路。")
        return 1
    print("→ 没有自激证据。注意：这只在**用户确实静默**的前提下才算 AEC 生效；")
    print("  若每轮用户都在说话，本脚本什么也证明不了。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
