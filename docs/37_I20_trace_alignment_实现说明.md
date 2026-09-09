# I20 Item 3：全链路 trace 对齐

**票**：I20 Item 3（`docs/i20-fix-plan.md` §四）。  
**状态**：后端本提交收口。iOS `log_id` 注入 tracker 已在 B15-I3（`SpeechSessionTimingsRecorder.setLogID`），本票不改 iOS。  
**关联**：不是 B15 70s 杀会话；不是 Item 2 fixture 路径。

## 1. 要守住的原理

同一轮说话在三层必须能对上：

| 键 | 谁发 | 谁收 |
|---|---|---|
| `turn_id` | iOS `"turn-N"` | 网关 outbound、`logx` segment、iOS tracker |
| `log_id` | Volc 握手 `X-Tt-Logid` | `ai.turn.end`、duplex segment、iOS tracker |
| `session_id` | ticket / `session.created` | handshake 与 duplex segment |

不允许再发明第二套 id（`volc-turn-N`、`dev-echo-turn`）。

## 2. 根因

`ai.turn.end.log_id` 和 iOS `setLogID` 已经有了。剩下三处对不上：

1. Volc `turnToOutbound` 在客户端没带 `turn_id` 时仍生成 `volc-turn-<seq>`
2. DevEcho 播完 PCM 后 `HandleClientAudio` 把 `ai.turn.end` 写成死值 `dev-echo-turn`，丢掉 speech.end 里的 id
3. `logx.Segment.End` 只认 `err==nil → outcome=ok`。`collectTurn` 的 `partial` 是 nil error，segment 会谎报 ok；也没有 `ts`

## 3. 方案与文件

| 文件 | 职责 |
|---|---|
| `turn_id.go` | `canonicalTurnID`：优先客户端，否则 `turn-N` |
| `provider_volc_duplex.go` | 用 canonical；`SetClientTurnID` 再进 `collectTurn` |
| `provider_dev_echo.go` | `lastTurnID` 贯穿 fixture 耗尽 |
| `pkg/logx/logx.go` | `ts`；显式 `outcome` 覆盖 err 推导 |
| `volc_duplex.go` collectTurn | Begin 带 `turn_id`；End 带 TurnOutcome |

不另做一套 JSON encoder：slog JSON 已经是可解析行；字段名对齐计划里的 SegmentLog。

## 4. 为何不折进已有路径

不把 `volc-turn-*` 当兼容别名继续发：iOS 永远对不上。空 `turn_id` 仍可 omitempty；有 seq 时与 iOS 同命名空间。

`log_id` 不在本票重做帧协议（v2 已有）。

## 5. 影响面

- 状态：无相位变化
- 协议：`ai.turn.end.turn_id` 在缺客户端 id 时从 `volc-turn-N` 改为 `turn-N`；DevEcho 耗尽 fixture 时带上客户端 id
- 音频：无
- 发布：日志字段可多 `ts` / 真实 `outcome=partial|timeout`

## 6. 测试

```bash
go test ./pkg/logx/ ./internal/voicegateway/ ./internal/voicepoc/ -count=1
go test ./...
go build ./...
```
