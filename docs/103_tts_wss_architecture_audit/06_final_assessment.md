# TTS WSS 架构最终评估报告

**日期**：2026-09-21  
**审计范围**：internal/voicegateway TTS/WSS 实现  
**结论**：架构整体良好，有 1 个 P0 问题需立即修复

---

## 执行摘要

### 整体评级：B+ (85/100)

**核心成就**（M1-M6，2026-09-18）：
- ✅ 解决了透明重开音频丢失问题（SeqAllocator）
- ✅ 统一了二进制帧编码（audioFramer）
- ✅ 建立了 Turn 状态机（唯一生命周期管理）
- ✅ 失败策略表化（可测试、可审计）
- ✅ 拆分了生产传输层（voiceduplex）

**待修复**：
- 🔴 **P0（紧急）**：turn_id 双重回落规则（影响客户端）
- 🟡 **P1（重要）**：上行音频块常量重复（可维护性）
- 🟢 **P2（技术债）**：provider 状态与 handler 部分重叠（文档化即可）

---

## 一、架构问题诊断结果

### 1.1 原始问题（94_ 识别的 9 个）

| ID | 问题 | 状态 | 关闭方式 |
|---|---|---|---|
| F1 | turn 生命周期无唯一所有者 | ⚠️ 部分关闭 | M4：handler 侧统一，provider 仍独立 |
| F2 | 同一 JSON 解 3 次 | ✅ 已关闭 | M4：统一解析 |
| F3 | turn id 三套取值规则 | ⚠️ 部分关闭 | handler 一处，provider 仍有回落 |
| F4 | 二进制帧编码两个实现 | ✅ 已关闭 | M3：只剩 AITTSAudio.Encode |
| F5 | 帧大小常量 4 个 | ⚠️ 部分关闭 | 下行统一，上行仍重复 |
| F6 | 4 种错误策略散落 | ✅ 已关闭 | M5：策略表化 |
| F7 | 传输层在 poc 包 | ✅ 已关闭 | M6：拆到 voiceduplex |
| F8 | 悬空注释 | ✅ 已关闭 | M3 顺手删除 |
| F9 | 生产只用 poc 1 函数 | ✅ 已关闭 | M6：清理依赖 |

**总结**：9 个问题中 5 个完全关闭，4 个部分关闭。

---

### 1.2 当前问题清单（基于代码实际检查）

#### 🔴 P0：turn_id 双重回落规则

**问题**：
- handler 回落 `session_id`（`turn.go:246-250`）
- provider 回落 `turn-<seq>`（`turn_id.go:8-17`）
- 同一 turn 在不同帧上可能显示不同 id

**影响**：
- iOS 按 turn_id 分组帧
- badge 标记为 `session_id`
- ai.text.delta / ai.tts.start 标记为 `turn-1`
- 两组帧不会关联到同一个对话项

**修复成本**：2-3 小时  
**修复方案**：统一回落规则为 `session_id`（见 05_next_steps.md §一）

---

#### 🟡 P1：上行音频块常量重复

**问题**：
- `voiceduplex/volc_duplex.go:57` 定义 `pcm16kChunkBytes = 640`
- `provider_dev_echo.go:106` 定义 `devEchoChunkBytes = 640`

**影响**：
- 采样率变更时需改两处
- 与采样率的关系隐式（人工计算）

**修复成本**：1-2 小时  
**修复方案**：抽取 `UplinkChunkBytes`（见 05_next_steps.md §二）

---

#### 🟢 P2：provider 侧独立状态

**问题**：
- provider 维护 `activeTurnID` / `turnStarted` / `ttsStarted` / `collectingTurn`
- handler 维护 `Turn` 状态机
- 两处状态部分重叠

**影响**：
- 理解成本：需要看两处才能知道 turn 完整状态
- 不影响功能（测试全绿，真机通过）

**建议**：文档化边界，不立即重构  
**理由**：provider 状态有明确用途（延迟计算、防重发），强行合并收益不明显

---

## 二、重构目标达成度

### 2.1 96_ 设计的三个核心对象

| 对象 | 设计目标 | 落地情况 | 达成度 |
|---|---|---|---|
| **SeqAllocator** | 序号属于会话，不属于 provider | ✅ 完全落地 | 100% |
| **Utterance** | AI 与梯子走同一 writer | ⚠️ 切帧层统一，流控制分开 | 70% |
| **Turn** | turn id 唯一来源 | ⚠️ handler 侧统一，provider 仍独立 | 75% |

**总体达成度**：**85%**

---

### 2.2 M1-M6 实施结果

| 步骤 | 目标 | 状态 | 验证 |
|---|---|---|---|
| M1 | SeqAllocator 抽出 | ✅ 完成 | carryAudioSequence 删除 |
| M2 | Utterance 落地 | ✅ 完成 | 救援走 UtteranceWriter |
| M3 | 切帧层统一 | ⚠️ 部分 | audioFramer 共享，流控制分开 |
| M4 | Turn 状态机 | ✅ 完成 | handler 侧统一 |
| M5 | HandleControl 重构 | ✅ 完成 | 279 → 29 行 |
| M6 | voiceduplex 拆包 | ✅ 完成 | depguard 强制 |

**总结**：M1/M2/M4/M5/M6 完全达成，M3 达成 70%（切帧层统一，流控制分开因历史原因）。

---

### 2.3 97_ 接口设计落地情况

| 接口 | 设计位置 | 落地情况 | 原因 |
|---|---|---|---|
| DuplexTransport | 97_ §一 | ❌ 未实现 | YAGNI：只有一个 vendor |
| TransportEvent | 97_ §一 | ❌ 未实现 | 用 ProviderOutbound |
| TurnTimeouts | 97_ §二 | ❌ 未实现 | 超时分散多处 |
| UtteranceSink | 97_ §三 | ❌ 未实现 | 直接 beginUtterance |
| TurnObserver | 97_ §四 | ❌ 未实现 | 特性层直接调用 |

**结论**：97_ 的接口设计大部分未落地，当前使用更简单的接缝（VoiceProvider）。

**是否需要补全**：不需要，等真实需求驱动（有第二个 vendor 时再做）。

---

## 三、测试覆盖与质量保证

### 3.1 测试统计

| 类型 | 数量 | 状态 |
|---|---|---|
| 单元测试 | 208 个 | ✅ 全绿 |
| 契约测试 | 15+ 个 | ✅ 全绿 |
| Schema 冻结测试 | 2 个 | ✅ 全绿 |
| 真机验证（101_） | 12 项 | ✅ 全通过 |

**结论**：测试覆盖充分，质量有保证。

---

### 3.2 M1-M6 改动验证

**停手条件（98_ §三）**：
- ✅ 无测试需要改断言（行为未变）
- ✅ `TestSchemaV1BytesAreFrozen` 保持绿色（协议未动）
- ✅ 新增测试在"改之前也是绿的"不成立（测试真测到东西）

**实际结果**：
- M1-M6 每步提交后所有测试绿色
- 无需改断言（结构重构，行为保持）
- 真机验证通过（按 101_ 清单）

---

## 四、架构评估（5 个维度）

### 4.1 功能正确性：✅ A 级（95/100）

- ✅ 所有测试绿色
- ✅ 真机验证通过
- ⚠️ turn_id 双重回落（功能正确但语义不一致）

---

### 4.2 可维护性：✅ B+ 级（85/100）

**优点**：
- ✅ 状态机管理生命周期
- ✅ 失败策略表化
- ✅ 切帧层统一
- ✅ 包职责清晰（voiceduplex / voicegateway）

**缺点**：
- ⚠️ provider 状态与 handler 部分重叠
- ⚠️ 上行常量重复
- ⚠️ 超时值分散

---

### 4.3 可测试性：✅ A 级（90/100）

- ✅ 208 个单元测试
- ✅ 契约测试覆盖跨包调用
- ✅ 时钟可注入（SeqAllocator / streamWriter.pace）
- ✅ 失败策略可断言（ProviderErrorPolicies）

---

### 4.4 可扩展性：⚠️ B 级（75/100）

**受限部分**：
- ⚠️ 无传输层抽象（vendor 锁定）
- ⚠️ 无统一观测点（TurnObserver）

**不受限部分**：
- ✅ 可添加新特性（Rescue / Badge 已证明）
- ✅ 可独立测试每个 handler

**结论**：当前需求够用，未来扩展有路径（97_ 设计在那）。

---

### 4.5 可理解性：✅ B+ 级（85/100）

**优点**：
- ✅ 状态机定义清晰（turnTransitions 白名单）
- ✅ 策略表可读（providerErrorStrategies）
- ✅ 注释充分（每个关键决定都有注释）
- ✅ 文档齐全（94-99 系列 + 本审计）

**缺点**：
- ⚠️ provider 状态职责未文档化
- ⚠️ 新人需要理解"为什么停在 M3"

---

## 五、与业界最佳实践对比

### 5.1 符合的实践

| 实践 | 体现 |
|---|---|
| **单一职责** | Turn 管生命周期，SeqAllocator 管序号，audioFramer 管切帧 |
| **接口隔离** | VoiceProvider 接口清晰，不依赖实现 |
| **测试驱动** | 208 个测试，真机坑都有回归测试 |
| **失败优先** | 策略表明确了"何时告知客户端" |
| **不可变协议** | Schema v1 冻结测试 |

---

### 5.2 可改进的地方

| 实践 | 当前情况 | 改进方向 |
|---|---|---|
| **依赖倒置** | 直接依赖 VoiceProvider | 抽象 DuplexTransport（P2） |
| **观察者模式** | 特性层直接调用 | TurnObserver（P2） |
| **配置集中** | 超时值分散 | TurnTimeouts（P2） |

**结论**：不完美，但不影响当前功能。优化点明确，有清晰路径。

---

## 六、最终建议

### 6.1 立即行动（本周）

**🔴 P0：修复 turn_id 双重回落**
- 工时：2-3 小时
- 风险：低
- 收益：高（避免客户端帧分组错误）
- 见：[05_next_steps.md](./05_next_steps.md) §一

**🟡 P1：统一上行音频块常量**
- 工时：1-2 小时
- 风险：极低
- 收益：中（可维护性）
- 见：[05_next_steps.md](./05_next_steps.md) §二

---

### 6.2 规划但不紧急（P2）

**文档化 provider 状态边界**
- 在代码注释中说明各自职责
- 在开发文档中记录边界
- 工时：2 小时

**接口抽象层（DuplexTransport）**
- 等真实需求（第二个 vendor）
- 预估工时：2-3 周

---

### 6.3 不做的事（明确范围）

❌ **provider 状态强行合并**
- 理由：当前形态可接受，改动风险高

❌ **完整实施 97_ 接口设计**
- 理由：YAGNI，等真实需求驱动

❌ **AI 音频走 UtteranceWriter**
- 理由：流式 vs 批处理特性不同，当前分开合理

---

## 七、总结

### 架构状态：**B+ 级（良好，可生产）**

**核心成就**：
- ✅ 解决了 5/9 个真机踩过的结构问题
- ✅ 所有测试绿色，真机验证通过
- ✅ 文档齐全，可追溯

**待完成工作**：
- 🔴 1 个 P0（turn_id 统一，2-3 小时）
- 🟡 1 个 P1（常量统一，1-2 小时）
- 🟢 2 个 P2（文档化，非紧急）

**结论**：
当前架构**可以继续使用**，无需大规模重构。
建议按 05_next_steps.md 的计划修复 P0/P1，P2 问题文档化即可。

---

## 附录：文档索引

本审计系列文档：
- [README.md](./README.md) - 概览
- [01_implementation_status.md](./01_implementation_status.md) - M1-M6 落地状态
- [02_remaining_gaps.md](./02_remaining_gaps.md) - 剩余问题分析
- [03_turn_id_audit.md](./03_turn_id_audit.md) - turn_id 专项审计
- [04_binary_framing_analysis.md](./04_binary_framing_analysis.md) - 二进制帧编码分析
- [05_next_steps.md](./05_next_steps.md) - 调整计划（推荐从这里开始）
- **06_final_assessment.md（本文）** - 最终评估

原始文档：
- docs/94_ - 审核报告（问题清单）
- docs/95_ - 链路图
- docs/96-98_ - 重构设计系列
- docs/99_ - 设计纪律
- docs/101_ - 真机验证清单
