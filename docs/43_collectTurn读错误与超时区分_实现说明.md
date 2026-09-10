# `collectTurn` 区分读错误与等待窗口超时

**对应**：真机火山日志回溯（session `cbfa2d23`，2026-09-10）。**不是** turn 合同变更——`outcome` 枚举与帧形状都不动。  
**代码**：`internal/voicepoc/volc_duplex.go`（`collectTurn` 的 `err != nil` 分支）  
**门禁**：`./scripts/dev-check.sh` → All checks passed；`go test ./...` 23 包全绿

## 原理与背景

**`outcome=partial` 只表示「等待窗口耗尽但捞到了内容」，不表示「上游断了」。**

两者对排障的意义完全相反：前者是供应商慢，后者是 dupline 已死、后面每一轮都会继续失败。把它们压成同一个标签，等于把致命故障伪装成正常降级。

## 缺口根因

`collectTurn` 的读循环里，`recv` 返回错误时**先看有没有内容**，有就返回 `partial` 且 **`err = nil`**：

```go
// 旧实现
evt, err := s.recv(readCtx)
if err != nil {
    out.AssistantText = strings.TrimSpace(text.String())
    // Graceful degradation: if we have any partial content (ASR or TTS),
    // return it instead of failing. Timeout errors are recoverable.
    if text.Len() > 0 || out.Transcript != "" || seenUserProgress || seenResponse {
        out.Outcome = TurnOutcomePartial
        return out, nil          // ← 读错误也走这里
    }
    ...
}
```

注释写的是「Timeout errors are recoverable」，但条件分支对**所有**错误一视同仁。`DeadlineExceeded`（窗口到点）与 `use of closed network connection`（对端已死）被判成同一档。

真机日志里两轮的形状都不可能是超时：

| 轮次 | `duration_ms` | `outcome` | 实际情况 |
|---|---|---|---|
| turn-2 | **2** | partial | `recv` 立即报错 —— 连接在 collect 之前就断了 |
| turn-3 | 1666 | partial | 同上，新一轮 duplex 也只撑了 1.6s |

紧跟着的日志直接给了物证：

```
21:47:42.920  provider reopened after audio forward failure; retrying chunk
              original_err: failed to write msg: ... use of closed network connection
```

`partial` + `nil error` 还带来第二个后果：调用方那句 WARN（`turn result non-ok outcome, ...`）里 `"err": null`，排障时看不到任何错误文本。

## 方案

```go
if err != nil {
    out.AssistantText = strings.TrimSpace(text.String())
    if errors.Is(err, context.DeadlineExceeded) {
        // 窗口到点：仍有内容就降级交出，否则报 timeout
        if text.Len() > 0 || out.Transcript != "" || seenUserProgress || seenResponse {
            out.Outcome = TurnOutcomePartial
            return out, nil
        }
        out.Outcome = TurnOutcomeTimeout
        collectErr = fmt.Errorf("duplex turn timeout: %w", err)
    } else {
        // 读失败：上游死了。仍然交出已到的内容，但如实标 error 并带回 err
        out.Outcome = TurnOutcomeError
        collectErr = err
    }
    return out, collectErr
}
```

判据从「有没有内容」换成「**错误是不是窗口到点**」。内容保留仍然照做——救人一命的部分不变，改的是标签与 `err`。

### 为什么返回非 nil error 不会把 iOS 打死

`provider_volc_duplex.go` 的 `user.speech.end` 分支里，**只要 outcome 已设置且非 ok，就先走**：

```go
if turn.Outcome != "" && turn.Outcome != voicepoc.TurnOutcomeOK {
    s.logger.Warn("turn result non-ok outcome, sending ai.turn.end to unblock iOS",
        ..., "outcome", turn.Outcome, ..., "err", err)
    return s.turnToOutbound(turn), nil
}
```

所以 `outcome=error` 依然发 `ai.turn.end`、依然返回 nil error、iOS 依然能离开 `.processing`。变化只有两处：日志标签从 `partial` 变成 `error`，WARN 里 `err` 不再是 null。

iOS 侧 `WSControlFrame.TurnOutcome` 已含 `error`，且只有 `timeout` 会走 `.failed("turn_timeout")`——`error` 走正常 `aiTurnEnd`，无行为回归。

## 新方案理由

- **不新增 outcome 取值**：v2 schema 的枚举是 `ok|partial|timeout|error`，冻结期不加值。`error` 本来就该覆盖这一类。
- **不把读错误也降级成 `partial`**：「上游断了」与「上游慢」必须可分，否则真机排障只能靠 `duration_ms` 猜。
- **不改 `partial` 在窗口耗尽时的语义**：`TestCollectTurn_OutcomePartialOnProgressThenWaitExpired` 原样通过，说明正常降级路径没动。

## 影响面

- **协议**：**零改动**。不改 schema、不加帧字段。
- **观测**：这类轮的 `logx.Segment` outcome 从 `partial` 变 `error`，WARN 带上错误文本。此前按 `partial` 统计的看板会看到分布位移。
- **行为**：iOS 侧无变化（都是非 ok outcome → 发 `ai.turn.end`）。上游确实已死，后续轮次仍会失败——但网关现在会先把 session 落库（见 `docs/42`），不再丢整场。
- **发布**：无迁移、无配置项。

## 明确不做

- 不在此处重连 duplex（abort 路径已有 `resetDuplex`；读失败的恢复归 reopen-once 与 `docs/39`）
- 不改 `WaitTurnResult` 的 20s 重试逻辑（只对 `DeadlineExceeded` 生效，不受本改动影响）
- 不动 `preload` 里 `evt.Type == "error"` 的分支（本来就正确）

## 测试

`internal/voicepoc/collect_turn_outcome_test.go`

| 测试 | 守住的不变量 |
|---|---|
| `TestCollectTurn_OutcomeErrorOnTransportFailureAfterProgress`（新增） | 有进度后连接断 → `outcome=error` + 非 nil err + 内容仍捞回 + **不等满窗口** |
| `TestCollectTurn_OutcomePartialOnProgressThenWaitExpired` | 窗口耗尽 + 有内容 → 仍是 `partial` + nil err（未回归） |
| `TestCollectTurn_OutcomeTimeoutOnSilentWait` | 整窗静默 → `timeout` + 非 nil err（未回归） |
| `TestCollectTurn_OutcomeErrorOnProviderErrorEvent` | 供应商 error 事件 → `error`（未回归） |
| `TestCollectTurn_OutcomeOKOnResponseDone` | happy path → `ok` + nil err（未回归） |

```bash
go test ./internal/voicepoc/ -run "TestCollectTurn_" -v
./scripts/dev-check.sh
```
