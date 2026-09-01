# B13 客户端 ASR 回灌 Backend 实现说明

**对应 ticket**：`B13` — 客户端 ASR 回灌（Backend gate 实现）

**前置文档**：`docs/21_B13_client_asr_回灌_kickoff.md`

**作者 / 完成日期**：Backend，2026-09-01

**状态**：✅ 已完成（Phase 1 骨架代码 + 测试覆盖）

---

## 一、实现范围

Backend B13 实现了 kickoff 文档中规定的**服务端 gate 逻辑**，确保当 `VOICE_CLIENT_ASR_REQUIRED` 环境变量启用时，空 `user.speech.end.text` 会被拒绝。

### 1.1 已实现

1. ✅ `Handler.clientASRRequired` 字段 + `SetClientASRRequired()` setter
2. ✅ `user.speech.end` gate 逻辑：当 `clientASRRequired=true` 且 `end.Text` 为空（含 whitespace-only）→ 返回 `error` 帧
3. ✅ `cmd/voice-gateway/main.go` 环境变量绑定（`VOICE_CLIENT_ASR_REQUIRED=1|true`）
4. ✅ 4 个测试用例覆盖所有分支：
   - `TestHandler_RejectsEmptyTextWhenClientASRRequired`
   - `TestHandler_AcceptsEmptyTextWhenClientASRNotRequired`
   - `TestHandler_AcceptsPopulatedTextWhenClientASRRequired`
   - `TestHandler_ClientASRRequiredWhitespaceOnlyCountsAsEmpty`

### 1.2 未实现（iOS 侧责任）

- 客户端 ASR 引擎（Volcengine / Apple Speech）
- 埋点事件（`speech_client_asr_completed/skipped/failed`）
- 客户端超时逻辑（800ms）

---

## 二、代码改动

### 2.1 `internal/voicegateway/handler.go`

新增字段：

```go
type Handler struct {
    // ... existing fields ...
    clientASRRequired bool // B13: gate user.speech.end with empty text when true
}
```

新增 setter：

```go
// SetClientASRRequired enables B13 gate: when true, user.speech.end with empty
// text returns an error frame (code: client_asr_required).
func (h *Handler) SetClientASRRequired(required bool) {
    h.clientASRRequired = required
}
```

Gate 逻辑（在 `handleControl` 中 `user.speech.end` 分支）：

```go
// B13 gate: when client ASR is required and text is empty, reject early.
if frameType == voiceproto.TypeUserSpeechEnd && h.clientASRRequired {
    var end voiceproto.UserSpeechEnd
    if jsonErr := json.Unmarshal(data, &end); jsonErr == nil {
        if strings.TrimSpace(end.Text) == "" {
            return writeJSON(ctx, conn, voiceproto.ErrorFrame{
                Type:    voiceproto.TypeError,
                Code:    "client_asr_required",
                Message: "user.speech.end.text is required when VOICE_CLIENT_ASR_REQUIRED is enabled",
            })
        }
    }
}
```

**关键设计点**：

- Gate 在 **B12 hit detection 之前**执行，确保空文本不会触发 badge emit
- 使用 `strings.TrimSpace` 将 whitespace-only 视为空（与 kickoff 文档一致）
- 错误码 `client_asr_required` 与 schema 中已有 `error.code` 字段兼容

### 2.2 `cmd/voice-gateway/main.go`

环境变量绑定：

```go
// B13: enable client ASR gate when VOICE_CLIENT_ASR_REQUIRED is set.
if os.Getenv("VOICE_CLIENT_ASR_REQUIRED") == "1" || os.Getenv("VOICE_CLIENT_ASR_REQUIRED") == "true" {
    handler.SetClientASRRequired(true)
    logger.Info("B13 client ASR gate enabled", "VOICE_CLIENT_ASR_REQUIRED", "true")
}
```

**默认行为**：gate **关闭**（`clientASRRequired=false`），兼容 day-one iOS（不携带 `text` 字段）。

### 2.3 `internal/voicegateway/handler_test.go`

新增 4 个测试（全部 `PASS`）：

| 测试名 | 覆盖场景 |
|---|---|
| `TestHandler_RejectsEmptyTextWhenClientASRRequired` | gate 开启 + 空 text → 返回 `error.client_asr_required` |
| `TestHandler_AcceptsEmptyTextWhenClientASRNotRequired` | gate 关闭 + 空 text → 正常转发给 provider |
| `TestHandler_AcceptsPopulatedTextWhenClientASRRequired` | gate 开启 + 非空 text → 正常转发 |
| `TestHandler_ClientASRRequiredWhitespaceOnlyCountsAsEmpty` | gate 开启 + whitespace-only → 返回错误 |

---

## 三、错误帧格式

当 gate 触发时，返回的错误帧：

```json
{
  "type": "error",
  "code": "client_asr_required",
  "message": "user.speech.end.text is required when VOICE_CLIENT_ASR_REQUIRED is enabled"
}
```

- `code` 和 `message` 与 kickoff 文档中的 §2.3 一致
- 无需修改 `infra/schemas/transport/wss-control-frames-v1.json`（已有 `error.code` / `message` 字段）

---

## 四、Rollout 状态

当前处于 **Phase 1（骨架代码）**：

| 阶段 | Backend 状态 | iOS 状态 | 说明 |
|---|---|---|---|
| Phase 1 | ✅ 完成 | ⏳ 待实现 | Backend gate 逻辑 + 测试覆盖；iOS 尚未实现 client ASR |
| Phase 2 | ✅ 就绪 | ⏳ 待开启 | iOS FeatureFlag 默认 `true`；Backend gate 仍为 `false` |
| Phase 3 | ⏳ 待灰度 | ⏳ 待灰度 | 10% 用户开启 `VOICE_CLIENT_ASR_REQUIRED=true` |
| Phase 4 | ⏳ 待全量 | ⏳ 待全量 | 全量开启 gate |

**当前环境变量默认值**：`VOICE_CLIENT_ASR_REQUIRED` **未设置**（等价于 `false`），兼容 iOS day-one 行为。

---

## 五、验证计划

### 5.1 单元测试

✅ 已验证：

```bash
$ go test ./internal/voicegateway -run "TestHandler_.*ClientASR" -v
=== RUN   TestHandler_RejectsEmptyTextWhenClientASRRequired
--- PASS: TestHandler_RejectsEmptyTextWhenClientASRRequired (0.00s)
=== RUN   TestHandler_AcceptsEmptyTextWhenClientASRNotRequired
--- PASS: TestHandler_AcceptsEmptyTextWhenClientASRNotRequired (0.60s)
=== RUN   TestHandler_AcceptsPopulatedTextWhenClientASRRequired
--- PASS: TestHandler_AcceptsPopulatedTextWhenClientASRRequired (0.60s)
=== RUN   TestHandler_ClientASRRequiredWhitespaceOnlyCountsAsEmpty
--- PASS: TestHandler_ClientASRRequiredWhitespaceOnlyCountsAsEmpty (0.00s)
PASS
```

### 5.2 端到端（待 iOS I13 完成后）

Phase 2 验证清单（需 iOS 客户端配合）：

- [ ] iOS 发送 `user.speech.end` 携带 `text`（非空） → Backend 正常转发 → 收到 `feedback.badge`（如命中）
- [ ] iOS 发送 `user.speech.end` 不携带 `text` + Backend `VOICE_CLIENT_ASR_REQUIRED=false` → 正常转发
- [ ] iOS 发送 `user.speech.end` 不携带 `text` + Backend `VOICE_CLIENT_ASR_REQUIRED=true` → 收到 `error.client_asr_required`

---

## 六、文件清单

**新增**：

- `docs/22_B13_client_asr_回灌_实现说明.md`（本文档）

**修改**：

- `internal/voicegateway/handler.go`（+10 行：字段 + setter + gate 逻辑）
- `internal/voicegateway/handler_test.go`（+270 行：4 个新测试）
- `cmd/voice-gateway/main.go`（+5 行：环境变量绑定）

---

## 七、与 kickoff 文档的对应

| Kickoff §4.1 要求 | 实现位置 | 状态 |
|---|---|---|
| Handler 新增 `clientASRRequired` 字段 | `handler.go:58` | ✅ |
| Gate 逻辑（empty text → error frame） | `handler.go:374-385` | ✅ |
| Setter `SetClientASRRequired` | `handler.go:80-83` | ✅ |
| 环境变量 `VOICE_CLIENT_ASR_REQUIRED` 绑定 | `cmd/voice-gateway/main.go:56-59` | ✅ |
| 4 个测试用例 | `handler_test.go:602-889` | ✅ |

---

## 八、已知限制

1. **客户端 ASR 引擎未实现**：iOS 侧需补齐 `ClientASRTranscriber` 协议 + Volcengine SDK 接入（I13 ticket）
2. **埋点事件未发送**：`speech_client_asr_completed/skipped/failed` 需 iOS 端实现（不影响 Backend gate 逻辑）
3. **Schema 未同步到 infra 仓**：三个新埋点事件定义尚未添加到 `infra/schemas/events/speech-observability-events-v1.json`（可在 Phase 2 前补齐）

---

## 九、下一步（Phase 2 前置条件）

- [ ] iOS I13 实现：`ClientASRTranscriber` + 三种实现（Volcengine / Apple Speech / Raw）
- [ ] iOS 端测试：5 个新测试（见 kickoff §6.1）
- [ ] 跨端联调：iOS 发送带 `text` 的 `user.speech.end` → Backend 接受 → 验证 `feedback.badge` 仍工作
- [ ] 环境变量文档：在 `README.md` 或 deployment runbook 中记录 `VOICE_CLIENT_ASR_REQUIRED` 用途

---

## 十、参考

- `docs/21_B13_client_asr_回灌_kickoff.md`（设计来源）
- `fluentwork-ios/docs/11_I13_iOS_backend_wss联调_runbook.md`（iOS 侧对应 ticket）
- `internal/voiceproto/frames.go`（`UserSpeechEnd.Text` 字段已存在）
- `docs/20_B12_badge_emit_问题修复说明.md`（B12 hit detection 现状）
