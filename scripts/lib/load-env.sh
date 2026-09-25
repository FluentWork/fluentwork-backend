#!/usr/bin/env bash
#
# dotenv 加载器 —— 唯一实现点。
#
# 真源：docs/106_环境加载器统一_实现说明.md（背景见 docs/105_ 的 F1/F2/F3）
#
# 这份语义曾经有两份 shell 实现（dev-up.sh 的 load_env_file 与
# dev-local-start.sh 的内联 IFS='=' read 循环），并且已经分歧到需要在
# .env.volc.local 的文件头写一段话让使用者规避。同一个语言的同一份语义
# 没有理由有两份，所以收在这里。
#
# 用法：
#   source "$ROOT/scripts/lib/load-env.sh"
#   load_env_snapshot                 # 必须在加载任何文件之前调用一次
#   load_env_file "$ROOT/.env"        # 可多次；后加载的覆盖先加载的
#
# 优先级：真实环境变量 > 后加载的文件 > 先加载的文件。
#
# 这正是两个调用方注释里各自声称的语义（dev-up.sh「Shell / --host / CI
# exports win」、dev-local-start.sh「Values here take precedence」）。
# 此前两份实现各实现了其中一半：dev-up.sh 做到了「shell 胜」但后加载的文件
# 压不过先加载的；dev-local-start.sh 做到了「后加载的胜」但压掉了 shell。
#
# 只使用 bash 3.2 语法：macOS 自带的 /bin/bash 是 3.2.57，而两个调用方的
# shebang 都是 `#!/usr/bin/env bash`，在这台机器上解析到的就是它。
# 因此本文件不得使用 declare -A（关联数组，bash 4+）。

# 脚本启动时就已经存在的变量名，以空格包围的形式存成一条字符串。
# 用字符串而不是关联数组，是为了 bash 3.2 兼容。
LOAD_ENV_PRESET=""

load_env_snapshot() {
  LOAD_ENV_PRESET=" $(compgen -e | tr '\n' ' ') "
}

_load_env_is_preset() {
  case "$LOAD_ENV_PRESET" in
    *" $1 "*) return 0 ;;
  esac
  return 1
}

# load_env_file <path>
# 文件不存在返回 1；调用方自己决定那是不是错误。
load_env_file() {
  local file="$1"
  if [[ -z "$file" || ! -f "$file" ]]; then
    return 1
  fi

  local line key value n=0
  while IFS= read -r line || [[ -n "$line" ]]; do
    n=$((n + 1))

    line="${line#"${line%%[![:space:]]*}"}"
    case "$line" in
      ''|\#*) continue ;;
    esac

    # 没有 `=` 的行不是赋值。以前 `key="${line%%=*}"` 与 `value="${line#*=}"`
    # 在无 `=` 时都返回整行，于是一行裸词 `BOGUS` 会被导出成 `BOGUS=BOGUS`。
    if [[ "$line" != *=* ]]; then
      printf 'load-env: %s:%d: 跳过没有 = 的行: %s\n' "$file" "$n" "$line" >&2
      continue
    fi

    key="${line%%=*}"
    value="${line#*=}"

    key="${key%"${key##*[![:space:]]}"}"
    key="${key#"${key%%[![:space:]]*}"}"

    # 键必须是合法标识符。畸形行以前会让 dev-local-start.sh 在
    # `export "  # 说明="` 上被 set -e 直接干掉；现在报一行警告并跳过。
    if [[ ! "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      printf 'load-env: %s:%d: 跳过无法解析的行: %s\n' "$file" "$n" "$line" >&2
      continue
    fi

    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"

    # 引号内的值原样保留（`KEY="a # b"` 的 `#` 不是注释）；
    # 未加引号时才剥掉 ` # comment`。剥完再 trim 一次，因为注释前
    # 可能留着空白。
    case "$value" in
      \"*)
        value="${value#\"}"
        value="${value%%\"*}"
        ;;
      \'*)
        value="${value#\'}"
        value="${value%%\'*}"
        ;;
      *)
        value="${value%%[[:space:]]#*}"
        value="${value%"${value##*[![:space:]]}"}"
        ;;
    esac

    _load_env_is_preset "$key" && continue
    export "$key=$value"
  done < "$file"
  return 0
}
