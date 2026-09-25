#!/usr/bin/env bash
#
# 断言开发服务监管器的行为。
#
# 真源：docs/109_开发启动脚本监管_实现说明.md
#
# 为什么需要它：这份实现修的是三个**在旧脚本里没有任何东西会变红**的坑
# （go run 包装进程泄漏占端口 / 端口冲突被报成「服务起不来」/ 失败无从诊断）。
# 没有断言的话，这三条随时可以长回去 —— 而它们的表现又是「起不来」这种
# 远离原因的样子。所以这里把三条各自的**可观察后果**钉成断言：
#
#   1. $! 必须是服务本体，不是包装进程 —— 断言注册到的 pid 的命令行就是它自己。
#   2. 停止必须收掉整个进程组 —— 断言被启动的 shell 派生的子进程也死了。
#   3. 端口被**外来**进程占着时必须拒绝并**不动它** —— 断言 nc 在预检后仍然活着。
#
# 另外钉住三段诊断的用词（构建失败 ≠ 起不来；端口没绑定 ≠ 不应答），
# 因为把这三段混成一句话正是旧脚本的毛病。
#
# 只使用 bash 3.2 语法（macOS 自带 /bin/bash 是 3.2.57）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT/scripts/lib/dev-service.sh"

if [ ! -f "$LIB" ]; then
  echo "✘ 找不到 $LIB" >&2
  exit 1
fi

# 这份检查会真的起进程、真的占端口，所以用一个只属于它的日志/产物目录，
# 不污染开发用的 .dev-logs。
tmp="$(mktemp -d)"
export DEV_LOG_DIR="$tmp/logs"
export DEV_BIN_DIR="$tmp/bin"

PORT_NC=19981
PORT_SLEEP=19982
PORT_GROUP=19983
PORT_DEAD=19984
PORT_NBIND=19985

NC_PID=""
GROUP_CHILD_PID=""

cleanup() {
  [ -n "$NC_PID" ] && kill -9 "$NC_PID" 2>/dev/null || true
  [ -n "$GROUP_CHILD_PID" ] && kill -9 "$GROUP_CHILD_PID" 2>/dev/null || true
  # 注册表里可能还留着测试起的进程。这里**不要**重新 source 库 ——
  # 那会把 DEV_SERVICES 清空，于是恰好丢掉要回收的那些 PID。
  if command -v dev_stop_all >/dev/null 2>&1; then
    dev_stop_all >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

# shellcheck disable=SC1090
source "$LIB"

checked=0
failed=0

ok() {
  checked=$((checked + 1))
}

expect() {
  local label="$1" want="$2" got="$3"
  checked=$((checked + 1))
  if [ "$want" != "$got" ]; then
    printf '✘ %s\n    期望 [%s]\n    实际 [%s]\n' "$label" "$want" "$got" >&2
    failed=1
  fi
}

expect_contains() {
  local label="$1" needle="$2" haystack="$3"
  checked=$((checked + 1))
  case "$haystack" in
    *"$needle"*) ;;
    *)
      printf '✘ %s\n    输出里没有 [%s]\n    实际输出 [%s]\n' "$label" "$needle" "$haystack" >&2
      failed=1
      ;;
  esac
}

expect_not_contains() {
  local label="$1" needle="$2" haystack="$3"
  checked=$((checked + 1))
  case "$haystack" in
    *"$needle"*)
      printf '✘ %s\n    输出里不该出现 [%s]\n    实际输出 [%s]\n' "$label" "$needle" "$haystack" >&2
      failed=1
      ;;
  esac
}

dev_services_init

# ---------------------------------------------------------------------------
# 1. 「是不是本仓的服务」—— 安全边界。判错会去杀别人的进程。
# ---------------------------------------------------------------------------

if dev_is_our_service "$ROOT/bin/app-server"; then ok; else
  printf '✘ 本仓 bin/ 下的二进制应被认作自己人\n' >&2; failed=1; checked=$((checked+1))
fi

if dev_is_our_service "/Users/x/Library/Caches/go-build/ab/ab-hash-d/voice-gateway"; then ok; else
  printf '✘ 迁移期：go-build 缓存里同名产物应被认作自己人（否则老残留收不掉）\n' >&2; failed=1; checked=$((checked+1))
fi

# `go run` 的第二种落点：现编现链接时落在 $TMPDIR/go-buildNNNN/b001/exe/<name>。
# 实测：同一台机器上两种布局**同时存在**（缓存命中走 Caches，未命中走 TMPDIR），
# 只认前者会让「回收老残留」在一半情况下静默失效 —— 而失效的样子是
# 「端口被占着，脚本却说不属于本仓，于是不动它」，正是本 ticket 要修的病。
if dev_is_our_service "/var/folders/x2/kff15jq9/T/go-build33506635/b001/exe/voice-gateway"; then ok; else
  printf '✘ $TMPDIR/go-buildNNN/b001/exe 布局应被认作自己人（实测的第二种 go run 落点）\n' >&2; failed=1; checked=$((checked+1))
fi

if dev_is_our_service "/var/folders/x2/kff15jq9/T/go-build33506635/b001/exe/postgres"; then
  printf '✘ TMPDIR 构建目录里**别的**程序被误判成自己人（只该认本仓服务名）\n' >&2; failed=1; checked=$((checked+1))
else ok; fi

if dev_is_our_service "/usr/sbin/nginx"; then
  printf '✘ /usr/sbin/nginx 被误判成自己人 —— 这会去杀别人的进程\n' >&2; failed=1; checked=$((checked+1))
else ok; fi

if dev_is_our_service "nc -l $PORT_NC"; then
  printf '✘ nc 被误判成自己人\n' >&2; failed=1; checked=$((checked+1))
else ok; fi

if dev_is_our_service "/Users/x/Library/Caches/go-build/ab/ab-hash-d/postgres"; then
  printf '✘ go-build 缓存里的**别的**程序被误判成自己人（只该认本仓服务名）\n' >&2; failed=1; checked=$((checked+1))
else ok; fi

# ---------------------------------------------------------------------------
# 2. 注册表
# ---------------------------------------------------------------------------

dev_register alpha 111 19999 /tmp/alpha.log
dev_register beta 222 19998 /tmp/beta.log
expect "注册表 pid"  "111"           "$(dev_registry_field alpha pid)"
expect "注册表 port" "19998"         "$(dev_registry_field beta port)"
expect "注册表 log"  "/tmp/alpha.log" "$(dev_registry_field alpha log)"
expect "注册表名字列表" "alpha
beta" "$(dev_registry_names)"
dev_services_init

# ---------------------------------------------------------------------------
# 3. 端口探测 + 外来进程必须不被碰
# ---------------------------------------------------------------------------

expect "空闲端口无占用者" "" "$(dev_port_holders $PORT_SLEEP)"
if dev_preflight_port svc "$PORT_SLEEP" 1 >/dev/null 2>&1; then ok; else
  printf '✘ 空闲端口应当预检通过\n' >&2; failed=1; checked=$((checked+1))
fi

if lsof -nP -iTCP:"$PORT_NC" -sTCP:LISTEN -t >/dev/null 2>&1; then
  echo "   （端口 $PORT_NC 已被占用，跳过 nc 相关断言）"
else
  nc -l "$PORT_NC" >/dev/null 2>&1 &
  NC_PID=$!
  disown "$NC_PID" 2>/dev/null || true
  sleep 1

  holders="$(dev_port_holders "$PORT_NC")"
  expect_contains "能探测到监听者" "$NC_PID" "$holders"

  # 外来进程：预检必须**拒绝**……
  if dev_preflight_port svc "$PORT_NC" 1 >/dev/null 2>&1; then
    printf '✘ 端口被外来进程占着时，预检应当失败\n' >&2; failed=1; checked=$((checked+1))
  else ok; fi

  # ……而且**不许碰它**。这是本文件最重要的一条断言。
  if kill -0 "$NC_PID" 2>/dev/null; then ok; else
    printf '✘ 预检杀掉了外来进程 —— 安全边界被破坏\n' >&2; failed=1; checked=$((checked+1))
  fi

  kill -9 "$NC_PID" 2>/dev/null || true
  NC_PID=""
  sleep 0.5
fi

# ---------------------------------------------------------------------------
# 4. 启动 = 本体（不是包装进程）
# ---------------------------------------------------------------------------

dev_start_service sleeper "$PORT_SLEEP" sleep 300 >/dev/null
sleeper_pid="$(dev_registry_field sleeper pid)"
sleeper_cmd="$(ps -o command= -p "$sleeper_pid" 2>/dev/null | sed 's/^[[:space:]]*//')"
expect "注册到的 pid 的命令行就是服务本体" "sleep 300" "$sleeper_cmd"

# 日志落盘：服务输出不该再出现在终端上，而该出现在文件里。
dev_start_service talker "$PORT_NBIND" bash -c 'echo "TALKER_SAYS_HELLO"; sleep 30' >/dev/null
sleep 1
if grep -q TALKER_SAYS_HELLO "$DEV_LOG_DIR/talker.log" 2>/dev/null; then ok; else
  printf '✘ 服务输出没有落到 %s/talker.log\n' "$DEV_LOG_DIR" >&2; failed=1; checked=$((checked+1))
fi

# ---------------------------------------------------------------------------
# 5. 停止 = 收掉整个进程组
# ---------------------------------------------------------------------------

dev_start_service grouper "$PORT_GROUP" \
  bash -c "sleep 300 & echo \$! > '$tmp/child.pid'; wait" >/dev/null
sleep 1
GROUP_CHILD_PID="$(cat "$tmp/child.pid" 2>/dev/null || true)"
if [ -n "$GROUP_CHILD_PID" ] && kill -0 "$GROUP_CHILD_PID" 2>/dev/null; then ok; else
  printf '✘ 没能起出用于验证进程组回收的子进程\n' >&2; failed=1; checked=$((checked+1))
fi

dev_stop_service grouper
sleep 1
if kill -0 "$GROUP_CHILD_PID" 2>/dev/null; then
  printf '✘ 停止后子进程 %s 仍活着 —— 只杀了直接子进程，没杀进程组\n' "$GROUP_CHILD_PID" >&2
  failed=1; checked=$((checked+1))
else ok; fi
GROUP_CHILD_PID=""

# 直接子进程也要死
dev_stop_service sleeper
if kill -0 "$sleeper_pid" 2>/dev/null; then
  printf '✘ 停止后 pid %s 仍活着\n' "$sleeper_pid" >&2; failed=1; checked=$((checked+1))
else ok; fi
dev_stop_service talker

# ---------------------------------------------------------------------------
# 6. 三段诊断的用词
# ---------------------------------------------------------------------------

# 6a. 构建失败必须说「构建失败」，不能说「起不来」。
build_out="$(dev_build bogus ./cmd/definitely-not-a-real-package 2>&1 || true)"
expect_contains "构建失败要说构建失败" "构建失败" "$build_out"
expect_not_contains "构建失败不该说成就绪失败" "没有就绪" "$build_out"

# 6b. 进程在就绪前退出 —— 报「退出」，并带上日志尾部。
dev_services_init
dev_start_service dying "$PORT_DEAD" bash -c 'echo "DYING_NOW"; exit 3' >/dev/null
sleep 1
dying_out="$(dev_wait_healthy dying "$PORT_DEAD" "http://127.0.0.1:$PORT_DEAD/healthz" 5 2>&1 || true)"
expect_contains "进程退出要报退出" "在就绪前退出" "$dying_out"
expect_contains "退出时要带日志尾部" "DYING_NOW" "$dying_out"

# 6c. 进程活着但端口始终不绑定 —— 报「端口没被绑定」，不是「不应答」。
dev_services_init
dev_start_service nobind "$PORT_NBIND" sleep 30 >/dev/null
nb_out="$(dev_wait_healthy nobind "$PORT_NBIND" "http://127.0.0.1:$PORT_NBIND/healthz" 1 2>&1 || true)"
expect_contains "没绑定端口要这么说" "始终没有被绑定" "$nb_out"
expect_not_contains "没绑定端口时不该说「不应答」" "不应答" "$nb_out"

# 6d. 状态行：没有监听时必须报红。
#
# 先取输出再断言，不要写成 `dev_status_line ... | grep -q`：脚本开了 pipefail，
# 而 dev_status_line 在「无监听」时**故意**返回 1，管道会被那个 1 带红，
# 于是断言在检查「无监听」这件事上永远失败。
dev_services_init
dev_register ghost 999999 "$PORT_NBIND" "$tmp/ghost.log"
ghost_line="$(dev_status_line ghost "$PORT_NBIND" 2>/dev/null || true)"
expect_contains "无监听时状态行应报红" "❌" "$ghost_line"

dev_stop_all >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
# 7. 静态：`$var` 后面紧跟非 ASCII 字符，必须写成 `${var}`
# ---------------------------------------------------------------------------
#
# 本项目特有的坑，写 lib 时实测撞到：本仓的输出与文档全是中文，而在 C locale
# 下 bash 把高位字节当成标识符字符 —— `echo "退出（pid $pid）"` 里的变量名会被
# 读成 `pid）`，在 set -u 下以「unbound variable」中断，而报出来的变量名是一串
# 乱码。崩的是脚本，报的位置也远离原因。
#
# 扫的是本仓自己的 shell 脚本。`[^ -~[:space:]]` 在 LC_ALL=C 下匹配的就是
# 高位字节与其它控制字符，正好是「变量名会被吞掉」的那个位置。
#
# 注释行要排除：这个形状本身就得在注释里写出来才讲得清（本文件与 lib 的头注
# 都举了 `$pid）` 这个例子），把示例也算成违规，规则就没法被写下来。
scan_files=("$ROOT"/scripts/lib/*.sh "$ROOT"/scripts/dev-*.sh "$ROOT"/scripts/dev-*.zsh)
bad="$(LC_ALL=C grep -nHE '\$[A-Za-z_][A-Za-z0-9_]*[^ -~[:space:]]' "${scan_files[@]}" 2>/dev/null \
  | awk '{ rest = $0; sub(/^[^:]+:[0-9]+:/, "", rest); if (rest ~ /^[ \t]*#/) next; print }' || true)"
if [ -z "$bad" ]; then
  ok
else
  printf '✘ 有 `$var` 后面紧跟非 ASCII 字符的写法（变量名会被吞掉）：\n%s\n' "$bad" >&2
  printf '    改成 ${var} 收尾。\n' >&2
  failed=1
  checked=$((checked + 1))
fi

# ---------------------------------------------------------------------------
# 8. 收尾不得声称「已停止」而实际停掉零个
# ---------------------------------------------------------------------------
#
# 端到端验证时实测撞到：端口预检失败 → exit 1 → EXIT trap 照样打
#     🛑 正在停止服务...  /  ✅ 已停止。
# 而那一刻一个服务都没起过。这是本 ticket 要消灭的那个形状的又一例 ——
# **看起来做了，其实没做**：这三个字会让人以为上一轮的服务被收了，
# 于是不再去看端口，而端口上恰恰还站着东西。

dev_services_init
empty_cleanup="$(dev_cleanup 2>&1 || true)"
expect "没起过服务时收尾不该声称停过什么" "" "$empty_cleanup"

dev_services_init
dev_register ghost2 999999 "$PORT_SLEEP" "$tmp/ghost2.log"
busy_cleanup="$(dev_cleanup 2>&1 || true)"
expect_contains "起过服务时收尾要说在停" "正在停止服务" "$busy_cleanup"
dev_services_init

# ---------------------------------------------------------------------------
# 9. 静态：日志目录只有一个所有者
# ---------------------------------------------------------------------------
#
# 实测撞到：dev-status.sh 里写死了 `$ROOT/.dev-logs/<name>.log`，于是
# `DEV_LOG_DIR=/tmp/x ./scripts/dev-status.sh` 报「.dev-logs/ 下没有日志」，
# 而日志就在 /tmp/x 里。同一个语义两个实现点 —— 且这次是**报告脚本**读错，
# 症状是「日志不见了」，比服务起不来更难查。
#
# 扫 `/.dev-logs` 而不是 `.dev-logs`：前者命中写死的形式 `$ROOT/.dev-logs`，
# 又不会误伤 usage 文本里的 ` .dev-logs/<name>.log`（它前面是空格，不是斜杠）。
# 扫描范围与本文件第 7 节一致：`dev-*.sh` 与 `lib/*.sh`，本文件自身不在内。
logdir_owner="lib/dev-service.sh"
bad_logdir="$(LC_ALL=C grep -nH '/\.dev-logs' "$ROOT"/scripts/lib/*.sh "$ROOT"/scripts/dev-*.sh 2>/dev/null \
  | awk -v owner="$logdir_owner" '
      { file = $0; sub(/:.*/, "", file);
        rest = $0; sub(/^[^:]+:[0-9]+:/, "", rest);
        if (rest ~ /^[ \t]*#/) next;
        if (file ~ owner"$") next;
        print }' || true)"
if [ -z "$bad_logdir" ]; then
  ok
else
  printf '✘ 有脚本写死了 .dev-logs 路径（日志目录的唯一所有者是 %s 里的 DEV_LOG_DIR）：\n%s\n' "$logdir_owner" "$bad_logdir" >&2
  printf '    改成引用 $DEV_LOG_DIR。\n' >&2
  failed=1
  checked=$((checked + 1))
fi

# ---------------------------------------------------------------------------
# 10. 静态：报告脚本不得拿「可能不存在的 CLI」当判据
# ---------------------------------------------------------------------------
#
# 实测：本机 PATH 里既没有 mysqladmin 也没有 redis-cli。dev-status.sh 原来用
# `mysqladmin ping` 判 MySQL 活没活 —— 于是它对「装没装、跑没跑」永远只能给出 ❌。
# **一个从不取真值的判据。** 而这比「判据写错」更坏：它给出的是一个确定的、
# 看起来很有信息量的答案。
#
# 只扫 dev-status.sh：MySQL/Redis 专用脚本（local-services-*、test-mysql-redis、
# dev-local-start）要求客户端在场是合理的 —— 你本来就在做 MySQL 的事。
# 但「看一眼全貌」这件事不该要求任何东西在场，所以它只用 lsof（一定在）。
bad_cli="$(LC_ALL=C grep -nHE 'mysqladmin|redis-cli' "$ROOT/scripts/dev-status.sh" 2>/dev/null \
  | awk '{ rest = $0; sub(/^[^:]+:[0-9]+:/, "", rest); if (rest ~ /^[ \t]*#/) next; print }' || true)"
if [ -z "$bad_cli" ]; then
  ok
else
  printf '✘ dev-status.sh 用可能不存在的 CLI 作判据：\n%s\n' "$bad_cli" >&2
  printf '    改用 lsof 看端口上有没有人听。\n' >&2
  failed=1
  checked=$((checked + 1))
fi

if [ "$failed" -ne 0 ]; then
  printf '\n== 开发服务监管：%d 条断言中失败\n' "$checked" >&2
  exit 1
fi

echo "== 开发服务监管：$checked 条断言全部通过"
