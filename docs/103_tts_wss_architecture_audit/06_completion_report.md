# TTS WSS 架构审核修复完成报告

**日期**: 2026-09-21  
**范围**: P0 turn_id 统一 + P1 常量提取

---

## 执行摘要

按照 `05_next_steps.md` 修复计划完成 P0 和 P1 任务，所有测试通过。

- ✅ **P0**: turn_id 回退规则统一（handler 侧与 provider 侧一致）
- ✅ **P1**: UplinkChunkBytes 常量提取（消除重复定义）
- ✅ **测试覆盖**: 新增 3 个测试用例，更新 1 个现有测试

---

## P0: turn_id 回退规则统一

### 问题描述

Handler 侧与 provider 侧的 turn_id 回退规则不一致：

- **Handler 侧** (`turn.go:246-250`): `clientID → session_id`
- **Provider 侧** (`turn_id.go`): `clientID → turn-<seq>`

当客户端未提供 turn_id 时，badge 类帧使用 `session_id`，但 `ai.text.delta`/`ai.tts.start` 使用 `turn-N`，导致 iOS 无法正确分组帧。

### 实施方案

1. **修改 `canonicalTurnID` 签名**:
   ```go
   // Before: func canonicalTurnID(clientID string, seq int) string
   // After:  func canonicalTurnID(clientID string, sessionID string) string
   ```

2. **更新回退逻辑**:
   ```go
   func canonicalTurnID(clientID string, sessionID string) string {
       if t := strings.TrimSpace(clientID); t != "" {
           return t
       }
       return sessionID  // 改为使用 sessionID，与 handler 侧一致
   }
   ```

3. **更新调用点** (`provider_volc_duplex.go` 5处):
   - L245: `UserTranscript` badge 发射
   - L270: `AssistantTextDelta` 发射  
   - L339: `AssistantAudio` 发射
   - L422: `markFirstAudio` 发射
   - L954: `turnToOutbound` 最终确认

4. **添加 sessionID 字段**:
   ```go
   type volcDuplexProviderSession struct {
       // ... existing fields ...
       sessionID   string  // 新增字段，用于 turn_id 回退
   }
   ```

### 测试覆盖

- ✅ `TestCanonicalTurnIDPrefersClient`: 验证优先使用 clientID
- ✅ `TestCanonicalTurnIDFallsBackToSession`: 验证回退到 sessionID
- ✅ `TestTurnToOutboundFallbackUsesTurnNNotVolcPrefix`: 更新为验证新回退行为

---

## P1: UplinkChunkBytes 常量提取

### 问题描述

Uplink 音频块大小常量重复定义：
- `voiceduplex/volc_duplex.go:57`: `pcm16kChunkBytes = 640`
- `provider_dev_echo.go:106`: `devEchoChunkBytes = 640`

### 实施方案

1. **创建统一常量** (`uplink_constants.go`):
   ```go
   const UplinkChunkBytes = 640
   ```

2. **处理导入循环问题**:
   - voiceduplex 依赖 voicegateway 会形成循环导入
   - 解决方案：在 voiceduplex 内部定义私有常量 `uplinkChunkBytes = 640` 并注释说明来源

3. **替换使用点**:
   - `voiceduplex/volc_duplex.go`: 4 处替换为 `uplinkChunkBytes`
   - `provider_dev_echo.go`: 3 处替换为 `voicegateway.UplinkChunkBytes`

4. **文档说明**:
   ```go
   // uplinkChunkBytes is 20ms of 16 kHz mono s16le (640 bytes).
   // Copied here from voicegateway.UplinkChunkBytes to avoid import cycle.
   const uplinkChunkBytes = 640
   ```

### 测试覆盖

- ✅ `TestUplinkChunkBytesDerivedFromClientFormat`: 验证常量与 `ClientAudioFormat.FrameBytes(20)` 一致

---

## 测试结果

```bash
# P0 测试
$ go test ./internal/voicegateway/... -run TestCanonicalTurnID
ok   github.com/FluentWork/fluentwork-backend/internal/voicegateway  0.264s

# P1 测试  
$ go test ./internal/voicegateway/... -run TestUplinkChunkBytes
ok   github.com/FluentWork/fluentwork-backend/internal/voicegateway  0.605s

# 完整测试套件
$ go test ./internal/voicegateway/...
ok   github.com/FluentWork/fluentwork-backend/internal/voicegateway  2.681s
```

所有测试通过，无编译错误。

---

## 文件变更清单

### 新增文件
- `internal/voicegateway/uplink_constants.go` - UplinkChunkBytes 常量定义
- `internal/voicegateway/uplink_constants_test.go` - 常量派生测试
- `docs/103_tts_wss_architecture_audit/06_completion_report.md` - 本文档

### 修改文件
- `internal/voicegateway/turn_id.go` - canonicalTurnID 签名与实现
- `internal/voicegateway/turn_id_test.go` - 测试用例更新
- `internal/voicegateway/provider_volc_duplex.go` - 5 处 canonicalTurnID 调用点
- `internal/voicegateway/provider_dev_echo.go` - 常量替换
- `internal/voicegateway/provider_volc_duplex_internal_test.go` - 测试用例修正
- `internal/voiceduplex/volc_duplex.go` - 私有常量定义与使用

---

## 后续行动

### 已完成
- ✅ P0: turn_id 回退规则统一
- ✅ P1: UplinkChunkBytes 常量提取  
- ✅ 测试覆盖补充

### P2 (文档类，无代码改动)
- M1-M6 实施状态已记录在 `02_milestone_status.md`
- F1-F9 发现项已记录在 `03_findings_assessment.md`
- 无需进一步代码改动

### 建议
1. **iOS 真机验证**: 确认 turn_id 统一后帧分组正常
2. **监控指标**: 观察 `ai.turn.end.turn_id` 分布，确认不再出现 `turn-N` 格式
3. **文档归档**: 将审核文档链接到主 README 或架构文档索引

---

## 总结

通过此次修复：
1. **消除了架构不一致性**: handler 侧与 provider 侧 turn_id 回退规则现已统一
2. **提升了可维护性**: 音频块大小常量集中定义，避免采样率变更时遗漏更新
3. **增强了测试覆盖**: 新增测试锁定关键约束，防止回归

所有变更均通过测试验证，可安全合并。
