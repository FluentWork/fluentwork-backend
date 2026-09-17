# `server_ts_ms` 出站戳 + 时钟校准设计

**对应**：进度报告 V1.1 缺口「`server_ts_ms` 时钟漂移」与时延预算。**不是** 网关 B15 timeout。  
**代码**：v2 `ai.text.delta` 在序列化前写入 UTC Unix 毫秒；v1 不加该字段。  
**门禁**：`go test ./internal/voiceproto/... ./internal/voicegateway/...` 与全仓 `./scripts/dev-check.sh`

## 原理与背景

协议已规定只有 `ai.text.delta` 带 `server_ts_ms`（`docs/30_技术方案/51_`）。没有这个戳，iOS tracker 和 `logx.Segment` 对出来的 p95 可能是假的。校准不能假设手机 NTP 已对齐。

## 方案

### 出站戳（本票已做）

生产路径（mock / DevEcho / Volc `turnToOutbound`）用 `voiceproto.NewAITextDelta`。`server_ts_ms` 取 **gateway 写入 WSS 之前** 的 UTC Unix 毫秒（Volc 可用测试用 `nowFn`）。字段缺失时 iOS 必须仍能解文本。

v1 schema 不加此字段。v2 已有 optional `server_ts_ms`。

### 校准（本票只定设计，iOS 消费面另做）

不在 `pong` 上加字段（v2 `additionalProperties: false`，冻结期不改握手形状）。用已有应用层 `ping.ts` 回声：

1. iOS 发 `ping.ts = client_unix_ms`
2. 网关原样 `pong.ts`
3. iOS 记 `t_recv`，`rtt = t_recv - ping.ts`
4. 之后每条带 `server_ts_ms` 的 delta：`offset_ms ≈ (t_recv_delta - server_ts_ms) - rtt/2`
5. 用最近一次成功 ping 的 offset；RTT 失败则不作对齐，日志标 `clock_unaligned`

不假设 NTP。offset 是「这一跳 WSS」的估计，不是全球绝对时。

### DevEcho 口径（本票）

`TestDevEchoFixture_LoopbackLatencyUnderBudget` 测本机 httptest：`user.speech.end` → 首个 binary → `ai.turn.end`。本机一次样本：首个 binary **176µs**，`ai.turn.end` **178µs**（20ms fixture）。这是 **loopback 下限**，不是火山，不是真机。超过 2s / 3s 当回归失败。真机 p50/p95 仍空，等 iOS 联调。

## 缺口根因

schema 有字段，出站仍是手写 map，从未填。校准被当成「协议拍板即完成」。

## 新方案理由

- 不把 `server_ts_ms` 加到 TTS/binary：51_ 已否，用 `turn_id`+`seq`。
- 不改 `pong`：避免 v2 握手变更。
- 不在本仓改 iOS tracker。

## 明确不做

- 不填真机 p95
- 不把 ping 改成带 `server_ts_ms` 的新合同
- 不把 Volc 供应商时间当权威（权威是网关序列化瞬间）

## 测试

```bash
go test ./internal/voiceproto/ -run 'AITextDelta|ServerTsMs'
go test ./internal/voicegateway/ -run 'ServerTsMs|LoopbackLatency|VoiceHandshakeAndSessionLoop'
go test ./...
go build ./...
```
