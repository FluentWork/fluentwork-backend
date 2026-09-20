# 下一步行动计划

**日期**: 2026-09-21  
**前提**: M1-M6 已全部落地，P0/P1 修复已完成

---

## 一、立即行动（本周）

### 1. iOS 真机验证 🔴 高优先级

**目标**: 确认 turn_id 统一后所有帧正确分组

**验证清单**:
- [ ] Badge 帧（user.badge.ready 等）使用正确 turn_id
- [ ] ai.text.delta 与 ai.turn.end 的 turn_id 一致
- [ ] 客户端未提供 turn_id 时，所有帧回落到 session_id
- [ ] 打断场景下 turn_id 不会跳变
- [ ] 救援梯子的 turn_id 正确标记为 ladder

**参考文档**: docs/101_重构后真机验证清单_2026-09-18.md

**负责人**: iOS + Backend 联合验证

**时间**: 1-2 天

---

### 2. 监控指标观察 🟡 中优先级

**目标**: 通过线上数据确认修复生效

**观察指标**:

1. **turn_id 分布**
   ```
   # 期望：不再出现 turn-N 格式
   SELECT turn_id, COUNT(*) 
   FROM ai_turn_end_events 
   WHERE created_at > '2026-09-21'
   GROUP BY turn_id
   ```

2. **rejectedTransitions 计数器**
   ```
   # 观察非法状态迁移频率
   rate(rejected_transitions_total[5m])
   ```

3. **audio_sequence 连续性**
   ```
   # 确认重开后序号不回退
   SELECT session_id, MIN(seq), MAX(seq), COUNT(DISTINCT seq)
   FROM audio_frames
   GROUP BY session_id
   HAVING COUNT(DISTINCT seq) != (MAX(seq) - MIN(seq) + 1)
   ```

**负责人**: Backend + 数据团队

**时间**: 持续 1 周观察

---

## 二、短期改进（两周内）

### 3. 补充集成测试 🟡 中优先级

**背景**: docs/98 明确指出 M1-M6 无法用单测验证真机行为

**任务**:

1. **turn_id 一致性测试**
   ```go
   func TestTurnIDConsistencyAcrossFrames(t *testing.T) {
       // 驱动真实 handler
       // 验证一个 turn 内所有帧的 turn_id 相同
   }
   ```

2. **重开后序号连续性测试**
   ```go
   func TestAudioSequenceContinuesAfterReconnect(t *testing.T) {
       // 模拟透明重开
       // 验证新 provider 的序号 > 旧 provider 最后序号
   }
   ```

3. **救援梯子分流测试**
   ```go
   func TestLadderAudioDoesNotTriggerSilenceDetector(t *testing.T) {
       // 验证 Utterance.Kind = Ladder 的音频不走 AI 记账路径
   }
   ```

**位置**: `internal/voicegateway/*_integration_test.go`

**负责人**: Backend

**时间**: 3-4 天

---

### 4. 文档整理与归档 🟢 低优先级

**任务**:

1. **更新主 README**
   - 链接到 docs/103_tts_wss_architecture_audit/
   - 说明重构完成状态

2. **创建架构决策记录（ADR）**
   - ADR: 为什么 provider 侧保留独立 turn 状态
   - ADR: 为什么流控制不统一到 UtteranceWriter
   - ADR: 为什么不实现 DuplexTransport 抽象层

3. **归档审核文档**
   - 将 docs/103 标记为已完成
   - 添加"重构已完成"标签

**负责人**: Backend Lead

**时间**: 1 天

---

## 三、中期优化（一个月内，可选）

### 5. 性能优化与资源管理 🟢 低优先级

**潜在改进点**:

1. **音频缓冲池化**
   - 当前每次分配新切片，可考虑 sync.Pool
   - 评估 GC 压力是否值得优化

2. **序号分配器优化**
   - 当前 SeqAllocator 每次取锁，可考虑 atomic
   - 基准测试确认是否有瓶颈

3. **状态机锁粒度**
   - Turn 状态机当前粗粒度锁，可考虑细化
   - 需要压测确认是否必要

**前置条件**: 压测发现性能瓶颈

**负责人**: Backend

**时间**: 待定

---

### 6. 完善可观测性 🟡 中优先级

**任务**:

1. **turn_id 来源标记** (docs/98 防御 D2)
   ```go
   // 记录 turn_id 是客户端给的还是回落的
   logger.Info("turn started",
       "turn_id", turnID,
       "turn_id_source", source, // "client" or "session"
   )
   ```

2. **音频字节数核对** (docs/98 防御 D3)
   ```go
   // Utterance 结束时记录
   metrics.RecordAudioBytes(
       "audio_in_bytes", vendorBytes,
       "audio_out_bytes", sentBytes,
       "frames", frameCount,
   )
   ```

3. **状态转换追踪**
   ```go
   // 每次状态迁移记录
   turnStateTransitions.WithLabelValues(from, to, event).Inc()
   ```

**负责人**: Backend + SRE

**时间**: 2-3 天

---

## 四、长期规划（季度级，按需）

### 7. 多 Provider 支持准备 🟢 低优先级

**触发条件**: 需要接入第二个 TTS 供应商

**任务**:

1. **抽象 DuplexTransport 接口**
   - 实现 docs/97 中设计的 `DuplexTransport`
   - 将 volcDuplex 重构为该接口的实现

2. **Provider 路由层**
   - 根据配置选择 provider
   - 支持 A/B 测试和灰度

3. **统一错误码映射**
   - 各 provider 的错误码统一映射
   - 客户端只看到标准错误

**前置条件**: 产品决策接入新 provider

**负责人**: Backend

**时间**: 2 周

---

### 8. 流控制统一（M3 的完整版）🟢 低优先级

**触发条件**: iOS 侧 Opus 解码器（ADR-0073）落地

**任务**:

1. **AI 音频也走 UtteranceWriter**
   - provider 通过 `beginUtterance()` 发起
   - 自动发送 `ai.tts.start` / `ai.tts.end`

2. **删除 provider 内部的切帧逻辑**
   - `provider_volc_duplex` 只负责收音频
   - 所有切帧、编号、发送走统一路径

3. **真机验证**
   - 确认不会静音（docs/98 §五的前置已解除）
   - 打断、barge-in 场景完整测试

**前置条件**: iOS 删除 legacy 回退路径（已完成）+ Opus 解码器稳定

**负责人**: Backend + iOS

**时间**: 1 周

---

## 五、不做的事情（明确放弃）

基于 docs/96 §5 非目标清单：

| 不做什么 | 为什么 |
|---------|--------|
| 分布式会话状态 | 单进程够用，引入外部存储是过度设计 |
| 消息队列 | 进程内 channel 足够，没有跨进程异步需求 |
| 插件框架 | 只有 2 个特性（救援、徽章），不值得加抽象层 |
| 通用实时音频框架 | 只有 1 个业务场景，过早通用化是浪费 |
| 改 vendor WebSocket | 供应商协议不动 |
| 改对 iOS 的帧格式 | 协议冻结（`TestSchemaV1BytesAreFrozen`） |

---

## 六、决策检查点

每项任务开始前，问这些问题：

1. **这是真机踩过的坑吗？** 
   - 是 → 高优先级
   - 否 → 需要更强理由

2. **现有测试是否足够？**
   - 否 → 先补测试
   - 是 → 继续

3. **会改变协议吗？**
   - 是 → 停手，重新评估
   - 否 → 继续

4. **单测能覆盖吗？**
   - 否 → 必须真机验证
   - 是 → 继续

5. **有没有更简单的方案？**
   - 有 → 先用简单方案
   - 没有 → 继续

---

## 七、风险管理

### 高风险操作

| 操作 | 风险 | 缓解措施 |
|------|------|----------|
| 修改主音频路径 | 可能静音 | 真机验证 + 灰度发布 |
| 改变状态转换逻辑 | 可能卡死 | 充分集成测试 + 监控 |
| 修改 turn_id 生成 | 可能打乱客户端分组 | iOS 联合验证 |

### 回滚准备

- 每个改动独立提交
- 关键路径保留特性开关
- 监控报警就绪

---

## 八、成功标准

### 功能正确性

- ✅ 所有现有测试通过
- ✅ iOS 真机验证通过
- ✅ 线上监控无异常

### 代码质量

- ✅ 无重复常量
- ✅ 策略显式化
- ✅ 边界清晰

### 可维护性

- ✅ 新增帧类型只需改一处
- ✅ 采样率变更自动推导
- ✅ 架构决策有文档

---

## 总结

**立即行动**（本周）:
1. 🔴 iOS 真机验证
2. 🟡 监控指标观察

**短期改进**（两周）:
3. 🟡 补充集成测试
4. 🟢 文档整理

**其余全部按需触发**，不提前做。

重构的目标已达成：把真机踩过的坑变成结构性防御。现在的任务是**验证**，不是继续改。
