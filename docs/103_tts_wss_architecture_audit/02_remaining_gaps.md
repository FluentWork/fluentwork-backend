# TTS WSS 架构剩余问题与根因分析

**日期**：2026-09-21  
**状态**：基于代码实际检查，非文档推测

---

## 一、仍未关闭的问题（4 个）

### 问题 1：provider 侧独立维护 turn 状态

**现象**：
- handler 有 `Turn` 状态机（`turn.go`）
- provider 仍有 `activeTurnID` / `turnStarted` / `ttsStarted` / `collectingTurn` 四个状态字段

**代码位置**：
```go
// provider_volc_duplex.go:113-196
turnStarted time.Time        // turn 开始时间
activeTurnID string          // 当前 turn id
ttsStarted bool             // 是否已发 ai.tts.start
collectingTurn bool         // 是否在等待 WaitTurnResult
```

**问题**：
- 同一个 turn 的状态分散在两个地方
- handler 的 `Turn.State()` 无法回答"provider 在干什么"
- provider 的 `turnStarted` 无法回答"handler 认为这是什么状态"

**根因**：
M4 只改了 handler 侧，provider 侧未触及。provider 需要这些字段因为：
1. `turnStarted` 用于计算延迟（`markFirstAudio`）
2. `activeTurnID` 用于 `canonicalTurnID` 回落
3. `ttsStarted` 用于判断是否已发 ai.tts.start
4. `collectingTurn` 用于防止打断期间清空状态

**影响范围**：
- 任何需要"知道 turn 在哪个阶段"的逻辑都要看两处
- turn 状态不一致时无单一来源判断谁对

---

### 问题 2：turn id 双重回落规则

**现象**：
- handler 回落 `session_id`（`turn.go:246-250`）
- provider 回落 `turn-<seq>`（`turn_id.go:8-17`）

**代码对比**：
```go
// handler 侧（Turn.ApplySpeechEnd）
if id := strings.TrimSpace(clientTurnID); id != "" {
    t.id = id
} else if strings.TrimSpace(t.id) == "" {
    t.id = t.session  // 回落 session_id
}

// provider 侧（canonicalTurnID）
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

**调用位置**：
- `AssistantTextDelta:278` - 文本帧用 `canonicalTurnID(activeTurnID, nextSeq)`
- `AssistantAudio:347` - 音频帧用 `canonicalTurnID(activeTurnID, nextSeq)`
- `ttsStartFrame:383` - start 帧用 `canonicalTurnID`

**问题**：
同一个 turn 如果客户端未提供 turn_id：
- badge / rescue anchor 会记录为 `session_id`
- ai.text.delta / ai.tts.start / 音频帧会标记为 `turn-<seq>`
- 客户端收到的同一轮对话帧带不同 turn_id

**真实影响**：
iOS 按 turn_id 分组帧，不同 id 的帧不会被关联到同一个对话项。

---

### 问题 3：上行音频块常量重复

**现象**：
- `voiceduplex/volc_duplex.go:57` 定义 `pcm16kChunkBytes = 640`
- `provider_dev_echo.go:106` 定义 `devEchoChunkBytes = 640`

**用途**：
两者都表示"20ms @ 16kHz mono s16le"，但：
- `pcm16kChunkBytes` 用于生产上行音频切块
- `devEchoChunkBytes` 用于 dev-echo 模拟音频

**问题**：
1. 两处字面量，含义相同但无类型或测试强制一致
2. 与采样率的关系隐式（`16000 * 1 * 2 / 8 * 20 / 1000 = 640`）
3. 不如 `AudioFormat.FrameBytes(20)` 可推导

**为什么 M3 没解决**：
M3 只统一了**下行**帧（gateway→client），上行块（client→gateway→provider）未触及。

---

### 问题 4：97_ 设计的接口未落地

**未实现的接口**（5 个）：

| 接口 | 设计位置 | 落地情况 |
|---|---|---|
| `DuplexTransport` / `DuplexConn` | 97_ §一 | ❌ 从未实现 |
| `TransportEvent` | 97_ §一 | ❌ 从未实现 |
| `TurnTimeouts` | 97_ §二 | ❌ 从未实现 |
| `UtteranceSink` | 97_ §三 | ❌ 从未实现 |
| `TurnObserver` | 97_ §四 | ❌ 从未实现 |

**实际接缝**：
- 传输层：`VoiceProvider` / `VoiceProviderSession`（`provider.go`）
- 事件：`ProviderOutbound`（`provider.go:19`）
- 超时：散布在多处（`silence_detector.go` / `collectTurn` / `WaitTurnResult`）

**为什么没落地**：
98_ §五记录了"M3 停在切帧层"的原因（当时发 ai.tts.start 会静音），后续没有继续推进接口抽象。

**影响**：
- 无法替换传输层实现（vendor 锁定）
- 超时值分散，"一轮最多等多久"这个产品决策拆在四处
- 无统一观测点（TurnObserver）

---

## 二、根因总结

### 2.1 M1-M6 的边界

M1-M6 的实施边界是：
- ✅ **handler 侧重构完成**：Turn / SeqAllocator / HandleControl
- ✅ **切帧层统一**：audioFramer
- ❌ **provider 侧未触及**：activeTurnID / turnStarted / ttsStarted 仍在

**设计文档的假设 vs 实际落地**：
- 96_ 设计假设 provider 也会走 Utterance（§3.1）
- 98_ §五发现"不能给 AI 发 ai.tts.start"（会静音）
- 实际只统一了切帧层，流控制（start/end）仍在 provider 内部

### 2.2 为什么停在这里

时间线（根据 98_ §五）：
1. **2026-09-12**：iOS 恢复 `EngineBackedTTSDecoder` → 静音 → 回滚
2. **2026-09-18**：M1-M6 落地，M3 只做切帧层
3. **2026-09-18 后**：iOS 删除回退路径（`d004869`），网关开始发 ai.tts.start（`3cb3774`）

**结论**：M3 当时因"会导致静音"停住，后续完成了 ai.tts.start 发送，但**未回头统一流控制**。

### 2.3 技术债务清单

| 债务 | 成因 | 清理难度 |
|---|---|---|
| provider 独立状态 | M4 边界：只改 handler | 中（需协调 handler/provider 状态） |
| turn id 双重回落 | 历史遗留 + M4 未完成 | 低（统一回落规则即可） |
| 上行块常量重复 | M3 边界：只做下行 | 低（抽取共享常量） |
| 接口未落地 | 98_ §五：M3 停住后未继续 | 高（需完整接口层） |

---

## 三、当前架构的实际形态

### 实际分层（vs 96_ 设计）

```
┌─────────────────────────────────────────┐
│ L4 特性层：Rescue / Badge / Metrics    │ ← 直接调用，无 Observer
└────────────┬────────────────────────────┘
             │
┌────────────▼────────────────────────────┐
│ L3 会话层                               │
│  - Turn 状态机（handler 侧）✅          │
│  - SeqAllocator ✅                      │
│  - HandleControl + 策略表 ✅            │
│  - provider 侧独立状态 ⚠️              │
└────────────┬────────────────────────────┘
             │
┌────────────▼────────────────────────────┐
│ L2 传输层                               │
│  - VoiceProvider（实际接缝）            │
│  - audioFramer（切帧层统一）✅          │
│  - 流控制（start/end）仍在 provider ⚠️  │
└────────────┬────────────────────────────┘
             │
┌────────────▼────────────────────────────┐
│ L1 协议层：voiceproto（冻结）✅         │
└─────────────────────────────────────────┘
```

**与设计的差异**：
1. ✅ Turn 状态机落地，但 provider 有平行状态
2. ✅ SeqAllocator 完全按设计
3. ⚠️ Utterance 只用于救援，AI 音频不走它
4. ❌ DuplexTransport 未实现，用 VoiceProvider
5. ❌ TurnObserver 未实现，特性层直接调用

---

## 四、影响评估

### 4.1 功能正确性：✅ 无问题

- 所有测试绿色（208 个）
- 真机验证通过（101_）
- M1-M6 解决了 5/9 个结构问题

### 4.2 可维护性：⚠️ 有隐患

**当前隐患**：
1. **问题 1（双重状态）**：出现 turn 相关 bug 时要看两处
2. **问题 2（双重回落）**：客户端可能收到同一 turn 的不同 id
3. **问题 3（常量重复）**：采样率变更时要改两处
4. **问题 4（接口缺失）**：无法替换 vendor、超时分散

**但不是"乱"**：
- 每个局部都有测试
- 真机教出来的行为都保留了
- 问题在"关系"不在"函数"（99_ §一）

### 4.3 扩展性：⚠️ 受限

**受限的部分**：
1. 无法替换传输层（vendor 锁定在 VoiceProvider）
2. 无法独立测试"会话层"（与 provider 耦合）
3. 新 vendor 需要重复实现 turn id / 切帧 / 流控制

**不受限的部分**：
1. 可添加新特性（Rescue / Badge 已证明）
2. 可独立测试每个 handler（208 个测试）

---

## 五、是否需要立即修复？

### 建议：**分级处理**

#### 🔴 P0：需要立即修复

**问题 2（turn id 双重回落）**
- **影响**：客户端可能收到同一轮对话的不同 id
- **风险**：iOS 按 id 分组帧，不同 id 导致 UI 显示错误
- **修复成本**：低（统一回落规则）
- **见**：[05_next_steps.md](./05_next_steps.md) §一

#### 🟡 P1：应该修复但可延后

**问题 3（上行块常量重复）**
- **影响**：采样率变更时需改两处
- **风险**：低（采样率不常变）
- **修复成本**：低（抽取共享常量）

#### 🟢 P2：技术债务，规划清理

**问题 1（双重状态）**
- **影响**：可维护性，不影响功能
- **风险**：中（复杂度积累）
- **修复成本**：中（需协调两侧状态）

**问题 4（接口未落地）**
- **影响**：扩展性，不影响当前功能
- **风险**：低（当前只有一个 vendor）
- **修复成本**：高（需完整接口层）

---

## 六、总结

### 当前架构评价：**B 级（可用，有优化空间）**

**✅ 优点**：
1. 核心问题已解决（序号连续、切帧统一、状态机、策略表）
2. 测试覆盖充分（208 个 + 真机验证）
3. 真机踩过的坑都有防护

**⚠️ 缺点**：
1. provider 侧状态与 handler 侧部分重叠
2. turn id 回落规则不统一（客户端可能见到不同 id）
3. 接口抽象未完成（扩展受限）

**建议行动**：
1. 立即修复 turn id 双重回落（P0）
2. 择机修复上行块常量重复（P1）
3. 规划 provider 状态整合（P2）
4. 接口抽象留待真实需求驱动（有第二个 vendor 时再做）
