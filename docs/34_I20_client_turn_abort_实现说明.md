# I20 `client.turn.abort` 网关接收

**对应**：iOS T-I20-1（录音 60s abort）。**不是** B15 的 `ai.turn.end outcome=timeout` / 70s 会话失败。  
**代码**：`voiceproto.ClientTurnAbort`、`handler.go` 接受该帧、Volc 清 `turnStarted` 且不 `CommitAudio`（`5c2e39f`）  
**门禁**：`go test ./internal/voicegateway/... ./internal/voiceproto/...` 与全仓 `./scripts/dev-check.sh`

## 原理与背景

用户一直在说、VAD 未结束时，iOS 必须主动关掉这一轮 user speech，否则 PCM 无限前向。这条路径发 `client.turn.abort`，**会话继续**，下一轮 `user.speech.start` 再开口。

B15 是另一条：已经发了 `user.speech.end`，等不到 `ai.turn.end`，才结束整场会话。

iOS 已上线 abort。网关 `default` 分支对未知 `type` 回 `error.code=unsupported_frame`；iOS 把 error 帧映射成 `.failed`，真机录满 60s 会把还活着的会话打死。

## 方案

收到合法 `{"type":"client.turn.abort","turn_id":"turn-N","outcome":"timeout"|"user_abandoned"|"error"}` 时：

1. 记下日志，**不**回 `unsupported_frame`
2. 不调用 `collectTurn` / `WaitTurnResult`，不发 `ai.turn.end`
3. 不关 WSS，不当 `session.end`
4. Volc 清掉本轮 `turnStarted` / `activeTurnID`，避免之后误 `CommitAudio`
5. 连接上还没有 `session.start`（hold 未开口）→ 静默 no-op
6. `outcome=ok` 或缺失 → `invalid_frame`，且不转给 provider

WSS **v2** schema 增加 `$defs.clientTurnAbort`（与 iOS 镜像一致）。**v1 冻结**，不加这帧。真源已写入 `fluentwork-infra` `d60d0fe`（见 `docs/35_I20_契约真源同步_实现说明.md`）。

Provider 转发失败只打 WARN，不发 error 帧，避免 abort 自己把会话打死。

## 缺口根因

网关只认识 `user.speech.*` / `interrupt` / `session.end`。iOS 先发了新帧，backend 把「未知类型」当成致命错误。根因是把录音 abort 和 B15 处理超时收成同一种失败。

## 新方案理由

- **不复用 `interrupt`**：interrupt 是 barge-in（AI 在说，用户开口），abort 是用户还没说完。
- **不发 `ai.turn.end outcome=timeout`**：那会走 B15 `.failed("turn_timeout")`，正好是 abort 要避免的。
- **outcome 拒绝 `ok`**：abort 表示开口没正常结束；干净结束只走 `user.speech.end`。

## 明确不做

- 不在本仓改 iOS（T-I20-1..4 已落地）
- 不把 abort 后的等待态改成 I21（iOS 已做）
- 不调用火山「清空缓冲区」API（没有稳定接口）；只保证本进程不再 collectTurn
- ~~不同步 `fluentwork-infra` schema 真源~~ — 已由 `d60d0fe` 完成

## 后续

未知 `type` 的默认策略已改为忽略 + 计数（`docs/38`），不再发 `unsupported_frame`。abort 白名单仍然需要：合法 abort 还要清 `turnStarted`、不能 `collectTurn`。
