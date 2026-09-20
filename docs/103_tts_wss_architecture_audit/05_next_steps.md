# TTS WSS 架构调整计划

**日期**：2026-09-21  
**目标**：完成重构目标，解决剩余架构问题

---

## 一、P0 优先级（立即执行）

### 任务 1.1：统一 turn_id 回落规则

**问题**：handler 回落 `session_id`，provider 回落 `turn-<seq>`，导致同一 turn 在不同帧上显示不同 id

**目标**：所有帧使用统一的 turn_id

**方案**：修改 `canonicalTurnID` 回落规则，从 `turn-<seq>` 改为 `session_id`

**改动清单**：

1. **修改 `turn_id.go`**
```go
// 改前
func canonicalTurnID(clientID string, seq int) string {
    if t := strings.TrimSpace(clientID); t != "" {
        return t
    }
    if seq <= 0 {
        return ""
    }
    return fmt.Sprintf("turn-%d", seq)  // 旧：回落序号
}

// 改后
func canonicalTurnID(clientID string, sessionID string) string {
    if t := strings.TrimSpace(clientID); t != "" {
        return t
    }
    return sessionID  // 新：回落 session_id
}
```

2. **修改 `provider_volc_duplex.go` 调用点**（6 处）
   - L244: `AssistantASRText` 中的 `canonicalTurnID(s.activeTurnID, s.nextSeq)` → `canonicalTurnID(s.activeTurnID, s.session.SessionID)`
   - L278: `AssistantTextDelta` 中
   - L347: `AssistantAudio` 中
   - L383: `ttsStartFrame` 中
   - L434: `markFirstAudio` 日志中
   - L966: `turnToOutbound` 中

3. **修改 `provider_dev_echo.go` 调用点**（2 处）
   - L188: `HandleClientControl` 中
   - L191: 同上

4. **更新 `turn_id_test.go`**
```go
func TestCanonicalTurnIDPrefersClient(t *testing.T) {
    if got := canonicalTurnID(" turn-7 ", "session-1"); got != "turn-7" {
        t.Errorf("got %q, want turn-7", got)
    }
}

func TestCanonicalTurnIDFallsBackToSession(t *testing.T) {
    if got := canonicalTurnID("", "session-1"); got != "session-1" {
        t.Errorf("got %q, want session-1", got)
    }
}
```

5. **在 `provider_volc_duplex.go` 增加 activeTurnID 回落**
```go
// HandleClientControl 的 user.speech.end 分支
case voiceproto.TypeUserSpeechEnd:
    var end voiceproto.UserSpeechEnd
    if err := json.Unmarshal(data, &end); err != nil {
        return nil, err
    }
    if !s.turnStarted.IsZero() {
        s.turnStarted = time.Now()
    }
    if t := strings.TrimSpace(end.TurnID); t != "" {
        s.activeTurnID = t
    } else {
        s.activeTurnID = session.SessionID  // 新增：回落
    }
    s.session.SetClientTurnID(s.activeTurnID)
```

**测试验证**：
- [ ] `TestCanonicalTurnID*` 全绿
- [ ] 集成测试：客户端不提供 turn_id 时，badge、ai.text.delta、ai.tts.start、ai.turn.end 的 turn_id 都相同
- [ ] 现有 208 个测试全绿

**工时估算**：2-3 小时

**风险**：低（只改回落值，不改调用关系）

---

## 二、P1 优先级（本周完成）

### 任务 2.1：统一上行音频块常量

**问题**：`pcm16kChunkBytes` 和 `devEchoChunkBytes` 都是 640，含义相同但分散

**目标**：单一常量定义，推导自采样率

**方案**：在 `utterance.go` 增加上行块常量

**改动清单**：

1. **在 `utterance.go` 增加常量**
```go
// UplinkChunkMS is how long one uplink audio chunk lasts: the gateway forwards
// client→provider audio in 20ms pieces, regardless of the vendor's own chunking.
const UplinkChunkMS = 20

// UplinkChunkBytes is how much 20ms of client audio occupies.
// The client captures at ClientAudioFormat (16kHz mono s16le).
var UplinkChunkBytes = ClientAudioFormat.FrameBytes(UplinkChunkMS)
```

2. **删除 `voiceduplex/volc_duplex.go:57`**
```go
// 删除这行
const pcm16kChunkBytes = 640
```

3. **替换 `voiceduplex/volc_duplex.go` 中所有 `pcm16kChunkBytes`**（5 处）
   - L314, L318, L319, L806, L956 → 改为 `voicegateway.UplinkChunkBytes`

4. **删除 `provider_dev_echo.go:106`**
```go
// 删除这行
const devEchoChunkBytes = 640
```

5. **替换 `provider_dev_echo.go` 中所有 `devEchoChunkBytes`**
   - 改为 `UplinkChunkBytes`

6. **更新 `utterance_test.go`**
```go
func TestUplinkChunkBytesMatchesFormat(t *testing.T) {
    want := ClientAudioFormat.FrameBytes(UplinkChunkMS)
    if UplinkChunkBytes != want {
        t.Errorf("UplinkChunkBytes = %d, want %d", UplinkChunkBytes, want)
    }
    if UplinkChunkBytes != 640 {
        t.Errorf("UplinkChunkBytes = %d, want 640 (20ms @ 16kHz)", UplinkChunkBytes)
    }
}
```

**测试验证**：
- [ ] `TestUplinkChunkBytesMatchesFormat` 通过
- [ ] `TestAudioSizesAreDerivedFromTheSampleRate` 通过
- [ ] voiceduplex 的测试全绿

**工时估算**：1-2 小时

**风险**：极低（纯重构，不改行为）

---

## 三、P2 优先级（规划但不紧急）

### 任务 3.1：provider 状态整合

**问题**：provider 维护 `activeTurnID` / `turnStarted` / `ttsStarted` / `collectingTurn`，与 handler 的 `Turn` 状态机部分重叠

**目标**：明确职责边界，减少状态重复

**方案选项**：

**选项 A：保持现状，文档化边界**
- provider 的状态用于内部时序控制（延迟计算、重复发送防护）
- handler 的 Turn 状态机用于外部可见的生命周期
- 在代码注释中明确各自职责

**选项 B：Turn 状态机持有 provider 需要的时间戳**
- `Turn` 增加 `startedAt` / `firstAudioAt` 字段
- provider 从 Turn 读取，不再自己记录
- 需要 Turn 提供查询接口

**选项 C：provider 完全无状态化**
- 所有 turn 状态由 handler 管理
- provider 每次调用都显式传入需要的信息
- 需要重新设计 provider 接口

**推荐**：**选项 A**（当前阶段）

**理由**：
1. provider 的状态有明确用途（延迟计算、防重发）
2. 这些状态不影响外部行为（测试全绿、真机通过）
3. 强行合并的收益不明显，但改动风险高

**何时重新评估**：
- 当有第二个 vendor 需要实现同样的状态管理时
- 当 turn 相关 bug 确实因"状态分散"导致时

**文档化工作**：
- 在 `turn.go` 顶部注释说明 handler 侧职责
- 在 `provider_volc_duplex.go` 顶部注释说明 provider 侧状态用途
- 在 `AGENTS.md` 或开发文档中记录边界

**工时估算**：文档化 2 小时 / 实施选项 B 需 1-2 周

---

### 任务 3.2：接口抽象层（DuplexTransport）

**问题**：97_ 设计的接口未落地，当前直接依赖 `VoiceProvider`

**目标**：抽象传输层，支持 vendor 替换

**前置条件**：
1. 有第二个 vendor 需要接入
2. 明确 vendor 切换的业务需求
3. 确定接口边界（不只是设计推演）

**为什么不立即做**：
1. **YAGNI 原则**：当前只有一个 vendor（Volcano），抽象层会是"为一个实现写的接口"
2. **98_ §五的教训**：M3 设计了 Utterance 接口但"给 AI 发 ai.tts.start 会静音"，设计假设与现实不符
3. **成本高收益低**：完整接口层需要 2-3 周，但当前无扩展需求

**何时做**：
- 业务确认需要接入第二个 vendor（如 Azure / AWS）
- 需要支持 A/B 测试不同 vendor
- 需要 mock 整个传输层做集成测试

**预估工时**：2-3 周（设计 + 实施 + 测试）

---

## 四、实施时间表

### 第 1 周（本周）

**Mon-Tue**：P0 任务 1.1（turn_id 统一）
- 修改 `canonicalTurnID` 签名与实现
- 更新所有调用点
- 测试验证

**Wed-Thu**：P1 任务 2.1（上行块常量统一）
- 抽取 `UplinkChunkBytes`
- 替换所有使用点
- 测试验证

**Fri**：代码审查 + 真机验证
- PR review
- 真机测试（按 101_ 清单）

### 第 2 周

**Mon-Wed**：P2 任务 3.1（文档化边界）
- 补充 turn 状态职责注释
- 更新开发文档
- 团队同步

**Thu-Fri**：总结 + 文档归档
- 更新本系列文档
- 记录实施经验
- 关闭相关 issue

---

## 五、验收标准

### P0（必须达成）

- [ ] 所有帧的 turn_id 使用统一回落规则
- [ ] 客户端不提供 turn_id 时，badge、ai.text.delta、ai.tts.start、ai.turn.end 的 turn_id 都是 `session_id`
- [ ] 客户端提供 turn_id 时，所有帧使用客户端值
- [ ] 现有 208 个测试全绿
- [ ] 真机验证通过（101_ 清单）

### P1（应该达成）

- [ ] 上行块常量统一为 `UplinkChunkBytes`
- [ ] `pcm16kChunkBytes` 和 `devEchoChunkBytes` 删除
- [ ] 测试验证块大小与采样率的关系

### P2（文档化）

- [ ] Turn 状态机职责边界文档化
- [ ] provider 状态用途注释清晰
- [ ] 开发文档更新

---

## 六、风险评估与缓解

### 风险 1：turn_id 改动影响现有逻辑

**可能性**：低  
**影响**：高（如果出错会导致 iOS 无法关联帧）

**缓解措施**：
1. 先在测试环境验证
2. 增加集成测试覆盖所有帧类型
3. 真机验证所有关键路径（对话、打断、救援）
4. 保留回滚方案（单独 commit，易于 revert）

### 风险 2：上行块常量改动影响音频质量

**可能性**：极低  
**影响**：高（如果出错会导致音频失真）

**缓解措施**：
1. 值不变（仍然是 640），只是来源改变
2. 测试验证值推导正确
3. 真机验证音频质量

### 风险 3：文档过时

**可能性**：中  
**影响**：低（不影响功能）

**缓解措施**：
1. 每次改动同步更新本系列文档
2. 在代码注释中引用文档位置
3. PR review 时检查文档一致性

---

## 七、成功标准

### 短期（1-2 周）

- ✅ P0 和 P1 任务全部完成
- ✅ 所有测试绿色
- ✅ 真机验证通过
- ✅ 代码审查通过

### 中期（1 个月）

- ✅ 运行稳定，无 turn_id 相关 bug
- ✅ 文档与代码一致
- ✅ 团队理解架构边界

### 长期（3-6 个月）

- ✅ 技术债务可控（P2 任务明确但不紧急）
- ✅ 如有第二个 vendor 需求，有清晰的扩展路径
- ✅ 架构文档成为后续开发的参考基准

---

## 八、总结

### 当前架构状态：**B 级（可用，有优化空间）**

**核心成就（M1-M6）**：
- ✅ 序号管理（SeqAllocator）
- ✅ 切帧统一（audioFramer）
- ✅ 状态机（Turn）
- ✅ 失败策略表（providerErrorStrategies）
- ✅ 包拆分（voiceduplex）

**待完成（本计划）**：
- 🔴 P0：turn_id 回落统一（影响客户端）
- 🟡 P1：上行块常量统一（可维护性）
- 🟢 P2：状态边界文档化（可理解性）

### 实施原则

1. **止血优先**：P0 解决实际问题（turn_id 不一致）
2. **成本收益**：P1 改动小收益明确，P2 改动大需求不明
3. **测试护航**：每步都有测试验证，保持 208 个测试绿色
4. **真机验证**：按 101_ 清单验证关键路径
5. **文档同步**：代码改动同步更新本系列文档

### 后续方向

**不做的事（明确不在范围）**：
- ❌ 完整接口抽象（DuplexTransport）：等真实需求
- ❌ provider 状态合并：当前形态可接受
- ❌ 客户端协议变更：v1 冻结

**可能做的事（视需求）**：
- ⏸️ 第二个 vendor 接入时再抽象接口
- ⏸️ turn 状态问题真实发生时再整合
- ⏸️ 新功能需要时再扩展架构
