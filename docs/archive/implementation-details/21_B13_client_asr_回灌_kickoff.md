# B13 客户端 ASR 回灌 实现说明（kickoff）

**对应 ticket**：`B13` — 客户端 ASR 回灌（iOS `user.speech.end.text` 由 `nil` 切换为客户端 ASR 转写结果）

**对应依赖**：
- 前置：`B11` daily reads（已完成）、`B12` BadgeEmitter（已完成，已知 `end.Text` 字段已存在）
- 上游：`B14` D3 server-side ASR（已完成，提供了 vendor-side 转写路径作为 fallback）
- 设计来源：`fluentwork-ios/docs/11_I13_iOS_backend_wss联调_runbook.md` §1 字段表

**作者 / 启动日期**：iOS + voice-gateway 双仓维护者，2026-09-01

**状态**：kickoff（本文档为 ticket 起点；后续每个落地步骤补 sub-section）

---

## 一、Ticket 范围

### 1.1 必须做

1. iOS 端在 `user.speech.end` 帧中从 `nil` 切到 `non-nil`，`Text` 字段携带客户端 ASR 转写结果
2. iOS 端引入 on-device ASR 管线（基于 Volcengine SDK 或 Speech Framework，由 B13 落定）
3. voice-gateway 当前已经支持 `end.Text`（见 `internal/voicegateway/handler.go` 374-385 行 + `internal/voiceproto/frames.go` UserSpeechEnd 定义），本次新增行为：当 `end.Text` 非空时**强制走客户端命中路径**，跳过 server-side ASR
4. 加环境变量 `VOICE_CLIENT_ASR_REQUIRED`（默认 `false`），用于灰度切换；`true` 时收到 `end.Text == ""` 直接返回 `client_asr_required` 错误帧
5. 加埋点 `speech_client_asr_completed` + `speech_client_asr_skipped`，用于监控客户端 ASR 实际启用率

### 1.2 不做（划清边界）

1. **不**改 vendor 选型决策（Volcengine vs Apple Speech）—— 这是另一个 ticket 的事，B13 接受占位
2. **不**改 phrase block 命中阈值（`HitDetector` 内部算法）
3. **不**改 dedupe key（仍 `session|turn|phrase_block`）
4. **不**删 server-side ASR（B14 D3 已 live），仅作为 fallback；客户端 ASR 是默认路径

### 1.3 关键决策（待产品确认）

| 决策项 | 当前倾向 | 备注 |
|---|---|---|
| 客户端 ASR 引擎选型 | Volcengine SDK（B14 已选）+ Apple Speech fallback | B13 不锁 |
| 客户端 ASR 是否强制启用 | 否，先做 capability-gated（`FeatureFlag.enableClientASR`） | 灰度 |
| Server-side ASR 何时退役 | B13 不动；B14+ 视命中率迁移 | 看 `speech_client_asr_skipped` 数据 |
| ASR 失败兜底 | 自动 fallback 到 server-side，`end.Text == ""` | day-one 不报 error |

---

## 二、wire-format 变化

### 2.1 当前

```json
{
  "type": "user.speech.end",
  "turn_id": "turn-1"
}
```

（iOS 当前 day-one，`text` 缺省省略）

### 2.2 B13 完成后

```json
{
  "type": "user.speech.end",
  "turn_id": "turn-1",
  "text": "我想练一下语速的稳定性"
}
```

schema 已经允许 `text`（`additionalProperties: false`，`text` 是 optional），**无需改 `infra/schemas/transport/wss-control-frames-v1.json`**。

### 2.3 失败帧

仅在 `VOICE_CLIENT_ASR_REQUIRED=true` 且 client 发了 `text == ""` / 缺省时返回：

```json
{
  "type": "error",
  "code": "client_asr_required",
  "message": "user.speech.end.text is required when VOICE_CLIENT_ASR_REQUIRED is enabled"
}
```

schema 已经在 `infra/schemas/transport/wss-control-frames-v1.json` 的 `$defs.error` 内有 `code` / `message` 字段，无需改 schema。

---

## 三、iOS 改动

### 3.1 新模块 / 类

1. `ClientASRTranscriber` 协议（位于 `Shared/FluentWorkAudio/ClientASRTranscriber.swift`）
   - `transcribe(pcm: AsyncStream<Data>) async throws -> String`
   - 实现类（具体选型由后续 ticket 决定）：
     - `VolcengineClientASRTranscriber`（B14 SDK 接入后启用）
     - `AppleSpeechClientASRTranscriber`（iOS 17+，本地 fallback）
     - `RawClientASRTranscriber`（debug 用，原样返回空字符串）

2. `AudioEngineProtocol` 扩展
   - 新增可选能力 `capabilities: Set<AudioEngineCapability>`，含 `.clientASR`
   - 已有 `PlaceholderAudioEngine` / `StubAudioEngine` 加 `.clientASR = false` 默认

3. `DefaultSpeechSessionClient` 扩展
   - `sendSpeechBoundary(started: Bool, turnID: String?, text: String?) async throws`
   - 新参数 `text`，iOS 端通过 `AudioEngine.events()` 捕获 `.clientASRFinal(text)` 后调用
   - 当 client ASR 不可用时，`text` 传 `nil`（保留 day-one 行为，不破坏旧路径）

### 3.2 状态机改动

`SpeechSessionMiddleware` 新增订阅 `.clientASRFinal`：

```text
vadSpeechEnd
  ↓
emit speech_turn_ended  ← 现有
  ↓
(clientASRFinal text)    ← B13 新增；无 ASR 能力则跳过
  ↓
sendSpeechBoundary(started: false, turnID: "turn-1", text: "...")
```

时序约束：`vadSpeechEnd` 与 `clientASRFinal` 必须在 `sendSpeechBoundary` 之前全部到达；若 ASR 超时（默认 800ms），仍以 `text: nil` 上行（避免阻塞 turn）。

### 3.3 埋点

| 事件 | source | 触发 | 关键字段 |
|---|---|---|---|
| `speech_client_asr_completed` | `ios` | client ASR 成功完成 | `turn_id`, `elapsed_ms`, `text_length` |
| `speech_client_asr_skipped` | `ios` | client ASR 不可用或超时 | `turn_id`, `reason: enum("not_available", "timeout", "engine_disabled")` |
| `speech_client_asr_failed` | `ios` | client ASR 引擎报错 | `turn_id`, `error_code`, `elapsed_ms` |

新增到 `infra/schemas/events/speech-observability-events-v1.json` 的 `$defs` 三个新事件定义。

---

## 四、Backend 改动

### 4.1 voice-gateway

`internal/voicegateway/handler.go` 当前逻辑：

```go
if frameType == voiceproto.TypeUserSpeechEnd && h.badgeEmitter != nil {
    var end voiceproto.UserSpeechEnd
    if jsonErr := json.Unmarshal(data, &end); jsonErr == nil {
        turnID := strings.TrimSpace(end.TurnID)
        if turnID == "" {
            turnID = session.SessionID
        }
        h.badgeEmitter.Emit(ctx, realBadgeConn{conn}, session.UserID, session.SessionID, turnID, end.Text)
    }
}
```

**B13 改动**：

```go
if frameType == voiceproto.TypeUserSpeechEnd && h.badgeEmitter != nil {
    var end voiceproto.UserSpeechEnd
    if jsonErr := json.Unmarshal(data, &end); jsonErr == nil {
        turnID := strings.TrimSpace(end.TurnID)
        if turnID == "" {
            turnID = session.SessionID
        }
        // B13 gate
        if h.clientASRRequired && strings.TrimSpace(end.Text) == "" {
            return writeJSON(ctx, conn, voiceproto.ErrorFrame{
                Type:    voiceproto.TypeError,
                Code:    "client_asr_required",
                Message: "user.speech.end.text is required when VOICE_CLIENT_ASR_REQUIRED is enabled",
            })
        }
        h.badgeEmitter.Emit(ctx, realBadgeConn{conn}, session.UserID, session.SessionID, turnID, end.Text)
    }
}
```

新增字段：

```go
type Handler struct {
    // ... existing ...
    clientASRRequired bool
}

func (h *Handler) SetClientASRRequired(required bool) {
    h.clientASRRequired = required
}
```

env 绑定（cmd/voice-gateway/main.go）：

```go
if os.Getenv("VOICE_CLIENT_ASR_REQUIRED") == "1" || os.Getenv("VOICE_CLIENT_ASR_REQUIRED") == "true" {
    handler.SetClientASRRequired(true)
}
```

### 4.2 voiceproto

无需改 — `UserSpeechEnd.Text` 字段已存在且 optional。

### 4.3 observability

`infra/schemas/events/speech-observability-events-v1.json` 新增三个事件：

```json
{
  "speechClientASRCompleted": {
    "allOf": [
      { "$ref": "#/$defs/eventBase" },
      {
        "type": "object",
        "required": ["event_name", "session_id", "turn_id", "elapsed_ms", "text_length"],
        "properties": { "event_name": { "const": "speech_client_asr_completed" } }
      }
    ]
  },
  "speechClientASRSkipped": {
    "allOf": [
      { "$ref": "#/$defs/eventBase" },
      {
        "type": "object",
        "required": ["event_name", "session_id", "turn_id", "reason"],
        "properties": {
          "event_name": { "const": "speech_client_asr_skipped" },
          "reason": { "enum": ["not_available", "timeout", "engine_disabled"] }
        }
      }
    ]
  },
  "speechClientASRFailed": {
    "allOf": [
      { "$ref": "#/$defs/eventBase" },
      {
        "type": "object",
        "required": ["event_name", "session_id", "turn_id", "error_code"],
        "properties": { "event_name": { "const": "speech_client_asr_failed" } }
      }
    ]
  }
}
```

`reason` 字段是 enum，禁止自由文本（与 `infra/docs/observability/00_FluentWork可观测性与事件Schema设计.md` 设计原则 4 一致）。

### 4.4 phrase_block_id

无需改 — `phrase_block_id` 仍由 backend BadgeEmitter 从 `corpus.BlockCandidate.ID` 取，iOS 端通过 `feedback.badge` 镜像。

---

## 五、Rollout 计划

### 5.1 阶段 1（本周）

- iOS + backend 双仓落地骨架代码
- 后端默认 `VOICE_CLIENT_ASR_REQUIRED=false`（兼容 day-one iOS）
- iOS 加 `FeatureFlag.enableClientASR`，默认 `false`，debug build 打开
- 自动化测试覆盖：
  - iOS `defaultSpeechSessionClientSendsClientASRTextWhenEngineSupports`（new）
  - iOS `defaultSpeechSessionClientFallsBackToNilWhenASRUnavailable`（new）
  - backend `TestHandler_RejectsEmptyTextWhenClientASRRequired`（new）
  - backend `TestHandler_AcceptsEmptyTextWhenClientASRNotRequired`（新写）

### 5.2 阶段 2（内测）

- iOS FeatureFlag 默认 `true`，后端 `VOICE_CLIENT_ASR_REQUIRED=false`
- 看 `speech_client_asr_completed` / `speech_client_asr_skipped` 比例
- 客户端 ASR 命中率与 server-side ASR 比较（基于 `feedback.badge` 命中数）

### 5.3 阶段 3（灰度放量）

- 选 10% 用户开 `VOICE_CLIENT_ASR_REQUIRED=true`
- 监控 `error.client_asr_required` 出现频率

### 5.4 阶段 4（默认开启）

- 全量开 `VOICE_CLIENT_ASR_REQUIRED=true`
- B13 完成

---

## 六、测试计划

### 6.1 iOS

新增测试（必须）：

| 测试名 | 覆盖 |
|---|---|
| `defaultSpeechSessionClientSendsClientASRTextWhenEngineSupports` | client ASR 输出 → 帧 `text` 非空 |
| `defaultSpeechSessionClientFallsBackToNilWhenASRUnavailable` | engine 不支持 ASR → 帧 `text == nil`（day-one 兼容） |
| `defaultSpeechSessionClientSendsClientASRTextWithinTimeout` | ASR 完成 < 800ms → 帧带 text |
| `defaultSpeechSessionClientTimesOutASRAndSendsNil` | ASR 超时 → 帧 `text == nil`，埋点 `speech_client_asr_skipped` reason=`timeout` |
| `placeholderAudioEngineEmitsNoClientASREvents` | placeholder 不发 ASR 事件 |

### 6.2 backend

新增测试（必须）：

| 测试名 | 覆盖 |
|---|---|
| `TestHandler_RejectsEmptyTextWhenClientASRRequired` | `end.Text == ""` + `clientASRRequired=true` → 写 `error` 帧 |
| `TestHandler_AcceptsEmptyTextWhenClientASRNotRequired` | `end.Text == ""` + `clientASRRequired=false` → 正常走 BadgeEmitter |
| `TestHandler_AcceptsPopulatedTextWhenClientASRRequired` | `end.Text != ""` + `clientASRRequired=true` → 走 BadgeEmitter |
| `TestHandler_ClientASRRequiredEnvWiring` | env `VOICE_CLIENT_ASR_REQUIRED=1` → handler 字段为 true |

### 6.3 端到端

参考 `fluentwork-ios/docs/11_I13_iOS_backend_wss联调_runbook.md` §3 Case 5（ASR 分段埋点）扩展：

| 验证项 | 期望 |
|---|---|
| 客户端 ASR 完成 → `speech_client_asr_completed` 埋点 | turn_id 一致，elapsed_ms < 800 |
| ASR 超时 → `speech_client_asr_skipped` reason=`timeout` | turn_id 一致 |
| `VOICE_CLIENT_ASR_REQUIRED=true` + iOS 缺 ASR 能力 | 收到 `error.client_asr_required` |
| `feedback.badge` 命中数 | 与 server-side ASR 时期 ±5% 内（不显著回归） |

---

## 七、风险 / 不在 B13 范围

1. **客户端 ASR 引擎选型未定**：本次实现走 transcriber 协议抽象；引擎实现在后续 ticket 补（建议独立 sub-ticket `B13.1`）
2. **Volcengine SDK 接入**：依赖 B14 SDK 升级路径；Apple Speech fallback 必须同步就绪
3. **ASR 准确率 vs 延迟**：800ms 超时阈值是 day-one 估计，需根据实测调整；可改 `FeatureFlag.clientASRTimeoutMs`
4. **server-side ASR 退役路径**：B13 不动；B14+ 视 `speech_client_asr_skipped` 数据决定
5. **多语言**：当前 B13 默认中文（与 phrase block 一致）；多语言扩展需另立 ticket
6. **on-device 资源占用**：客户端 ASR 会增加 CPU / 内存；建议 iOS 端加 capability probe（`AVAudioSession.inputNode`）

---

## 八、文件清单（落本次实现）

新增：

- `fluentwork-ios/Shared/FluentWorkAudio/ClientASRTranscriber.swift`（协议）
- `fluentwork-ios/Shared/FluentWorkAudio/VolcengineClientASRTranscriber.swift`（占位实现）
- `fluentwork-ios/Shared/FluentWorkAudio/AppleSpeechClientASRTranscriber.swift`（占位实现）
- `fluentwork-ios/Shared/FluentWorkAudio/RawClientASRTranscriber.swift`（debug）
- `fluentwork-backend/internal/voicegateway/handler_client_asr.go`（gate 逻辑；或内联到 handler.go）
- `fluentwork-backend/internal/voicegateway/handler_client_asr_test.go`

修改：

- `fluentwork-ios/Shared/FluentWorkCore/Dependencies/AppDependencies.swift`（协议 + DI）
- `fluentwork-ios/Shared/FluentWorkCore/Architecture/Middleware/SpeechSessionMiddleware.swift`（订阅 clientASRFinal）
- `fluentwork-ios/Shared/FluentWorkNetworking/SpeechSessionClient.swift`（`sendSpeechBoundary` 增 `text` 参数）
- `fluentwork-ios/Tests/.../SpeechSessionClientTests.swift`（新测试）
- `fluentwork-ios/Tests/.../SpeechSessionMiddlewareTests.swift`（新测试）
- `fluentwork-backend/internal/voicegateway/handler.go`（gate + 错误帧）
- `fluentwork-backend/cmd/voice-gateway/main.go`（env 绑定）
- `fluentwork-backend/internal/voicegateway/handler_test.go`（新测试）
- `fluentwork-infra/schemas/events/speech-observability-events-v1.json`（三个新事件定义）
- 三仓 schema mirror 副本（`fluentwork-ios/Shared/FluentWorkCore/Resources/Schemas/...`、`fluentwork-backend/schemas/...`）

---

## 九、上线 checklist

- [ ] iOS 协议 + 三种 transcriber 占位实现
- [ ] iOS middleware 订阅 + 800ms 超时
- [ ] iOS 测试 5 个新测试 pass
- [ ] backend gate + 错误帧实现
- [ ] backend 测试 4 个新测试 pass
- [ ] infra schema 三个新事件 + 三仓 mirror 同步
- [ ] mock device 测试支持说明（`fluentwork-ios/docs/12_mock_device_测试支持说明.md` §11）状态更新：LoopbackSocketTransport / OpusDecoder 占位改实
- [ ] FeatureFlag `enableClientASR` 默认值确认（建议阶段 2 切 true）
- [ ] 环境变量 `VOICE_CLIENT_ASR_REQUIRED` 默认值确认（建议阶段 3 才开）
- [ ] 埋点 dashboard 占位（看 `speech_client_asr_completed` / `_skipped` 比例）
- [ ] rollout runbook 落地（阶段 2/3/4 切换步骤）

---

## 十、参考

- `fluentwork-ios/docs/11_I13_iOS_backend_wss联调_runbook.md` §1 字段表 + §3 Case 5
- `fluentwork-ios/docs/12_mock_device_测试支持说明.md` §7.1 Audio mock / §11 已知缺口
- `fluentwork-backend/docs/07_B14_D3_ASR实现说明.md`（server-side ASR 现状）
- `fluentwork-backend/docs/20_B12_badge_emit_问题修复说明.md`（`end.Text` 当前用法）
- `fluentwork-infra/docs/observability/00_FluentWork可观测性与事件Schema设计.md`（埋点 schema 真源）
- `fluentwork-meta/docs/30_技术方案/36_FluentWork可观测性与事件Schema设计草案.md`（跨端 `source=ios` 约定）