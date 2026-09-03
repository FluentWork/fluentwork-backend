# FluentWork B15 Issue 文档

> Issue 编号：B15  
> 创建日期：2026-09-03  
> 状态：实施中  
> 优先级：P0

## 问题背景

B15 是 FluentWork 语音网关的第二阶段问题，主要涉及：

1. **Turn 超时显式 `outcome` 字段** — iOS 无法区分正常完成和超时
2. **后端写失败立即退 session** — iOS 需等待 2 分钟才能感知连接断开
3. **Warn 去重** — 防止 80+ 重复警告日志淹没根因

## Item 1: Turn 超时显式 outcome

### 问题
`ai.turn.end` 帧没有携带明确的终端状态，iOS 只能通过隐式计时器感知超时。

### 实现方案

**后端改动（`voiceproto/frames.go`）：**

```go
type AITurnEnd struct {
    Type    string `json:"type"`
    TurnID  string `json:"turn_id,omitempty"`
    Outcome string `json:"outcome,omitempty"` // "" | "ok" | "partial" | "timeout" | "error"
}
```

**Provider 实现：**
- `provider_volc_duplex.go` — `turnToOutbound` 传递 `turn.Outcome`
- `provider_dev_echo.go` — `outcome="ok"` 表示正常结束

**iOS 改动（`WSControlFrame.swift`）：**
- 解码带 `outcome` 的 `ai.turn.end` 帧
- `outcome == "timeout"` 时触发 `.failed("turn_timeout")`

### 验收标准
- [x] `voiceproto.AITurnEnd` 已包含 `Outcome` 字段
- [x] DevEchoProvider 设置 `Outcome: "ok"`
- [x] VolcDuplexProvider 传递 `turn.Outcome`
- [ ] iOS 端解码并处理 `outcome=timeout`

---

## Item 2: 后端写失败立即退 session

### 问题
`handleAudio` 中音频帧写入失败后，`rt.broken = true` 静默丢弃后续帧，但 session 不退出，iOS 需等待 2 分钟 idle timeout。

### 实现方案（`handler.go`）

```go
func (h *Handler) handleAudio(...) error {
    // ... provider 调用失败后
    rt.broken = true
    h.logWarn(rt, "provider_audio_failed", ...)
    
    // Item 1.2: 发送错误帧后返回错误，让 loop 立即退出
    _ = writeJSON(ctx, conn, voiceproto.ErrorFrame{
        Type:    voiceproto.TypeError,
        Code:    "provider_audio_failed",
        Message: err.Error(),
    })
    return fmt.Errorf("provider audio forward failed: %w", err)  // 关键：返回非 nil error
}
```

### 验收标准
- [x] Provider 写入失败后立即发送 `error` 帧
- [x] Handler 返回错误使 session loop 退出
- [x] iOS 收到 `provider_audio_failed` 错误码
- [x] 测试 `TestHandler_AudioMarksSessionBrokenAfterFirstFailure` 通过

---

## Item 3: Warn 去重

### 问题
Provider 音频转发失败时，产生 80+ 重复 `WARN` 日志，淹没根因。

### 实现方案（`handler.go`）

```go
const warnDedupInterval = 10

func (h *Handler) logWarn(rt *sessionRuntime, key, msg string, args ...any) {
    now := h.now()
    window := 5 * time.Second
    
    if rt.warnDedup.key == key && now.Sub(rt.warnDedup.lastAt) < window {
        rt.warnDedup.count++
        rt.warnDedup.lastAt = now
        if rt.warnDedup.count%interval == 0 {
            // 每 10 次输出一次汇总
            summaryArgs := append([]any{"dedup_count", rt.warnDedup.count, "first_key", key}, args...)
            h.logger.Warn(msg+" (deduplicated)", summaryArgs...)
        }
        return
    }
    // 新 key 或超出窗口：输出完整行
    rt.warnDedup.key = key
    rt.warnDedup.lastAt = now
    rt.warnDedup.count = 1
    h.logger.Warn(msg, args...)
}
```

### 验收标准
- [x] 相同 key 在 5s 窗口内只输出 1 次完整 WARN
- [x] 每 10 次重复输出一次汇总（含计数）
- [x] 测试验证 `warnCount("provider audio forward failed") == 1`

---

## 文件索引

| 文件 | 改动内容 |
|------|----------|
| `internal/voiceproto/frames.go` | `AITurnEnd` 增加 `Outcome` 字段 |
| `internal/voicegateway/handler.go` | Item 1.2: 返回错误退出 session；Item 1.3: `logWarn` 去重 |
| `internal/voicegateway/provider_dev_echo.go` | `outcome="ok"` 设置 |
| `internal/voicegateway/provider_volc_duplex.go` | 传递 `turn.Outcome` |
| `internal/voicegateway/handler_b15_test.go` | B15 回归测试 |

## 相关文档

- [I20 收口计划](../docs/i20-fix-plan.md) — 完整的 B15 及全链路架构文档
- [B12 Badge Emit 修复说明](./20_B12_badge_emit_问题修复说明.md)
- [B14 Client ASR Relay 架构](./22_B13_client_asr_回灌_实现说明.md)
