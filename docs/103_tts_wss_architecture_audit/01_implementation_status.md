# M1-M6 落地状态与 F1-F9 关闭情况

**日期**：2026-09-21  
**基准**：docs/98_ §七实施结果（2026-09-18）  
**审计范围**：94_ 识别的 9 个发现（F1-F9）与 98_ 规划的 6 个步骤（M1-M6）

---

## 一、M1-M6 实施结果

| 步骤 | 提交 | 状态 | 核心改动 | 净行数 |
|---|---|---|---|---|
| M1 | a566c4f | ✅ 完成 | `SeqAllocator` 抽出，序号属于会话不属于 provider | -20 |
| M2 | b18664b | ✅ 完成 | `Utterance` / `UtteranceWriter`，救援先走它 | +230 |
| M3 | 5ff7543 | ⚠️ 部分 | 共享切帧层 `audioFramer`，流控制仍分开 | -15 |
| M4 | b55181c | ✅ 完成 | `Turn` 状态机接管身份与顺序 | +180 |
| M5 | b991db2 | ✅ 完成 | `HandleControl` 279 → 29 行，策略表化 | -250 |
| M6 | 34395f0 | ✅ 完成 | `voiceduplex` 拆包 + depguard 规则 | 重组 |

**总结**：M1/M2/M4/M5/M6 完全落地；M3 只完成切帧层统一，流的开合（ai.tts.start）仍分开。

---

## 二、F1-F9 发现关闭情况

### ✅ 完全关闭（5 个）

| 发现 | 原问题 | 关闭方式 | 验证位置 |
|---|---|---|---|
| **F4** | 二进制帧编码两个实现 | M3：只剩 `voiceproto.AITTSAudio.Encode()` | `utterance.go:217` audioFramer.encode |
| **F6** | 4 种错误策略散在 case 里 | M5：策略表 `providerErrorStrategies` | `handler_control.go` |
| **F7** | 传输层住在 `poc` 包里 | M6：拆到 `voiceduplex` + depguard | `.golangci.yml` no-poc-in-production |
| **F8** | 悬空注释（synthesis endpoint） | M3 顺手合并 | 注释已删除 |
| **F9** | 生产只用 voicepoc 1 个函数 | M6：现在只依赖 voiceduplex | 依赖图已清理 |

### ⚠️ 部分关闭（4 个）

#### F1：turn 生命周期没有唯一所有者

**关闭部分**：
- ✅ handler 侧：`Turn` 状态机接管（`turn.go`）
- ✅ 三次 JSON 解析变一次（M4）
- ✅ badge / rescue 从 `Turn.ID()` 取

**未关闭部分**：
- ❌ provider 侧仍有 `activeTurnID` 字段（`provider_volc_duplex.go:122`）
- ❌ provider 自己的 turn 开始/结束标记（`turnStarted` / `ttsStarted`）

**影响**：provider 仍在管理自己的 turn 状态，与 handler 的 `Turn` 状态机部分重叠。

---

#### F2：同一份 JSON 解 3 次、规则写 2 遍

**关闭部分**：
- ✅ `startCollectTurn` 的三次 unmarshal 合并为一次（M4）
- ✅ `resolvedUserText` 统一文本取值规则

**验证**：`handler.go` 中 badge / rescue / ASR 三条路径现在共享同一次解析。

---

#### F3：turn id 三套取值规则、2 处重复实现

**关闭部分**：
- ✅ handler 侧：`Turn.ApplySpeechEnd` 一处回落（`turn.go:246-250`）
- ✅ badge / rescue anchor 都从 `Turn.ID()` 取

**未关闭部分**：
- ❌ provider 的 `canonicalTurnID` 仍存在（`turn_id.go`）
- ❌ provider 回落规则是 `turn-<seq>`，与 handler 的 `session_id` 不同
- ❌ 同一 turn 可能在不同帧上以两个名字出现

**代码位置**：
```go
// handler 侧（turn.go:246-250）
if id := strings.TrimSpace(clientTurnID); id != "" {
    t.id = id
} else if strings.TrimSpace(t.id) == "" {
    t.id = t.session  // 回落 session_id
}

// provider 侧（turn_id.go + provider_volc_duplex.go:244, 278, 347）
turnID := canonicalTurnID(s.activeTurnID, s.nextSeq)
// activeTurnID 为空时回落 "turn-<seq>"
```

---

#### F5：帧大小常量 4 个

**关闭部分**：
- ✅ 客户端帧（3200）：由 `AudioFormat.FrameBytes(100)` 推导
- ✅ 测试钉住：`utterance_test.go:289-295`

**未关闭部分**：
- ❌ 上行块 640 仍在两处：`voiceduplex/volc_duplex.go:57` 与 `provider_dev_echo.go:106`
- ⚠️ 两处都叫不同名字：`pcm16kChunkBytes` vs `devEchoChunkBytes`

**测试覆盖**：`TestAudioSizesAreDerivedFromTheSampleRate` 钉住了它们与采样率的关系，但未统一常量本身。

---

## 三、核心架构对象落地情况

### 3.1 SeqAllocator ✅

**设计目标**（96_ §3.2）：让"序号回退"在类型上不可能  
**落地情况**：完全按设计实现

| 设计 | 实现位置 | 状态 |
|---|---|---|
| 属于会话不属于 provider | `seq_allocator.go` | ✅ |
| Next() 单调递增 | atomic.Uint32 | ✅ |
| 无需手工搬运 | Open 时传入同一个 allocator | ✅ |
| 测试钉住序号连续性 | `handler_audio_sequence_test.go` | ✅ |

**收益**：`carryAudioSequence` 与序号调和逻辑删除（净 -20 行）。

---

### 3.2 Utterance / UtteranceWriter ⚠️

**设计目标**（96_ §3.1）：AI 与梯子的音频走同一个 writer  
**落地情况**：部分实现

| 组件 | 设计 | 实现 | 差异 |
|---|---|---|---|
| Utterance 类型 | ID / Kind / Format / Text / Audio | `utterance.go:75-107` | ✅ 完全一致 |
| UtteranceWriter | Text / Audio / Finish / Interrupt | `utterance.go:141-149` | ⚠️ **无 Text 方法** |
| beginUtterance | 发 ai.tts.start，返回 writer | `utterance.go:267-300` | ✅ 落地 |
| streamWriter | 切帧 + 节奏 + 结束 | `utterance.go:238-345` | ✅ 落地 |
| audioFramer | 切片 + 编号 + 留尾 | `utterance.go:166-222` | ✅ 落地 |

**重要差异**：
1. **AI 的音频不走 UtteranceWriter**（98_ §五明确说明）
   - 原因：当时发 ai.tts.start 会导致静音（客户端路径问题）
   - 后续：客户端删除回退路径后才完成（`provider_volc_duplex.go:353-361`）
2. **文本不进这条流**：梯子文本走 `ai.rescue.ladder`，不是 Text(delta)

**共享的部分**：
- ✅ 切帧：`audioFramer`（唯一编码位置）
- ✅ 帧长：`AudioFormat.FrameBytes` 推导
- ❌ 流控制：ai.tts.start 发送时机仍在 provider 内部

---

### 3.3 Turn 状态机 ✅

**设计目标**（96_ §3.3 + 97_ §二）：turn id 与状态迁移的唯一来源  
**落地情况**：完全按设计实现

| 设计 | 实现位置 | 状态 |
|---|---|---|
| 状态定义与迁移白名单 | `turn.go:19-134` | ✅ |
| 拒绝非法迁移并计数 | `Turn.Apply` + `rejected` map | ✅ |
| ID 解析一次 | `Turn.ApplySpeechEnd` | ✅ |
| badge / rescue 从这里取 ID | 各自的调用点 | ✅ |

**97_ 设计差异**：
- 无 `Abandoned` 态：abort 直接落到 `TurnClosed`
- 无 `TurnObserver` 接口：特性层仍是直接调用

---

## 四、97_ 接口设计未落地部分

97_ 设计了完整的接口体系，但**大部分未实现**：

| 设计接口 | 落地情况 | 实际接缝 |
|---|---|---|
| `DuplexTransport` / `DuplexConn` | ❌ 从未实现 | `VoiceProvider` / `VoiceProviderSession` |
| `TransportEvent` | ❌ 从未实现 | `ProviderOutbound` |
| `TurnTimeouts` | ❌ 从未实现 | 超时散布多处 |
| `UtteranceSink` | ❌ 从未实现 | 直接 `beginUtterance(...)` |
| `TurnObserver` | ❌ 从未实现 | 特性层直接调用 |

**原因**：98_ §五记录了"为什么 M3 停在切帧层"，后续没有继续推进接口抽象。

---

## 五、测试覆盖情况

### 新增测试（M1-M6 期间）

| 测试 | 覆盖内容 | 位置 |
|---|---|---|
| `handler_audio_sequence_test.go` | 序号跨重开连续性 | M1 |
| `turn_test.go` | 状态机迁移合法性 | M4 |
| `turn_id_test.go` | canonicalTurnID 规则 | M4 |
| `utterance_test.go` | 切帧 + 节奏 + 结束 | M2/M3 |
| `handler_control_policy_test.go` | 失败策略表 | M5 |

### 现有测试仍绿

所有 208 个测试在 M1-M6 后仍然通过，**无需改断言**（98_ §三停手条件）。

---

## 六、判据验证

98_ §三 的可观测判据：

| 判据 | 验证结果 |
|---|---|
| 删掉 `carryAudioSequence` 后全绿 | ✅ 已删除（M1） |
| `provider_volc_duplex` 的 `encodeAudioFrame` 消失 | ✅ 已删除（M3） |
| turn id 回落一处 | ⚠️ handler 一处，provider 仍有自己的回落 |
| `HandleControl` 279 → 29 行 | ✅ 实际 29 行（M5） |
| `go list -deps ./internal/voicegateway \| grep voicepoc` 为空 | ✅ depguard 强制（M6） |

---

## 七、总结

### 完成度：85%

- **结构改善**：序号管理、切帧统一、状态机、失败策略表、包拆分全部完成
- **接口抽象**：DuplexTransport / TurnTimeouts 等未实现，保持了 VoiceProvider 接缝
- **剩余工作**：provider 侧 turn id 回落、上行块常量统一

### 关键成就

M1-M6 解决了 94_ 识别的 **5/9 个结构问题**，且保持了所有测试绿色 + 真机验证通过（101_）。

### 下一步建议

见 [05_next_steps.md](./05_next_steps.md)
