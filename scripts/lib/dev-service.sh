#!/usr/bin/env bash
#
# 开发服务的构建 / 启动 / 监管 —— 唯一实现点。
#
# 真源：docs/109_开发启动脚本监管_实现说明.md
#
# 为什么需要它：三份启动脚本（dev-up.sh / dev-local-start.sh / dev-stack.zsh）
# 各写了一遍「起一个服务」，于是同一个坑也有三份。三条都是实测出来的：
#
#   1. `go run ./cmd/x &` 的 `$!` 是 go run 的**包装进程**，不是服务本体。
#      实测：kill 掉包装进程后服务本体仍然活着（wrapper 18941 已死 / child 18948 存活），
#      于是 Ctrl-C 之后残留的网关继续占着 :8081，下一次启动绑不上端口 ——
#      而脚本报的是「voice-gateway exited before becoming healthy」。
#      → 本文件改成**先构建成二进制、再直接运行二进制**：`$!` 就是本体；
#        并用独立进程组回收（`kill -TERM -$pid`），把它派生的子进程一起收。
#
#   2. 没有端口预检，所以「端口被占」被报成「服务起不来」；健康等待窗口固定
#      30s，冷编译超时也报同一句话。启动与编译挤在同一个窗口里，两者分不开。
#      → 本文件把三段分开报告：**构建** / **绑定端口** / **/healthz 应答**。
#
#   3. 服务日志直接落在终端，失败时无从诊断。
#      → 每个服务写 .dev-logs/<name>.log，失败时打印它的尾部。
#
# 只使用 bash 3.2 语法（macOS 自带 /bin/bash 是 3.2.57），因此不用
# declare -A / mapfile / ${var,,}。注册表是
# 「名字<TAB>PID<TAB>端口<TAB>日志」的换行分隔字符串。
#
# ⚠️ 本仓的输出与文档全是中文，于是有一个本项目特有的坑，写这个文件时实测撞到：
#    **`$var` 后面紧跟非 ASCII 字符时必须写成 `${var}`。**
#    在 C locale 下 bash 把高位字节当成标识符字符，所以
#        echo "退出（pid $pid）"
#    里的变量名会被读成 `pid）`，在 set -u 下以
#        dev-service.sh: line 277: pid<乱码>: unbound variable
#    中断 —— 崩的是脚本，报的变量名是一串乱码，位置也远离原因。
#    `scripts/check-dev-service.sh` 有一条静态断言在扫这个形状。
#
# 用法：
#   source "$ROOT/scripts/lib/dev-service.sh"
#   dev_services_init                              # 建目录、清注册表
#   dev_preflight_port app-server 8080             # 端口预检（回收自己的残留）
#   dev_build app-server ./cmd/app-server          # 失败即报构建错误
#   dev_start_service app-server 8080 "$DEV_BIN_DIR/app-server"
#   dev_wait_healthy app-server 8080 "http://127.0.0.1:8080/healthz" 90
#   dev_supervise                                  # 阻塞；任一个退出就报出来
#
# 前置：调用方必须先设好 $ROOT。

DEV_LOG_DIR="${DEV_LOG_DIR:-$ROOT/.dev-logs}"
DEV_BIN_DIR="${DEV_BIN_DIR:-$ROOT/bin}"
DEV_SERVICES=""

# ---------------------------------------------------------------------------
# 注册表
# ---------------------------------------------------------------------------

dev_services_init() {
  mkdir -p "$DEV_LOG_DIR" "$DEV_BIN_DIR"
  DEV_SERVICES=""
}

dev_register() {
  local name="$1" pid="$2" port="$3" log="$4"
  DEV_SERVICES="${DEV_SERVICES}${name}	${pid}	${port}	${log}
"
}

# dev_registry_field <name> <pid|port|log>
dev_registry_field() {
  local want="$1" field="$2" name pid port log
  while IFS=$'\t' read -r name pid port log; do
    [ -n "$name" ] || continue
    if [ "$name" = "$want" ]; then
      case "$field" in
        pid) printf '%s\n' "$pid" ;;
        port) printf '%s\n' "$port" ;;
        log) printf '%s\n' "$log" ;;
      esac
      return 0
    fi
  done <<< "$DEV_SERVICES"
  return 1
}

dev_registry_names() {
  local name pid port log
  while IFS=$'\t' read -r name pid port log; do
    [ -n "$name" ] || continue
    printf '%s\n' "$name"
  done <<< "$DEV_SERVICES"
}

# ---------------------------------------------------------------------------
# 端口
# ---------------------------------------------------------------------------

# 打印占用 <port> 的每个监听者：「pid<TAB>命令行」。空闲则无输出。
dev_port_holders() {
  local port="$1" pid cmd
  for pid in $(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null || true); do
    cmd="$(ps -o command= -p "$pid" 2>/dev/null | sed 's/^[[:space:]]*//')"
    printf '%s\t%s\n' "$pid" "$cmd"
  done
  return 0
}

# 这个命令行是不是本仓自己起的开发服务？
#
# 判据刻意保守：只有路径落在本仓 bin/ 下，或者命中 go 构建目录里与本仓服务
# 同名的产物，才算「我们的」。否则一律当成别人的进程，不碰。
# 这样迁移期（老进程由 `go run` 起）也能被回收。
#
# `go run` 的产物有**两种落点，同一台机器上实测同时存在**：
#   ~/Library/Caches/go-build/<xx>/<hash>-d/<name>     ← 缓存命中，直接 exec 缓存里的可执行文件
#   $TMPDIR/go-buildNNNN/b001/exe/<name>               ← 现编现链接（缓存未命中/改动后首次）
# 只认前者，会让「回收老残留」在一半情况下静默失效 —— 而失效的样子正是
# 「端口被占着，但脚本说不属于本仓，所以不动它」，也就是这次要修的病本身。
# 实测两例（同一次会话）：22736 → $TMPDIR/go-build33506635/b001/exe/voice-gateway；
#                        22786 → ~/Library/Caches/go-build/a0/a00d…-d/app-server。
dev_is_our_service() {
  local cmd="$1" base
  case "$cmd" in
    "$DEV_BIN_DIR"/*) return 0 ;;
    "$ROOT"/bin/*) return 0 ;;
  esac
  case "$cmd" in
    */go-build/*|*/go-build*/b001/exe/*)
      base="${cmd##*/}"
      case "$base" in
        app-server|voice-gateway|worker|corpus-seed) return 0 ;;
      esac
      ;;
  esac
  return 1
}

# dev_reclaim_ours <port> [quiet]
#
# 回收占用 <port> 的**本仓**残留服务（判据唯一收在 dev_is_our_service）。
# 外来进程只报告，quiet=1 时连报告也省。
# 返回 0 = 端口现在空闲；1 = 仍被占用。
#
# 这是「回收端口」这件事的唯一实现点：dev_preflight_port（启动前）与
# dev-down.sh（收尾）都走它，否则两处对「谁的进程」的判断会各自漂移。
dev_reclaim_ours() {
  local port="$1" quiet="${2:-0}"
  local holders pid cmd i

  holders="$(dev_port_holders "$port")"
  [ -n "$holders" ] || return 0

  while IFS=$'\t' read -r pid cmd; do
    [ -n "$pid" ] || continue
    if dev_is_our_service "$cmd"; then
      [ "$quiet" = "1" ] || printf '      → 是本仓的开发服务残留，回收 pid %s（进程组）\n' "$pid" >&2
      kill -TERM "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
    elif [ "$quiet" != "1" ]; then
      printf '      ⚠️  端口 %s 上是别人的进程，不动：pid %s  %s\n' "$port" "$pid" "$cmd" >&2
    fi
  done <<< "$holders"

  # 回收之后等端口真正释放（TCP 从 FIN 到释放要一点时间）
  for i in $(seq 1 40); do
    holders="$(dev_port_holders "$port")"
    if [ -z "$holders" ]; then
      [ "$quiet" = "1" ] || printf '      ✅ 端口 %s 已释放\n' "$port" >&2
      return 0
    fi
    sleep 0.25
  done
  return 1
}

# dev_preflight_port <name> <port> [kill_stale]
#
# 启动前的端口预检。0 = 端口可用；1 = 不可用（并已说明原因）。
# kill_stale 默认 1：占着端口的是我们自己的残留服务时，回收它。
dev_preflight_port() {
  local name="$1" port="$2" kill_stale="${3:-1}"
  local holders pid cmd foreign

  holders="$(dev_port_holders "$port")"
  [ -n "$holders" ] || return 0

  printf '⚠️  %s 要用的端口 %s 已被占用：\n' "$name" "$port" >&2
  while IFS=$'\t' read -r pid cmd; do
    [ -n "$pid" ] || continue
    printf '      pid %s  %s\n' "$pid" "$cmd" >&2
  done <<< "$holders"

  # 先判「有没有外来进程」。只要有一个，就什么都不动 —— 同端口上就算还有
  # 本仓的进程，杀掉它也不会让端口空出来，只会把现场搅乱。
  foreign=0
  while IFS=$'\t' read -r pid cmd; do
    [ -n "$pid" ] || continue
    dev_is_our_service "$cmd" || foreign=1
  done <<< "$holders"

  if [ "$foreign" = "1" ]; then
    cat >&2 <<EOF

✘ 端口 ${port} 上有**不属于本仓**的进程，脚本不会去动它。
  先确认那是什么（上面列了 pid 与命令行），然后二选一：
    - 停掉它；或
    - 换一个端口再启动（--port / --gateway-port）。
EOF
    return 1
  fi

  if [ "$kill_stale" != "1" ]; then
    cat >&2 <<EOF

✘ 端口 ${port} 被本仓的开发服务残留占着，而 --no-kill-stale 要求保持原样。
  去掉 --no-kill-stale 重新启动即可自动回收；或手工 kill 上面列出的 pid。
EOF
    return 1
  fi

  if ! dev_reclaim_ours "$port" 0; then
    printf '✘ 端口 %s 回收后仍未释放，剩下的占用者：\n' "$port" >&2
    while IFS=$'\t' read -r pid cmd; do
      [ -n "$pid" ] || continue
      printf '      pid %s  %s\n' "$pid" "$cmd" >&2
    done <<< "$(dev_port_holders "$port")"
    echo "  它们可能不响应 TERM。手工处理：kill -9 <pid>" >&2
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# 构建
# ---------------------------------------------------------------------------

# dev_build <name> <pkg>
#
# 成功：把二进制路径打到 **stdout**（只有路径，便于 `bin="$(dev_build ...)"`）。
#       进度与失败信息都走 stderr —— 否则 `$(...)` 会把进度行一起捕获进来，
#       拿到的「路径」变成多行文本，后面启动时才会以奇怪的方式失败。
# 失败：编译错误打到 stderr，返回 1。
#
# 构建与运行分开，是这份实现的核心：旧脚本把两者挤进同一个 30s 健康窗口，
# 于是「还在编译」和「起不来」在输出上一模一样。
dev_build() {
  local name="$1" pkg="$2"
  # 注意：不能写成 `local name="$1" out="$DEV_BIN_DIR/$name"` ——
  # `local` 是一条命令，所有参数在它执行**之前**展开，所以那里的 $name
  # 读的是外层（未定义）的变量，在 set -u 下会以「unbound variable」中断。
  local out="$DEV_BIN_DIR/$name" err start elapsed
  err="$DEV_LOG_DIR/$name.build.err"
  mkdir -p "$DEV_BIN_DIR" "$DEV_LOG_DIR"

  printf '   🔨 构建 %s (%s)\n' "$name" "$pkg" >&2
  start="$(date +%s)"
  if ! go build -o "$out" "$pkg" 2>"$err"; then
    elapsed=$(( $(date +%s) - start ))
    printf '✘ %s 构建失败（%ss）—— 这不是「服务起不来」，是编译不过：\n' "$name" "$elapsed" >&2
    sed 's/^/      /' "$err" >&2
    printf '      完整输出：%s\n' "$err" >&2
    return 1
  fi
  elapsed=$(( $(date +%s) - start ))
  printf '   ✅ %s 构建完成（%ss）→ %s\n' "$name" "$elapsed" "$out" >&2
  printf '%s\n' "$out"
  return 0
}

# ---------------------------------------------------------------------------
# 启动 / 停止
# ---------------------------------------------------------------------------

# dev_start_service <name> <port> <cmd> [args...]
#
# 用独立进程组启动，所以 `kill -TERM -$pid` 一次收掉本体与它派生的子进程。
# 必须用二进制本体启动（见文件头第 1 条），不要传 `go run ...`。
dev_start_service() {
  local name="$1" port="$2"
  shift 2
  local log="$DEV_LOG_DIR/$name.log"
  mkdir -p "$DEV_LOG_DIR"
  : > "$log"

  set -m
  "$@" >>"$log" 2>&1 &
  local pid=$!
  set +m
  # disown 只是把它从 bash 的作业表里摘掉，免得停止时 bash 再打一行
  # 「Terminated」通知；进程本身照旧由我们显式回收。
  disown "$pid" 2>/dev/null || true

  dev_register "$name" "$pid" "$port" "$log"
  printf '   ▶  %s 已启动  pid %s  端口 %s\n' "$name" "$pid" "$port"
  printf '      日志：%s\n' "$log"
}

dev_print_failure() {
  local name="$1" log
  log="$(dev_registry_field "$name" log 2>/dev/null || printf '%s' "$DEV_LOG_DIR/$name.log")"
  echo "" >&2
  echo "──── ${name} 的日志尾部（${log}）────" >&2
  if [ -f "$log" ]; then
    tail -n 30 "$log" >&2
  else
    echo "      （日志文件不存在）" >&2
  fi
  echo "────────────────────────────────" >&2
}

# dev_wait_healthy <name> <port> <url> [budget_sec]
#
# 三段分开报告，超时的那一段会写在错误里：
#   - 进程已经退出  → 报退出 + 日志尾部
#   - 端口还没绑定  → 报「仍在启动」
#   - 端口已绑定但没有 /healthz → 报「绑定了但不应答」
dev_wait_healthy() {
  local name="$1" port="$2" url="$3" budget="${4:-90}"
  local pid i ticks last_note="" note elapsed
  pid="$(dev_registry_field "$name" pid)"

  if [ -z "$pid" ]; then
    echo "✘ dev_wait_healthy: $name 不在注册表里" >&2
    return 1
  fi

  ticks=$(( budget * 2 ))
  for i in $(seq 1 "$ticks"); do
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "✘ ${name} 在就绪前退出（pid ${pid}）" >&2
      dev_print_failure "$name"
      return 1
    fi
    if curl -sf "$url" >/dev/null 2>&1; then
      printf '   ✅ %s 已就绪（%s）\n' "$name" "$url"
      return 0
    fi
    # 每 5 秒打一条进度，并说明卡在哪一段
    if [ $(( i % 10 )) -eq 0 ]; then
      elapsed=$(( i / 2 ))
      if [ -n "$(dev_port_holders "$port")" ]; then
        note="端口 $port 已绑定，等待 /healthz 应答"
      else
        note="端口 $port 尚未绑定（服务还在启动）"
      fi
      if [ "$note" != "$last_note" ]; then
        printf '   ⏳ %s %ss：%s\n' "$name" "$elapsed" "$note"
        last_note="$note"
      fi
    fi
    sleep 0.5
  done

  echo "✘ $name 在 ${budget}s 内没有就绪" >&2
  if [ -n "$(dev_port_holders "$port")" ]; then
    echo "  卡在：端口已绑定，但 $url 不应答。" >&2
  else
    echo "  卡在：端口 $port 始终没有被绑定。" >&2
  fi
  dev_print_failure "$name"
  return 1
}

# dev_stop_service <name> —— 先 TERM 进程组，2s 后仍活着就 KILL。
dev_stop_service() {
  local name="$1" pid i
  pid="$(dev_registry_field "$name" pid 2>/dev/null || true)"
  [ -n "$pid" ] || return 0
  kill -0 "$pid" 2>/dev/null || return 0

  kill -TERM "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
  for i in $(seq 1 8); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.25
  done
  kill -KILL "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
  return 0
}

dev_stop_all() {
  local name
  for name in $(dev_registry_names); do
    dev_stop_service "$name"
  done
  # 回收干净之后再报一次端口状态：残留占用是下一次启动失败的原因，
  # 这里不确认就等于把问题留给下一次。
  local port holders
  while IFS=$'\t' read -r name _ port _; do
    [ -n "$name" ] || continue
    holders="$(dev_port_holders "$port")"
    if [ -n "$holders" ]; then
      printf '⚠️  %s 停止后端口 %s 仍被占用：\n' "$name" "$port" >&2
      while IFS=$'\t' read -r pid cmd; do
        [ -n "$pid" ] || continue
        printf '      pid %s  %s\n' "$pid" "$cmd" >&2
      done <<< "$holders"
    fi
  done <<< "$DEV_SERVICES"
}

# dev_cleanup —— EXIT / INT / TERM 的收尾。
#
# 只有真的起过服务才报「正在停止」：端口预检失败就退出时，什么都没起，
# 旧写法照样打「🛑 正在停止服务... ✅ 已停止。」——那看起来像停掉了什么，
# 而实际停掉的是零个。这正是本文件要消灭的「看起来做了，其实没做」。
dev_cleanup() {
  if [ -z "$DEV_SERVICES" ]; then
    return 0
  fi
  echo ""
  echo "🛑 正在停止服务..."
  dev_stop_all
  echo "✅ 已停止。"
}

# ---------------------------------------------------------------------------
# 监管
# ---------------------------------------------------------------------------

# dev_supervise —— 阻塞。任一服务退出就报出是哪个、为什么，然后返回 1。
#
# 旧脚本用 `wait "$SERVER_PID" "$GATEWAY_PID"`：任一退出后它只是返回，
# 不说是谁、也不带日志，于是「某个服务掉了」在终端上看起来像脚本自己结束了。
dev_supervise() {
  local name pid port log
  while true; do
    while IFS=$'\t' read -r name pid port log; do
      [ -n "$name" ] || continue
      if ! kill -0 "$pid" 2>/dev/null; then
        echo "" >&2
        echo "✘ ${name} 已退出（pid ${pid}）" >&2
        dev_print_failure "$name"
        return 1
      fi
    done <<< "$DEV_SERVICES"
    sleep 1
  done
}

# 汇总：把「起了哪些、pid 多少、日志在哪」一次打出来。
# 「服务没起来」这件事之所以难查，一半原因是没有一个地方能一眼看到全貌。
dev_print_summary() {
  local name pid port log
  echo ""
  echo "✅ 已就绪的服务："
  while IFS=$'\t' read -r name pid port log; do
    [ -n "$name" ] || continue
    printf '   %-16s pid %-7s 端口 %-5s 日志 %s\n' "$name" "$pid" "$port" "$log"
  done <<< "$DEV_SERVICES"
  echo ""
  echo "   日志目录：$DEV_LOG_DIR"
  echo "   查看状态：./scripts/dev-status.sh"
}

# 一行状态，给 dev-status.sh 用。$1 是服务名，$2 是端口。
dev_status_line() {
  local name="$1" port="$2" holders pid cmd state
  holders="$(dev_port_holders "$port")"
  if [ -z "$holders" ]; then
    printf '❌ %-16s 端口 %-5s 无监听\n' "$name" "$port"
    return 1
  fi
  state="✅"
  if ! curl -sf "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
    state="⚠️ "
  fi
  printf '%s %-16s 端口 %-5s ' "$state" "$name" "$port"
  while IFS=$'\t' read -r pid cmd; do
    [ -n "$pid" ] || continue
    printf 'pid %s  %s  ' "$pid" "$cmd"
  done <<< "$holders"
  printf '\n'
  return 0
}
