# Abort 后重建 Volc Duplex（无清缓冲 API）

**对应**：进度报告 V1.1 缺口「火山清空缓冲区」。**不是** 网关 B15 timeout，也不是 B15 #28。  
**代码**：`volcDuplexProviderSession` 在 `client.turn.abort` 时 `resetDuplex`（先 `OpenDuplex` 再关旧连接）。  
**门禁**：`go test ./internal/voicegateway/... ./internal/voicepoc/...` 与全仓 `./scripts/dev-check.sh`

## 原理与背景

abort 只清本进程 `turnStarted`，不 `CommitAudio`。PCM 已经 `input_audio_buffer.append` 到火山。若供应商连接跨 turn 保缓冲，下一轮 ASR 可能吃到上一轮尾巴。这会直接打穿 turn 边界。

## 查过的 API

火山 Seeduplex / Realtime Speech 3.0 双工客户端事件（[docs 6561/2549778](https://www.volcengine.com/docs/6561/2549778)，SDK 对照 [realtime_duplex.md](https://github.com/GizClaw/doubao-speech-go/blob/main/docs/realtime_duplex.md)）只列出：

- `input_audio_buffer.append`
- `input_audio_buffer.commit`
- `response.cancel`（取消**出站**回复，不是清输入缓冲）
- `session.close`

**没有** `input_audio_buffer.clear`。不发未文档化事件：失败会变成 error 事件，可能把整场 duplex 打挂。

## 方案

abort 时：

1. 仍清 `turnStarted` / `activeTurnID`，不 `collectTurn`，不发 `ai.turn.end`
2. **先开新 duplex，再关旧连接**（开失败则保留旧连接，避免 WSS 侧 session 变 nil）
3. 开新连接失败：WARN，**不**把 error 回给 iOS（abort 必须保活）
4. `Start` 把 scene/material instructions 写进 `s.cfg`，重建时带上同一份 prompt

超时：开新连接 8s，关旧连接 3s。

## 缺口根因

把「本进程不再 CommitAudio」当成「供应商缓冲也空了」。火山没有清缓冲事件，隔离只能换一条 duplex 会话。

## 新方案理由

- 不 `CommitAudio`：那会把 abort 的半轮送进 ASR，正是要避免的。
- 不发未文档 `clear`：没有稳定合同。
- 不开失败就杀 iOS 会话：与 abort 保活同一条红线。
- 不在 abort 时 `response.cancel`：abort 发生在 `speech.end` 之前，通常没有出站 TTS。

## 明确不做

- 不在本仓改 iOS
- 不向火山提工单（文档已足够否定 clear；工单不能当门禁）
- 不把 handler 层 #43 reopen-once 的空 `session.start` 指令丢失一并修掉（那是另一张票）

## 测试

```bash
go test ./internal/voicegateway/ -run 'VolcDuplexAbort'
go test ./...
go build ./...
```
