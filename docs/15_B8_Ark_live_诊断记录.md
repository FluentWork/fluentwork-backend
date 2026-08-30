# B8 Ark live 诊断记录

## 结论

当前 `B8` live 推进的关键证据与根因如下：

1. **Ark review/refine endpoint 可达**
   - 最小 `PONG`（curl / Go）可 `HTTP 200`
   - Key / endpoint / 代理链路本身可用

2. **P1 根因：review endpoint 绑定了会返回 `reasoning_content` 的深度思考模型**
   - 未关闭 thinking 时，带 `response_format=json_object` 的真实 review/refine 请求经常 **45–120s 仍 0 字节**（`awaiting headers` / curl 28）
   - 不是 Go 传输层坏了：同一进程对同一 endpoint 发 `PONG` 约 2–3s 可通
   - **修复**：请求体加 `"thinking":{"type":"disabled"}` 后，B15 形请求约 **8–9s** 返回，且 `reasoning_tokens=0`

3. **P2：schema 不符合 B15**
   - 早期 live / 精简 curl prompt 会出现自由字段或非法枚举（如 `contraction` / `function_tag=plan`）
   - **权威门禁是 Go smoke**：完整 `systemPrompt()` + `eval.ValidateSample`
   - 2026-08-30 复测：`./scripts/smoke-review-ark.sh` → **PASS**（`validated_b15=true`，约 7.3s，`thinking.disabled`）
   - curl smoke 主要用于连通与时延；枚举约束已加严，但仍以 Go 校验为准

## 已补入口

### 1. Go 版 live smoke

- `cmd/smoke-review-ark`
- `scripts/smoke-review-ark.sh`

### 2. curl 版 live smoke

- `scripts/smoke-review-ark-curl.sh`（已加 `thinking.disabled` + wall 计时）

## 复现实验（2026-08-30）

| 用例 | 结果 |
|---|---|
| `PONG` @ `fw-review-refine` | ~2.9s，`HTTP 200`，带 `reasoning_content` |
| tiny `json_object`（thinking 默认） | 曾 45s 超时 / 也曾短时成功（不稳定） |
| B15 短 prompt（thinking 默认） | 90s 超时，0 字节 |
| tiny `json_object` + `thinking.disabled` | ~1.9s，`reasoning_tokens=0` |
| B15 + `json_object` + `thinking.disabled` | ~8.5s，`HTTP 200`，B15 结构 PASS |

## 工程修复

`internal/reviewgen/reviewgen.go`：

- chat completions 请求增加 `Thinking: {"type":"disabled"}`
- `max_tokens` 提到 `800`（完整 comparisons/blocks 更稳）

单测：`TestArkGeneratorGenerateDisablesThinking` 断言请求体含 thinking disabled。

## 当前判断

| 说法 | 是否成立 |
|---|---|
| endpoint 不可用 | **否** |
| Go 代理彻底坏了 | **否**（最小请求可通） |
| live review 过慢主因是 thinking | **是** |
| prompt/schema 已可过 B15 | **是（Go smoke）** — `validated_b15=true` |
| worker 可切 live | **可以进入接线** — live generator 已绿；仍建议先观察 cost/时延再全量切换 |

## 推荐下一刀

1. `zsh -ic 'proxy_on && ./scripts/smoke-review-ark-curl.sh'`
2. `zsh -ic 'proxy_on && ./scripts/smoke-review-ark.sh'`（必须 `validated_b15=true`）
3. 通过后再把 worker 从 stub 切到 `ArkGenerator`
4. 若仍偶发 >15s：考虑把 review 接到非 thinking 的 endpoint，或拆 refine 二次调用
