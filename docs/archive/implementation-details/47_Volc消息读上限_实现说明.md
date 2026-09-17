# Volc duplex 消息读上限

**对应**：真机会话 `5d500ec0`（2026-09-10 22:36–22:38）。**这就是「每轮失忆」的根因。**  
**代码**：`internal/voicepoc/volc_duplex.go`（`duplexReadLimit` + `conn.SetReadLimit`）  
**门禁**：`./scripts/dev-check.sh` → All checks passed；`go test ./...` 全绿

## 原理与背景

**duplex 连接的读上限必须容得下供应商自己的音频帧。**

`coder/websocket` 默认每帧最多读 **32 KiB**。Volc 把助手的语音以 **base64 编码的 `response.output_audio.delta`** 事件推下来 —— 一条就超过这个数。读一超限,连接当场作废。

真机证据（会话 `5d500ec0`,7 轮里 6 轮的 `err`）：

```
duplex connection closed: failed to read:
  websocket: message too big: read limited at 32769 bytes
```

| 轮次 | 时刻 | outcome | 是否重置 |
|---|---|---|---|
| turn-1 | 22:36:24 | error（超限） | ✔ |
| turn-2 | 22:36:45 | error（超限） | ✔ |
| turn-3 | 22:37:12 | error（超限） | ✔ |
| turn-4 | 22:37:28 | error（超限） | ✔ |
| turn-5 | 22:37:44 | **ok** | —— |
| turn-6 | 22:38:11 | error（超限） | ✔ |
| turn-7 | 22:38:38 | error（超限） | ✔ |

turn-5 是唯一没超限的一轮,也是唯一正常结束的一轮。**回复越长越容易挂** —— 这正是音频帧大小的分布。

### 为什么一直没被发现

同一个错误在两份更早的真机日志里**都在**,只是被 `partial` 掩盖了：旧代码把「读失败但捞到了内容」标成 `partial` 并返回 `nil` error,错误文本从来没被看到。`docs/43` 把读失败改成 `outcome=error` + 非 nil error 之后,真实错误才第一次显形。

**「每轮失忆」是两个 bug 叠加的结果**：

1. 本 bug —— 连接每轮死一次
2. `docs/45` 的 `resetAfterTurnReadFailure` —— 每次死亡都开一条**新的 Volc 会话**,服务端保存的对话历史随之清空

第 2 条是对第 1 条的正确反应（连接真死了,不重置下次写就必挂）,但它把「连接不稳定」放大成了「每轮失忆」。修掉第 1 条之后,重置不再触发。

## 方案

`OpenDuplex` 拨通后立刻抬高读上限：

```go
conn.SetReadLimit(duplexReadLimit)   // 4 MiB
```

选 4 MiB 的理由：默认值 32 KiB 连一条音频 delta 都装不下；4 MiB 是默认值的 128 倍,远超任何合理的单帧,同时仍然给内存一个上界。全仓在此之前**没有任何 `SetReadLimit` 调用**。

## 影响面

- **协议**：零改动。
- **连接寿命**：长回复不再打断 duplex,会话得以保留 Volc 侧上下文 —— 这是本票的主要收益。
- **内存**：单帧上限从 32 KiB 放宽到 4 MiB。极端情况下一个恶意/异常的对端可以多占几 MiB,仍是有界的。
- **观测**：`duplex reset after turn read failure` 应停止出现。若仍出现,说明还有别的断连原因,需要另查。

## 明确不做

- 不解析 / 不转发 `response.output_audio.delta`（那是 I15 的范围,客户端目前也不消费它）
- 不动 `resetAfterTurnReadFailure`：连接真死时它仍是正确反应
- 不改 `sendSilence` —— 之前怀疑它与音频输出相互干扰,现在看不是主因

## 测试

先红后绿。

| 测试 | 守住的不变量 | 修复前 |
|---|---|---|
| `TestCollectTurn_ReadsAudioDeltaFramesPastTheDefaultReadLimit` | 96 KiB 的 `response.output_audio.delta` 不打断该轮,turn 正常收 `ok` | 复现出与生产**一字不差**的错误：`message too big: read limited at 32769 bytes` |

```bash
go test ./internal/voicepoc/ -run "TestCollectTurn_" -v
./scripts/dev-check.sh
```

## 后续

**badge 不命中是另一回事,与本票无关。** 用户当时练的是 clean architecture / TCA 话题,而 dev 语料是 10 条 standup 短语（`Let's ship it.` 等）,没有可匹配项。要复现 badge,需要用命中语料的句子,或扩充 dev 语料覆盖面。
