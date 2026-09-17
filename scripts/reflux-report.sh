#!/usr/bin/env bash
# 月度回流报表：把四个"数据 → 产品决策"的信号收进一页（86_ M12）。
#
# 用法：
#   ./scripts/reflux-report.sh --user <user_id> --token <access_token>
#   ./scripts/reflux-report.sh --user <user_id> --token <token> --base-url http://host:8080
#
# 输出：eval-out/reflux-<date>.md（stdout 同时打印）
#
# 四个信号：
#   1. 卡壳视图（按 function_tag）→ 该补什么内容、哪类意图系统性难
#   2. 实战转化率分来源（hit / checkin）→ 实战出口往哪投入
#   3. "没聊成"原因分布 → no_partner / not_confident / no_time / not_relevant
#   4. 最近一次质量门禁与基线的差异 → 改动是否退化
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE_URL="${APP_BASE_URL:-http://127.0.0.1:8080}"
USER_ID=""
TOKEN=""
INTERNAL_TOKEN="${INTERNAL_API_TOKEN:-fluentwork-dev-internal-token-change-me!!}"
DAYS="${DAYS:-30}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --user) USER_ID="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --base-url) BASE_URL="$2"; shift 2 ;;
    --internal-token) INTERNAL_TOKEN="$2"; shift 2 ;;
    --days) DAYS="$2"; shift 2 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

if [[ -z "$USER_ID" ]]; then
  echo "reflux-report: --user <user_id> is required (whose month are we reviewing)" >&2
  exit 2
fi

OUT_DIR="eval-out"
mkdir -p "$OUT_DIR"
OUT="$OUT_DIR/reflux-$(date -u +%Y-%m-%d).md"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fetch() { # fetch <out-file> <url> [auth-header]
  local out="$1" url="$2" auth="${3:-}"
  if [[ -n "$auth" ]]; then
    curl -sS -m 10 -H "$auth" "$url" -o "$out" 2>/dev/null || echo '{}' >"$out"
  else
    curl -sS -m 10 "$url" -o "$out" 2>/dev/null || echo '{}' >"$out"
  fi
}

fetch "$TMP/stuck.json" "${BASE_URL%/}/internal/v1/drill/stuck-map?days=${DAYS}&user_id=${USER_ID}" "X-Internal-Token: ${INTERNAL_TOKEN}"
if [[ -n "$TOKEN" ]]; then
  fetch "$TMP/stats.json" "${BASE_URL%/}/api/v1/topic-cards/stats?days=${DAYS}" "Authorization: Bearer ${TOKEN}"
else
  echo '{"_note":"no --token given; user-scoped stats skipped"}' >"$TMP/stats.json"
fi
fetch "$TMP/metrics.txt" "${BASE_URL%/}/metrics"

LATEST_DIFF="$(ls -t eval-out/*/baseline-diff.md 2>/dev/null | head -1 || true)"

python3 - "$TMP" "$OUT" "$USER_ID" "$DAYS" "$LATEST_DIFF" <<'PY'
import json, pathlib, sys, datetime

tmp, out_path, user_id, days, latest_diff = sys.argv[1:6]
tmp = pathlib.Path(tmp)

def load(name):
    p = tmp / name
    try:
        return json.loads(p.read_text())
    except Exception:
        return {}

stuck = load("stuck.json")
stats = load("stats.json")
metrics = (tmp / "metrics.txt").read_text() if (tmp / "metrics.txt").exists() else ""

lines = []
lines.append(f"# 月度回流报表 — {user_id}  ({datetime.date.today().isoformat()})")
lines.append("")
lines.append(f"窗口：最近 {days} 天。四个信号 + 一张待办清单（86_ M12）。")
lines.append("")

lines.append("## 1. 卡壳视图（按 function_tag）")
rows = stuck.get("rows") or []
if rows:
    lines.append("")
    lines.append("| 意图类型 | 块 | 判定 | 失败 | 练过又忘 | 转正 | 平均判定/转正 | 最近失败 |")
    lines.append("|---|---|---|---|---|---|---|---|")
    for r in rows:
        lines.append("| {function_tag} | {blocks} | {attempts} | {failures} | {forgot} | {promotions} | {avg} | {last} |".format(
            function_tag=r.get("function_tag",""), blocks=r.get("blocks",0), attempts=r.get("attempts",0),
            failures=r.get("failures",0), forgot=r.get("forgot_after_learning",0),
            promotions=r.get("promotions",0), avg=r.get("avg_attempts_per_promotion",0),
            last=(r.get("last_failure_at","") or "")[:10]))
    lines.append("")
    lines.append("→ **待办**：失败最多、或“练过又忘”占比最高的意图类型，优先补素材/调难度。")
    rescues = stuck.get("rescues") or {}
    if rescues:
        lines.append(f"救援：{', '.join(f'{k}={v}' for k, v in sorted(rescues.items()))}（silent 占比高说明用户卡在“开不了口”，不是“说不对”）。")
else:
    lines.append("")
    lines.append("（窗口内没有判定记录）")

lines.append("")
lines.append("## 2. 实战转化率（分来源）")
if stats and not stats.get("_note"):
    lines.append("")
    lines.append(f"- 打卡：{stats.get('checkins',0)} 次 / 发出卡片 {stats.get('cards_served',0)} 张（打卡率 {stats.get('checkin_rate',0):.0%}）")
    lines.append(f"- 实战使用：服务端观测（hit）**{stats.get('real_uses_hit',0)}** 次，用户确认（checkin）**{stats.get('real_uses_checkin',0)}** 次")
    lines.append(f"- 绿灯块 {stats.get('green_blocks',0)} 个，其中用过 {stats.get('green_used',0)} 个（转化率 {stats.get('conversion_rate',0):.0%}）")
    lines.append("")
    lines.append("→ **待办**：若 hit 远高于 checkin，说明用户在 App 内用得多但没去实战；若都低，先看下面的原因分布。")
else:
    lines.append("")
    lines.append("（未提供用户 token，跳过）")

lines.append("")
lines.append("## 3. “没聊成”原因分布")
reasons = (stats or {}).get("dismiss_reasons") or {}
if reasons:
    total = sum(reasons.values())
    for reason, count in sorted(reasons.items(), key=lambda kv: -kv[1]):
        lines.append(f"- `{reason}`：{count}（{count/total:.0%}）")
    lines.append("")
    lines.append("→ **待办**：no_partner→系统内模拟对话；not_confident→更低的第一步；no_time→卡片更短；not_relevant→话题生成仍需调。")
else:
    lines.append("")
    lines.append("（窗口内没有“没聊成”记录）")

lines.append("")
lines.append("## 4. 质量门禁（最近一次对比）")
if latest_diff:
    diff = pathlib.Path(latest_diff).read_text()
    regressions = [l for l in diff.splitlines() if "退化" in l]
    if regressions:
        lines.append("")
        lines.append("**有指标退化：**")
        lines += regressions
    else:
        lines.append("")
        lines.append("无退化（明细见 " + latest_diff + "）")
else:
    lines.append("")
    lines.append("（还没跑过 `./scripts/eval-gate.sh`）")

feedback = {}
for line in metrics.splitlines():
    if line.startswith("corpus_block_feedback_total{"):
        try:
            reason = line.split('reason="')[1].split('"')[0]
            value = int(line.rsplit(" ", 1)[1])
            if reason == "none":
                continue  # the metric's own placeholder for "nothing recorded"
            feedback[reason] = value
        except Exception:
            pass
lines.append("")
lines.append("## 5. 炼化质量负反馈（累计）")
if feedback:
    for reason, value in sorted(feedback.items(), key=lambda kv: -kv[1]):
        lines.append(f"- `{reason}`：{value}")
    lines.append("")
    lines.append("→ **待办**：这个比率不降，说明改写规则没生效（86_ M7）。")
else:
    lines.append("")
    lines.append("（没有负反馈记录，或 /metrics 不可达）")

lines.append("")
lines.append("---")
lines.append("")
lines.append("## 本月要做的事")
lines.append("")
lines.append("1. 从 §1 挑一个意图类型，补素材或调难度")
lines.append("2. 从 §3 挑占比最高的原因，做一件对应的产品动作")
lines.append("3. 每做完一件，**在 `eval/moat/revisions.md` 记一条前后指标**（这是纪律，不是形式）")

text = "\n".join(lines) + "\n"
pathlib.Path(out_path).write_text(text)
print(text)
PY

echo "reflux-report: 写入 $OUT"
