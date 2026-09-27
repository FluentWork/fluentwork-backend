#!/usr/bin/env bash
#
# 断言落地门禁（scripts/dev-check.sh）自身的形状。
#
# 真源：AGENTS.md Local Rule 1 里那份步骤清单。
#
# 为什么需要它：门禁是本仓唯一一条**跑很多遍**的防线 —— 数据竞争这类缺陷按
# 调度发生、不按命令发生，只有跑得足够频繁的东西才会持续撞到它。而门禁的强度
# 不写在任何 Go 代码里，只由几行 shell 的写法承载：
#
#   1. 测试那一步带不带 `-race`。去掉一个词，整整一类缺陷（数据竞争）对整条
#      门禁完全不可见 —— 而门禁照样打印 "All checks passed."。
#      `BE-S0-7`（DuplexSession.conn 的竞态）就是这么潜伏到 2026-09-27 的。
#   2. 开头有没有 `set -euo pipefail`。去掉它，后面每一步都从「断言」退化成
#      「建议」：红的那一步不再中止整条运行。门禁照样打印 All checks passed.
#   3. 每个 `scripts/check-*.sh` 有没有被门禁真的调用。写了一个检查脚本却没接
#      进门禁，是「有保护」与「只是多了个文件」的差别。
#   4. 测试那一步带不带 `-count>1`。这是与第 1 条同源的另一半：门禁的**选项**也是
#      覆盖范围。固定 `-count=1` 时，「包级全局状态跨测试残留」这一类缺陷（断言一个
#      进程级计数器的绝对值）对全绿门禁完全不可见 —— `BE-S2-9`（tts 的 routeHits
#      在 `-count=2` 时报 "expected 1 hit for voice-a, got 2"）就是这么潜伏的。
#      `-race` 照不到它，`-count=1` 照不到它，只有跑第二遍才照得到。
#
# 这四条的共同点：**一次一个词的编辑就能毁掉，而毁掉之后没有任何东西会变红。**
# 所以每条各有一条断言。第 1 条与第 4 条断言的是**同一行**的两个词，各自独立。
#
# ⚠️ 这条守卫的边界，说清楚、不夸大：**它无法断言「自己被门禁调用」。** 一旦有人
# 把 dev-check.sh 里那行调用删掉，这个脚本根本不会跑。剩下的只有 AGENTS.md /
# CLAUDE.md / README.md 里那份写死的步骤清单 —— 那是文档，不是机制。不声称有别的。
#
# 只使用 bash 3.2 语法（macOS 自带 /bin/bash 是 3.2.57）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATE="$ROOT/scripts/dev-check.sh"

if [ ! -f "$GATE" ]; then
  echo "✘ 找不到 $GATE" >&2
  exit 1
fi

checked=0
failed=0

ok() {
  checked=$((checked + 1))
}

fail() {
  checked=$((checked + 1))
  failed=1
  printf '✘ %s\n' "$1" >&2
}

# 有效行（去注释行、去空行），带行号 —— 点名证据时要把行号一起给出来。
gate_code="$(LC_ALL=C grep -n -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$GATE" || true)"

if [ -z "$gate_code" ]; then
  printf '✘ %s 里没有可解析的有效行 —— 提取分支失效，本脚本不能给出结论。\n' "$GATE" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# 1. 开头必须有 set -euo pipefail
# ---------------------------------------------------------------------------
if printf '%s\n' "$gate_code" | LC_ALL=C grep -q -E '^[0-9]+:set -euo pipefail$'; then
  ok
else
  fail "$GATE 里没有 'set -euo pipefail'。去掉它，后面每一步都从断言退化成建议（红的那一步不再中止整条运行），而门禁仍然打印 All checks passed."
fi

# ---------------------------------------------------------------------------
# 2. 测试那一步必须带 -race，且必须带 -count>1
# ---------------------------------------------------------------------------
test_lines="$(printf '%s\n' "$gate_code" | LC_ALL=C grep -E '^[0-9]+:go test([[:space:]]|$)' || true)"

if [ -z "$test_lines" ]; then
  # 反空洞：解析不到测试行时报「形状失效」，不能静默通过。
  fail "$GATE 里找不到以 'go test' 开头的命令行 —— 解析失效，本脚本不能给出结论（期望恰好一行）。"
else
  test_line_count="$(printf '%s\n' "$test_lines" | LC_ALL=C grep -c . || true)"
  if [ "$test_line_count" -ne 1 ]; then
    fail "$GATE 里有 $test_line_count 行 'go test' 命令，期望恰好一行：
$test_lines"
  else
    if printf '%s' "$test_lines" | LC_ALL=C grep -q -- '-race'; then
      ok
    else
      fail "$GATE 的测试步骤不带 -race（${test_lines}）。去掉这一个词，数据竞争这一类缺陷对整条门禁完全不可见，而门禁仍然打印 All checks passed.（BE-S0-7 就是这么潜伏的）"
    fi
    # -count>1：只匹配 N>=2 —— `-count=1`、`-count=0`、没有 -count 都咬住；
    # `-count=3`/`-count=10` 放行（这条断言判的是「跑不止一遍」，不是「字面等于 2」）。
    # 边界（不夸大）：它只匹配文本。写 `-count=2 -count=1` 会命中，而后者覆盖前者。
    # 它防的是「有人把 -count=2 删掉/改回 1」这一种编辑，与第 1 条同源。
    if printf '%s' "$test_lines" | LC_ALL=C grep -qE -- '-count=([2-9]|[1-9][0-9]+)'; then
      ok
    else
      fail "$GATE 的测试步骤不带 -count>1（${test_lines}）。门禁只跑一种 -count 时，「包级全局状态跨测试残留」这一类缺陷（断言一个进程级计数器的绝对值）对全绿门禁完全不可见 —— 而门禁仍然打印 All checks passed.（BE-S2-9 就是这么潜伏的）"
    fi
  fi
fi

# ---------------------------------------------------------------------------
# 3. 每个 scripts/check-*.sh 都必须被门禁调用
# ---------------------------------------------------------------------------
#
# 例外只有一类：**需要凭据或网络的线上探针**，它跑不进离线门禁，只能手工跑。
# 名单写成「名字 → 为什么它不在门禁里」，不是 bool 白名单 ——
# 下一个人要能看出那一条为什么在（`BE-S0-7` 的 AST 守卫踩过这个：把合法用法
# 报成违规时，正确的反应是**收窄规则**，不是放宽名单）。
manual_probes="check-volc-voice-resource-ids.sh"

check_scripts="$(
  for f in "$ROOT"/scripts/check-*.sh; do
    [ -e "$f" ] || continue
    printf '%s\n' "${f##*/}"
  done
)"

if [ -z "$check_scripts" ]; then
  # 正向控制点：这条断言的提取分支（含它自己）坏掉时必须报形状失效，
  # 而不是「仓库干净」。
  fail "$ROOT/scripts 下找不到任何 check-*.sh —— 提取分支失效，本脚本不能给出结论。"
else
  # 反空洞：例外名单会随文件改名而腐烂，所以每一条都必须真的存在。
  for exempt in $manual_probes; do
    if [ -e "$ROOT/scripts/$exempt" ]; then
      ok
    else
      fail "例外名单里的 scripts/$exempt 不存在 —— 名单腐烂了，删掉这一条。"
    fi
  done

  while IFS= read -r name; do
    [ -n "$name" ] || continue
    skip=0
    for exempt in $manual_probes; do
      [ "$name" = "$exempt" ] && skip=1
    done
    if [ "$skip" -eq 1 ]; then
      ok
      continue
    fi
    invoked=0
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      case "$line" in
        *"$name"*) invoked=1 ;;
      esac
    done <<GATELINES
$gate_code
GATELINES
    if [ "$invoked" -eq 1 ]; then
      ok
    else
      fail "scripts/$name 存在，但 $GATE 没有调用它 —— 写了一个检查脚本却不接进门禁，它就不会在任何一次运行里跑到。"
    fi
  done <<SCRIPTS
$check_scripts
SCRIPTS
fi

if [ "$failed" -ne 0 ]; then
  printf '\n== 门禁自身形状：%d 条断言中失败\n' "$checked" >&2
  exit 1
fi

echo "== 门禁自身形状：$checked 条断言全部通过"
