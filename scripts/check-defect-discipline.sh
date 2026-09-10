#!/usr/bin/env bash
#
# 检查缺陷修复纪律留下的凭据。
# 真源：fluentwork-meta/agents/shared/defect-fix-discipline.md
#
# 规则要求实现说明里必须有一节「测试」，写明：哪个测试复现了这个缺陷、
# 它修复前的实际输出、修复后的门禁证据。本脚本检查这一节**存在**。
# 它检查不了内容是否诚实 —— 那需要人看。但"忘了写"是可以自动挡住的。
#
# 范围刻意限定为**本次工作区改动过的**实现说明，不追溯历史：
# 该规则从 docs/36 开始生效，之前的文档早于规则。追溯会让这个检查长期红着，
# 而一个长期红的检查等于没有检查。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# 已跟踪文件的改动（含已暂存）+ 未跟踪的新文件。
#
# `-z` 是必须的，不是风格问题：git 默认 core.quotePath=true，会把非 ASCII
# 文件名输出成带引号的转义串（"docs/99_\346\265\213..."），路径随即失效。
# 而本仓的文档名全是中文 —— 用默认输出的版本会让这个脚本对每一篇新文档
# 都静默放行，也就是一个永远通过的检查。
changed() {
  {
    git diff --name-only -z --diff-filter=ACMR HEAD -- docs 2>/dev/null || true
    git ls-files -z --others --exclude-standard -- docs 2>/dev/null || true
  } | tr '\0' '\n' | sort -u
}

checked=0
failed=0

# 进程替换而非管道：管道会让 while 跑在子壳里，failed/checked 传不出来。
while IFS= read -r file; do
  [ -n "$file" ] || continue
  # 只管实现说明；其他文档不受本规则约束。
  case "$file" in
    *实现说明*.md) ;;
    *) continue ;;
  esac
  [ -f "$file" ] || continue

  checked=$((checked + 1))

  # 「测试」或「门禁」小节（doc 42-49 用前者，doc 50 用后者）。
  if grep -qE '^##+ .*(测试|门禁|验证)' "$file"; then
    continue
  fi

  # 规则允许三类例外，但必须在实现说明里写明。短语取自规则原文。
  if grep -q '红/绿不适用' "$file"; then
    continue
  fi

  echo "✘ $file" >&2
  echo "    缺少「测试」/「门禁」小节，也没有写明『红/绿不适用』。" >&2
  echo "    规则：fluentwork-meta/agents/shared/defect-fix-discipline.md" >&2
  failed=1
done < <(changed)

if [ "$failed" -ne 0 ]; then
  exit 1
fi

if [ "$checked" -eq 0 ]; then
  echo "== 缺陷修复纪律：本次没有改动实现说明"
else
  echo "== 缺陷修复纪律：$checked 篇实现说明均带凭据"
fi
