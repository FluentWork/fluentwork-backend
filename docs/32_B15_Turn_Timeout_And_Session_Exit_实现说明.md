# B15 Turn Timeout & Session Exit 实现说明

**对应**：网关 B15（`docs/23` / I20 Item 1）。**不是** GitHub #28 / `docs/31` 那张离线评估集 B15。  
**代码**：`voiceproto.AITurnEnd.Outcome`、`handler.go` `logWarn` + 写失败立即退 session、`provider_volc_duplex.turnToOutbound`、#43 reopen-once（此前已合入 `main`）  
**本提交**：WSS schema 补 `outcome`/`log_id`；补 `ai.turn.end` 上线证明与 `logWarn` 11→2 单测  
**门禁**：`go test ./internal/voicegateway/... ./internal/voiceproto/... ./internal/voicepoc/...` 与全仓 `./scripts/dev-check.sh`

## 原理与背景

iOS 语音会话有三条会「假活着」的路径：

1. Turn 等不到 `ai.turn.end` 时，客户端只能靠 70s 计时器猜是超时还是正常结束。
2. 火山上行写失败后，网关把 `rt.broken` 置位并丢掉后续 PCM，但 loop 不退，iOS 要等到 2 分钟 WSS idle。
3. 同一失败在超时循环里打出 80+ 行相同 WARN，把根因淹掉。

#43（idle 后 reopen-once + keepalive probe）已经关单，本票不再重做。

## 方案

| 项 | 做法 |
| --- | --- |
| Item 1 outcome | `collectTurn` 每个出口打 `ok` / `partial` / `timeout` / `error`。Volc `turnToOutbound` 原样写入 `ai.turn.end`；DevEcho 干净结束用 `ok`。有 Outcome 就先发 `ai.turn.end` 让 iOS 离开 `.processing`，session 只在 Outcome 未设置的传输错误上 abort。 |
| Item 2 写失败退出 | `handleAudio` 失败：reopen 一次 → 仍失败则 `rt.broken`、`error` 帧 `provider_audio_failed`、**返回非 nil**，loop 立刻退出。 |
| Item 3 Warn 去重 | `logWarn`：同一 key 在 5s 窗口内只打第 1 条完整 WARN，每 10 次一条 `(deduplicated)` 汇总。只覆盖音频转发这种会刷屏的路径。 |
| 契约 | 只改 **v2** `aiTurnEnd` 的 `outcome`、`log_id`。v1 是冻结的 V1.0 快照，保持 `type`+`turn_id`。线上 speaking room 走 V2；handler 用 `json.Marshal` 写帧，不按 JSON Schema 校验。 |

Turn 级一次性 WARN（例如 `turn result timeout, retrying`）仍走 `logger.Warn`：它们不是 80 行 cascade，也不在 Handler 的 `logWarn` 作用域里。

## 缺口根因

Outcome 在 `TurnResult` 和 Go 结构体上早就有了，但：

- JSON schema 没跟上，iOS / 校验器仍认为 `ai.turn.end` 只有 `type` + `turn_id`。
- 回归停在 `collectTurn` 的内部字段，没有证明 handler 把 `outcome=timeout` 写到 WSS。
- `docs/i20-fix-plan.md` §2.1 仍写「帧上没有 outcome」，容易让下一轮把已做完的后端再做一遍。

写失败退出和 Warn 去重此前已有 `TestHandler_AudioMarksSessionBrokenAfterFirstFailure`；缺的是 schema 与「11 次 logWarn → 2 条日志」的直接单测。

## 新方案理由

- **先发 `ai.turn.end` 再考虑重试**：超时窗口耗尽时 Outcome 一定已设置。再等 20s 会让 iOS 继续卡在 `.processing`；未分类的 `DeadlineExceeded` 才走重试。
- **error 帧 + return err，而不是只设 `rt.broken`**：静默丢 PCM 解决日志风暴，解决不了「连接还活着」。iOS 必须在约 1s 内看到 `provider_audio_failed`。
- **schema 与结构体一起改**：`additionalProperties: false` 下漏字段等于协议未发布。

## 明确不做 / 留给 iOS

- iOS 解码 `outcome=timeout` → `.failed("turn_timeout")` 已在 `fluentwork-ios` 收口；本仓不改 iOS。
- 不把 provider 一次性 WARN 全部改成 `logWarn`。
- 不重开 #43 keepalive / reopen-once。
