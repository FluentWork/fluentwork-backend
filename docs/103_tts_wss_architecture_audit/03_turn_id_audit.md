# turn_id 架构审计与统一方案

**日期**：2026-09-21  
**问题**：turn id 有两套回落规则，导致同一 turn 可能在不同帧上显示不同 id

---

## 一、当前 turn_id 体系

### 1.1 两个来源

| 来源 | 位置 | 回落规则 | 使用方 |
|---|---|---|---|
| **Turn 状态机** | `turn.go:246-250` | `client_id` → `session_id` | badge, rescue anchor, end-of-session |
| **canonicalTurnID** | `turn_id.go:8-17` | `client_id` → `turn-<seq>` | ai.text.delta, ai.tts.start, 音频帧 |

### 1.2 代码对比

```go
// Turn.ApplySpeechEnd（handler 侧）
if id := strings.TrimSpace(clientTurnID); id != "" {
    t.id = id
} else if strings.TrimSpace(t.id) == "" {
    t.id = t.session  // 回落 session_id
}

// canonicalTurnID（provider 侧）
func canonicalTurnID(clientID string, seq int) string {
    if t := strings.TrimSpace(clientID); t != "" {
        return t
    }
    if seq <= 0 {
        return ""
    }
    return fmt.Sprintf("turn-%d", seq)  // 回落 turn-<seq>
}
```

---

## 二、调用路径分析

### 2.1 Turn.ID() 调用方（handler 侧）

| 调用位置 | 用途 | 帧类型 |
|---|---|---|
| `badge_emitter.go:167` | badge 发射 | `client.badge.hit` |
| `handler_rescue.go:126` | 救援生成 | rescue anchor |
| `handler_rescue.go:157` | 梯子音频 | `ai.rescue.ladder` |
| `utterance.go:289` | 救援 utterance | ai.tts.start (救援) |

### 2.2 canonicalTurnID 调用方（provider 侧）

| 调用位置 | 用途 | 帧类型 | activeTurnID 来源 |
|---|---|---|---|
| `provider_volc_duplex.go:244` | ASR 文本 | `client.asr.transcription` | user.speech.end 解析 |
| `provider_volc_duplex.go:278` | 文本 delta | `ai.text.delta` | 同上 |
| `provider_volc_duplex.go:347` | TTS start | `ai.tts.start` | 同上 |
| `provider_volc_duplex.go:383` | TTS start (批) | `ai.tts.start` | 同上 |
| `provider_volc_duplex.go:966` | Turn 结束 | `ai.turn.end` | 同上 |

**关键**：provider 的 `activeTurnID` 在 `HandleClientControl` 的 `user.speech.end` 分支解析：
```go
// provider_volc_duplex.go:554
if t := strings.TrimSpace(end.TurnID); t != "" {
    s.activeTurnID = t
}
```

---

## 三、问题实例

### 场景：客户端未提供 turn_id

**时序**：
1. 客户端发 `user.speech.start`（无 turn_id）
2. 客户端发 `user.speech.end`（无 turn_id）
3. handler 调用 `Turn.ApplySpeechEnd("")` → Turn.ID = `session_id`
4. provider 的 `activeTurnID` = `""` (未解析到)
5. provider 调用 `canonicalTurnID("", seq=1)` → `"turn-1"`

**结果**：

| 帧类型 | turn_id 值 | 来源 |
|---|---|---|
| `client.badge.hit` | `session_id` | Turn.ID() |
| `ai.rescue.ladder` | `session_id` | Turn.ID() |
| `ai.text.delta` | `turn-1` | canonicalTurnID |
| `ai.tts.start` | `turn-1` | canonicalTurnID |
| `ai.turn.end` | `turn-1` | canonicalTurnID |

**iOS 影响**：
- iOS 按 turn_id 分组帧
- badge 和 ladder 标记为 `session_id`
- AI 回复标记为 `turn-1`
- 两组帧不会关联到同一个对话项

---

## 四、为什么会有两套规则

### 4.1 历史原因

**M4 之前**（94_ F3）：
- turn id 在三处解析：badge、rescue、provider
- 每处各有回落规则
- provider 用序号造 id 是因为"没人给它 turn id"

**M4 实施**（b55181c，2026-09-18）：
- handler 侧统一：`Turn.ApplySpeechEnd` 解析一次
- provider 侧**未改**：`canonicalTurnID` 保持原样

**为什么 M4 没改 provider 侧**：
- M4 目标是"handler 侧统一"，边界是 handler/provider 接缝
- `activeTurnID` 在 provider 内部，改它需要协调两侧

### 4.2 设计意图

96_ §3.3 的设计意图：
> turn id 在**进入 Active 时确定一次**，之后所有出口都从它取。

**落地情况**：
- ✅ badge / rescue 已从 `Turn.ID()` 取
- ❌ provider 出口（ai.text.delta / ai.tts.start / ai.turn.end）仍用 `canonicalTurnID`

---

## 五、统一方案

### 5.1 目标

**唯一来源**：所有帧的 turn_id 都从 `Turn.ID()` 取，无第二套回落。

### 5.2 方案 A：provider 从 Turn 取 id（推荐）

**改动**：
1. provider 在 `user.speech.end` 时从 handler 获取已解析的 turn id
2. 删除 `activeTurnID` 字段
3. 删除 `canonicalTurnID` 函数
4. 所有 `canonicalTurnID(s.activeTurnID, s.nextSeq)` 改为从 Turn 取

**接口变更**：
```go
// VoiceProviderSession 接口增加方法
SetTurnID(turnID string)

// 或者：在 HandleClientControl 返回值中带上
HandleClientControl(ctx, frameType, data) ([]ProviderOutbound, turnID string, error)
```

**调用点**：
```go
// handler.go 在 user.speech.end 分支
if rt.turn.ApplySpeechEnd(end.TurnID) {
    turnID := rt.turn.ID()
    session.SetTurnID(turnID)  // 传递给 provider
    // ...
}
```

**优点**：
- ✅ 单一来源（Turn 状态机）
- ✅ 删除重复逻辑（canonicalTurnID）
- ✅ 符合 96_ 设计意图

**缺点**：
- 需要改 VoiceProviderSession 接口（影响所有实现）

---

### 5.3 方案 B：统一回落规则（最小改动）

**改动**：
1. `canonicalTurnID` 的回落改为 `session_id`
2. `activeTurnID` 在未解析到时回落到 `session_id`

**代码**：
```go
// turn_id.go
func canonicalTurnID(clientID string, sessionID string) string {
    if t := strings.TrimSpace(clientID); t != "" {
        return t
    }
    return sessionID  // 改：原来是 turn-<seq>
}

// provider_volc_duplex.go HandleClientControl
if t := strings.TrimSpace(end.TurnID); t != "" {
    s.activeTurnID = t
} else {
    s.activeTurnID = session.SessionID  // 新增：回落
}
```

**优点**：
- ✅ 改动最小
- ✅ 不改接口
- ✅ 回落规则统一

**缺点**：
- ❌ 仍有两处解析（Turn / provider）
- ❌ 未达成"单一来源"

---

### 5.4 方案 C：Turn 状态机持有 provider 引用

**改动**：
1. Turn 在 `ApplySpeechEnd` 时通知 provider
2. provider 订阅 Turn 事件

**代码**：
```go
// Turn 增加回调
type TurnObserver interface {
    OnTurnIdentified(turnID string)
}

func (t *Turn) ApplySpeechEnd(clientTurnID string, observer TurnObserver) bool {
    // ... 解析 id
    if observer != nil {
        observer.OnTurnIdentified(t.id)
    }
    return ok
}
```

**优点**：
- ✅ 单一来源
- ✅ 符合 97_ §四的 TurnObserver 设计

**缺点**：
- ❌ 引入新概念（Observer）
- ❌ Turn 需要知道谁在观察它

---

## 六、推荐方案与实施

### 推荐：**方案 B（最小改动）**

**理由**：
1. **成本最低**：不改接口，改动集中在两处
2. **风险最小**：不改调用关系，只改回落值
3. **效果相同**：客户端收到统一的 turn_id

**实施步骤**：
1. 修改 `canonicalTurnID` 签名，增加 `sessionID` 参数
2. 修改回落逻辑：`turn-<seq>` → `sessionID`
3. 修改 `HandleClientControl` 中 `activeTurnID` 的赋值，增加回落
4. 更新所有调用点（6 处）
5. 测试：客户端不提供 turn_id 时，所有帧 id 相同

**测试覆盖**：
- 单元测试：`TestCanonicalTurnIDFallsBackToSession`
- 集成测试：`TestAllFramesCarrySameTurnID`

---

## 七、长期方向：方案 A

方案 B 是"止血"，方案 A 是"治本"。

**何时做方案 A**：
- 当需要"provider 完全不知道 turn"时（97_ 的 DuplexTransport 设计）
- 当有第二个 vendor 且不想重复 turn id 逻辑时
- 当重构到"provider 只负责传输"时

**前置条件**：
- 完成 97_ 的接口抽象（DuplexTransport）
- 明确 provider 与 handler 的职责边界

---

## 八、验收标准

**P0（方案 B）**：
1. ✅ 客户端不提供 turn_id 时，所有帧的 turn_id 字段相同
2. ✅ badge、ladder、ai.text.delta、ai.tts.start、ai.turn.end 的 turn_id 都是 `session_id`
3. ✅ 客户端提供 turn_id 时，所有帧使用客户端的值
4. ✅ 现有 208 个测试仍然绿色

**P2（方案 A）**：
1. ✅ `canonicalTurnID` 函数删除
2. ✅ `activeTurnID` 字段删除
3. ✅ 所有 turn_id 查询从 `Turn.ID()` 或接口方法获取
4. ✅ 全仓 `grep -rn "turn-<seq>"` 零命中
