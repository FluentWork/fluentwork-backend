#!/usr/bin/env bash
#
# 断言 dotenv 加载器的语义。
#
# 真源：docs/106_环境加载器统一_实现说明.md
#
# 为什么需要它：加载器曾经有两份 shell 实现，分歧到需要在 .env.volc.local
# 的文件头写一段话让使用者规避（docs/105_ F3）。两份实现各自都无法被断言 ——
# 这正是它们能分歧下去的原因。收成一个实现点之后，这个脚本让「语义」这件事
# 第一次变得可执行：改动 scripts/lib/load-env.sh 而破坏其中任何一条，这里变红。
#
# 只使用 bash 3.2 语法（macOS 自带 /bin/bash 是 3.2.57）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT/scripts/lib/load-env.sh"

if [[ ! -f "$LIB" ]]; then
  echo "✘ 找不到 $LIB" >&2
  exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/base.env" <<'FIXTURE'
# 顶格注释
  # 缩进注释（曾经会让 dev-local-start.sh 在 set -e 下直接退出）

PLAIN=plain
SPACED_KEY   =   spaced
INLINE=abc  # 这是注释，应被剥掉
HASH_IN_VALUE=abc#def
QUOTED="quoted value"
QUOTED_HASH="a # b"
SINGLE='single'
QUOTED_THEN_COMMENT="v" # 注释
EMPTY=
DUP=first
PRESET=from_file

1BAD=1
BAD-KEY=2
BOGUS_LINE_WITHOUT_EQUALS
FIXTURE

cat > "$tmp/overlay.env" <<'FIXTURE'
DUP=second
FIXTURE

checked=0
failed=0

expect() {
  local label="$1" want="$2" got="$3"
  checked=$((checked + 1))
  if [[ "$want" != "$got" ]]; then
    printf '✘ %s\n    期望 [%s]\n    实际 [%s]\n' "$label" "$want" "$got" >&2
    failed=1
  fi
}

# 在干净子壳里跑一遍加载器，打印每个键的解析结果。
# 用 eval 的 ${VAR-default} 而不是 [[ -v ]]，后者是 bash 4.2+。
probe() {
  PRESET=from_shell bash -c '
    set -euo pipefail
    source "$1"
    load_env_snapshot
    load_env_file "$2"
    load_env_file "$3"
    for k in PLAIN SPACED_KEY INLINE HASH_IN_VALUE QUOTED QUOTED_HASH SINGLE \
             QUOTED_THEN_COMMENT EMPTY DUP PRESET BOGUS_LINE_WITHOUT_EQUALS; do
      eval "v=\${$k-__UNSET__}"
      printf "%s\t%s\n" "$k" "$v"
    done
  ' _ "$LIB" "$tmp/base.env" "$tmp/overlay.env" 2>"$tmp/probe.err"
}

result="$(probe)"

value_of() {
  printf '%s\n' "$result" | awk -F'\t' -v k="$1" '$1==k { print substr($0, length($1)+2); found=1 } END { if (!found) print "__MISSING__" }'
}

expect "普通赋值"              "plain"          "$(value_of PLAIN)"
expect "键两端空白被 trim"      "spaced"         "$(value_of SPACED_KEY)"
expect "行内 # 注释被剥掉"      "abc"            "$(value_of INLINE)"
expect "值里的 # 无空格时保留"  "abc#def"        "$(value_of HASH_IN_VALUE)"
expect "双引号被剥掉"           "quoted value"   "$(value_of QUOTED)"
expect "引号内的 # 不当作注释"  "a # b"          "$(value_of QUOTED_HASH)"
expect "单引号被剥掉"           "single"         "$(value_of SINGLE)"
expect "引号后的 # 是注释"      "v"              "$(value_of QUOTED_THEN_COMMENT)"
expect "空值保持为空且已设置"   ""               "$(value_of EMPTY)"
expect "后加载的文件覆盖先加载" "second"         "$(value_of DUP)"
expect "真实环境变量压过文件"   "from_shell"     "$(value_of PRESET)"
expect "裸词行被跳过"           "__UNSET__"      "$(value_of BOGUS_LINE_WITHOUT_EQUALS)"

# 畸形行（非法标识符 / 缺 `=`）必须被报告，而不是静默吞掉。
# 这是「跳过」与「没看见」的区别 —— 前者可查，后者不可查。
for needle in '1BAD' 'BAD-KEY' 'BOGUS_LINE_WITHOUT_EQUALS'; do
  checked=$((checked + 1))
  if ! grep -q "$needle" "$tmp/probe.err"; then
    printf '✘ 畸形行 %s 没有被警告（应报告并跳过）\n' "$needle" >&2
    failed=1
  fi
done

# 文件缺失必须是可检测的返回码，而不是崩掉调用方。
if bash -c 'set -euo pipefail; source "$1"; load_env_snapshot; load_env_file "$2"' \
     _ "$LIB" "$tmp/does-not-exist.env" >/dev/null 2>&1; then
  checked=$((checked + 1))
  printf '✘ 文件缺失时 load_env_file 应返回非零\n' >&2
  failed=1
else
  checked=$((checked + 1))
fi

# 畸形行只应警告，不应中断 —— 加载器要能继续读后面的好行。
if bash -c '
  set -euo pipefail
  source "$1"
  load_env_snapshot
  load_env_file "$2"
  [[ "$PLAIN" == "plain" ]]
' _ "$LIB" "$tmp/base.env" >/dev/null 2>&1; then
  checked=$((checked + 1))
else
  checked=$((checked + 1))
  printf '✘ 畸形行中断了加载（后续正常行没被读到）\n' >&2
  failed=1
fi

if [[ "$failed" -ne 0 ]]; then
  printf '\n== 环境加载器：%d 条断言中失败\n' "$checked" >&2
  exit 1
fi

echo "== 环境加载器：$checked 条断言全部通过"
