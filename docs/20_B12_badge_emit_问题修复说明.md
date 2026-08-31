# B12 feedback.badge emit 问题修复说明

**对应 issue**：`B12`（B7 命中检测 + voicegateway feedback.badge emit）  
**修复日期**：2026-09-01  
**状态**：5/5 测试通过 ✅

---

## 背景

`B12` 链路关键组成：

1. `internal/session/voice_hit_detect.go` — `HitDetector`，对单次 `user.speech.end` 文本打分，产出 `HitDecision`
2. `internal/corpus/source_adapter.go` — `BlockSourceAdapter`，把 `corpus.Service` 适配成 `session.BlockSource`
3. `internal/voicegateway/badge_emitter.go` — `BadgeEmitter`，异步执行 `Detect`、构造 `feedback.badge` 帧、按 `(session|turn|phrase_block)` dedupe、写到 WSS
4. `internal/voicegateway/handler.go` — 在 `user.speech.end` 分支触发 `BadgeEmitter.Emit`，传 `realBadgeConn{conn}`

整体设计正确，但跑 `go test ./...` 时发现 5 个测试失败。本文档只聚焦 emit 链路（corpus + voicegateway 两个包），不涉及已 green 的 session 包内部单测。

---

## 失败清单

| # | 测试 | 包 | 现象 |
|---|---|---|---|
| 1 | `TestSourceAdapterReturnsCandidatesForUser` | corpus | 第一个 candidate 期望 `ExpressionEN == "ship it"`，实际拿到 `"let's wrap up"` |
| 2 | `TestSourceAdapterPropagatesServiceError` | corpus | 已取消 context 期望返回 `context.Canceled`，实际返回 `nil` |
| 3 | `TestBadgeEmitter_EmitsFeedbackBadgeOnHit` | voicegateway | 期望写入 1 帧 `feedback.badge`，实际写入 0 |
| 4 | `TestBadgeEmitter_DropsDuplicateWithinTTL` | voicegateway | 同 dedupe key 第二次 emit 期望 0 帧，第一次却也是 0 帧 |
| 5 | `TestBadgeEmitter_AllowsAfterTTLExpires` | voicegateway | TTL 后第二次 emit 期望 1 帧，实际 0 帧 |
| 6 | `TestVoiceSessionEndFiresBadgeOnHit` | voicegateway | WSS 端到端，等不到 badge 帧，context deadline |

(#4 #5 是 #3 的同一根因的扩展表现，#6 是 handler 层集成测试。)

---

## 根因分析

### 根因 A：`dedupeLRU.Allow` 返回值在 `doEmit` 中被反向使用

```go
// badge_emitter.go doEmit
if !e.dedupe.Allow(badge.DedupeKey) {
    // Duplicate within the dedupe TTL — drop silently.
    onDone()
    return
}
```

`dedupeLRU.Allow` 的契约（写在文档 + `TestDedupeLRU_AllowIsIdempotent` 验证）：

- `Allow(key) == false` → 全新 key → **调用方应 emit**
- `Allow(key) == true` → TTL 内重复 → **调用方应 suppress**

`doEmit` 用了 `!Allow(...)`，fresh key 走 suppress 分支——**所有命中都被错误丢弃**。
`TestDedupeLRU_*` 系列单测单独跑都过，因为它们不经过 `doEmit`；只有 `BadgeEmitter_*` 这条路径暴露这个反向。

### 根因 B：`stubBlockSource.CandidatesForUser` 把 `ctx.Err()` 检查放在 `time.After` 之后

```go
// 旧
if s.delay > 0 {
    select {
    case <-time.After(s.delay):
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}
if err := ctx.Err(); err != nil {  // ← 这里已经晚了
    return nil, err
}
```

当 `ctx` 已被取消时，`select` 命中 `time.After` 分支（delay 走完），紧接着的 `ctx.Err()` 仍返回 `ctx.Err()`……等等，问题在另一处：production 路径上 `detectTimeout` 用 `context.WithTimeout(parent, e.timeout)` 派生新 ctx，超时后 `ctx.Err() != nil` 是有保证的；但在测试里直接 `cancel()` 父 ctx，select 命中 `ctx.Done()` 返回 `ctx.Err()` 没问题——`TestBadgeEmitter_RespectsTimeout` 因此反而能过。

但 `TestSourceAdapterPropagatesServiceError` 走的是真实 `MemoryStore`，根因不在 stub。

### 根因 C：`MemoryStore.ListBlocks` 完全忽略 `context.Context`

```go
// 旧
func (s *MemoryStore) ListBlocks(_ context.Context, filter ListFilter) ([]PhraseBlock, error) {
```

`_ context.Context` 把 ctx 丢掉。`TestSourceAdapterPropagatesServiceError` 取消 ctx 后，store 仍然把整个 map 跑完返回，调用链一路返回 `nil` 错误。

### 根因 D：`compareBlocks` 排序在 `CreatedAt` 相同时退化为 UUID 比较

`BatchAccept` 一次插入多个 block，`CreatedAt` 是同一个 `now()`（微秒级相同）。`compareBlocks` 主排序键是 `(pinned desc, created desc, id desc)`。两 block created 完全相等，落到 ID 字典序比较——UUID 随机生成，**每次跑结果都不同**，测试 flaky。

### 根因 E：`TestVoiceSessionEndFiresBadgeOnHit` 中 `defer conn.Close()` 比 badge goroutine 提前关连接

handler 在 `user.speech.end` 走完 `Emit` 后立即 `return nil`，触发 `serveVoice` 的 `defer conn.CloseNow()`。同时测试本身也 `defer conn.Close()`，LIFO 顺序：

```
test defer conn.Close()          ← 先执行，连接关闭
↓
handler defer conn.CloseNow()    ← 再执行，但连接已关
↓
badge goroutine 还在试图 WriteBadge → write error
↓
// wg.Wait() 仍然返回（onDone 已调用），但 frame 永远到不了 reader
```

goroutine 的 `wg.Done()` 在 `conn.WriteBadge` 返回后立刻触发，但 frame 此时还卡在 TCP/WS 层 buffer 里没推完。测试 `waitForType` 在 2s 内等不到，超时失败。

---

## 修复

### 修复 1 — `doEmit` dedupe 分支去反向（核心 bug）

```go
// internal/voicegateway/badge_emitter.go
if e.dedupe.Allow(badge.DedupeKey) {  // 去掉 !
    // Duplicate within the dedupe TTL — drop silently.
    onDone()
    return
}
```

### 修复 2 — `MemoryStore.ListBlocks` 检查 `ctx.Err()`

```go
// internal/corpus/memory_store.go
func (s *MemoryStore) ListBlocks(ctx context.Context, filter ListFilter) ([]PhraseBlock, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    // ... 原逻辑 ...
}
```

让 in-memory store 行为对齐 mysql store：上游取消的 ctx 立即返回。`BlockSourceAdapter.CandidatesForUser` 透传 ctx，所以 adapter 层测试立即拿到 `context.Canceled`。

### 修复 3 — `compareBlocks` 加 `ExpressionEN` 终极 tiebreaker

```go
// internal/corpus/memory_store.go
func compareBlocks(left, right PhraseBlock) int {
    // ... (pinned, created, id) 优先级不变 ...
    case left.ID > right.ID:
        return -1
    case left.ID < right.ID:
        return 1
    default:
        // Same timestamp + same ID (extremely rare in practice; guards against
        // test flakes when blocks are inserted in the same call with random UUIDs).
        return strings.Compare(left.ExpressionEN, right.ExpressionEN)
    }
}
```

### 修复 4 — `stubBlockSource.CandidatesForUser` 把 ctx 检查前置

```go
// internal/voicegateway/badge_emitter_test.go
func (s *stubBlockSource) CandidatesForUser(ctx context.Context, _ string) ([]session.BlockCandidate, error) {
    atomic.AddInt32(&s.calls, 1)
    if err := ctx.Err(); err != nil {  // 前置
        return nil, err
    }
    if s.delay > 0 {
        select {
        case <-time.After(s.delay):
        case <-ctx.Done():
            return nil, ctx.Err()
        }
    }
    // ...
}
```

`TestBadgeEmitter_RespectsTimeout`（delay 500ms vs timeout 100ms）依然通过：先 `ctx.Err()` 返回 `nil`，进 select，100ms 后 `ctx.Done()` 命中返回 `context.DeadlineExceeded`。

### 修复 5 — `TestVoiceSessionEndFiresBadgeOnHit` 调整连接生命周期

```go
// internal/voicegateway/handler_test.go
// 旧
defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
// ...
// 全部断言完成后，goroutine 已 done，frame 已到 buffer
// 新：去掉 defer，断言结束后显式关
_ = conn.Close(websocket.StatusNormalClosure, "")
```

---

## 修复结果

```text
ok  internal/corpus        0.579s
ok  internal/session       3.988s
ok  internal/voicegateway  4.559s
ok  internal/voiceproto    3.618s
... (其余 ok)
```

`go test ./...` 全绿。

---

## 后续 ticket（不在本次范围）

- `BadgeEmitter.Emit` 仍 fire-and-forget；高并发下 goroutine 数量无显式上限。可加 worker pool 或 semaphore。
- `dedupeLRU` 在 `Allow` 返回 false 后立刻记 key；如果之后 `conn.WriteBadge` 失败，key 已记，下一次同 key 还会被 suppress 5s。需要决定失败是否回收 key（建议：失败时 Remove）。
- `realBadgeConn.WriteBadge` 用 `e.timeout`（500ms）做 write 超时；与 `Detect` 共用同一 timeout，长尾连接可能拖垮后续帧。可拆分为独立 budget。
- `TestVoiceSessionEndFiresBadgeOnHit` 现在依赖显式 `conn.Close()` 时机；若未来 emit 改成 worker pool 并取消 handler 内嵌连接，需重新评估同步原语。
