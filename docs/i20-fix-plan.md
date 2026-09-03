# FluentWork I20 P0/P1 收口计划与全链路架构文档

> 文档版本：v1.0  
> 创建日期：2026-09-03  
> 范围：fluentwork-backend (Go) + fluentwork-ios (Swift/SwiftUI)

---

## 一、问题背景与整体架构

### 1.1 当前系统架构总览

```
┌─────────────────────────────────────────────────────────────────┐
│                        iOS App (SwiftUI)                        │
│  ┌──────────────────┐  ┌───────────────────┐  ┌──────────────┐ │
│  │ LiveAudioEngine  │  │SpeechSessionMachine│  │ URLSession   │ │
│  │  (VAD 能量检测)   │→ │  (TGReduxKit 状态机)│→ │ SocketTransport│ │
│  │  16kHz PCM 采集  │  │  phase: waitingUser │  │  WSS 连接     │ │
│  │  AVAudioEngine   │  │  .recording        │  │              │ │
│  └──────────────────┘  └───────────────────┘  └──────┬───────┘ │
└───────────────────────────────────────────────────────┼─────────┘
                                                        │ WSS
                                                        │ PCM 音频流
                                                        │ 控制帧
                    ┌────────────────────────────────────┴───────────┐
                    │           voice-gateway (Go) Port 8081           │
                    │  ┌────────────────────────────────────────────┐  │
                    │  │ Handler (sessionRuntime)                   │  │
                    │  │  · 认证握手 (ticket consume)               │  │
                    │  │  · 控制帧分发 (handleControl)               │  │
                    │  │  · 音频帧转发 (handleAudio)                │  │
                    │  │  · rt.broken / rt.reopenAttempted         │  │
                    │  │  · warn deduplication (logWarn)           │  │
                    │  └──────────────┬─────────────────────────────┘  │
                    │                 │                              │
                    │  ┌──────────────▼─────────────────────────────┐  │
                    │  │ VoiceProvider (interface)                   │  │
                    │  │  · MockVoiceProvider (测试用)              │  │
                    │  │  · DevEchoVoiceProvider (本地开发)          │  │
                    │  │  · VolcDuplexProvider (生产)               │  │
                    │  └──────────────┬─────────────────────────────┘  │
                    └─────────────────┼───────────────────────────────┘
                                        │
                    ┌───────────────────▼─────────────────────────────┐
                    │         voicepoc/volc_duplex.go                │
                    │  · DuplexSession (WSS ↔ Volcengine)           │
                    │  · TurnResult { Transcript, Outcome, ... }    │
                    │  · TurnOutcome: ok / partial / timeout / error │
                    └─────────────────────────────────────────────────┘

                    ┌─────────────────────────────────────────────────┐
                    │           app-server (Go) Port 8080            │
                    │  · session lifecycle (Activate/End)              │
                    │  · utterance 持久化                             │
                    │  · background worker                            │
                    └─────────────────────────────────────────────────┘
```

### 1.2 关键标识符体系

| 标识符 | 作用域 | 生命周期 | 创建时机 | 使用场景 |
|--------|--------|----------|----------|----------|
| `session_id` | 全链路 | session 级别 | POST /sessions 返回 | 所有帧的关联键 |
| `turn_id` | 一次对话轮次 | turn 级别 | iOS 端 `"turn-N"` 或 Volc 生成 | 语料命中去重、trace 对齐 |
| `log_id` | 第三方请求 | 单次请求 | Volcengine 握手返回 `X-Tt-Logid` | 供应商侧问题追查 |
| `ticket_id` | 一次性令牌 | 一次性 | app-server 生成 | WSS 认证 |

### 1.3 关键文件索引

#### Backend (Go)

| 文件 | 职责 |
|------|------|
| `internal/voicegateway/handler.go` | WSS 连接处理、帧分发、`rt.broken`、warn 去重 |
| `internal/voicegateway/provider_volc_duplex.go` | 火山引擎双工 Provider、TurnOutcome 设置 |
| `internal/voicegateway/provider_dev_echo.go` | 本地开发 Provider (含 PCM fixture T2) |
| `internal/voicegateway/provider.go` | `VoiceProvider` / `VoiceProviderSession` 接口 |
| `internal/voiceproto/frames.go` | 所有 WSS 控制帧类型定义 |
| `internal/voicepoc/volc_duplex.go` | `DuplexSession`、TurnResult、TurnOutcome 枚举 |
| `internal/session/types.go` | Session 状态机、Utterance 类型 |
| `internal/session/service.go` | Session 生命周期管理 |
| `pkg/logx/logx.go` | 结构化日志、Segment timing |

#### iOS (Swift)

| 文件 | 职责 |
|------|------|
| `FluentWorkCore/Services/LiveAudioEngine.swift` | AVAudioEngine 音频采集、VAD 能量检测 |
| `FluentWorkCore/SpeechSession/SpeechSessionMachine.swift` | 纯状态机 (phase 流转) |
| `FluentWorkCore/Architecture/Middleware/SpeechSessionMiddleware.swift` | 副作用解释、B15 70s turn timeout |
| `FluentWorkCore/Services/DefaultSpeechSessionClient.swift` | WSS 会话生命周期管理 |
| `FluentWorkNetworking/Socket/URLSessionSocketTransport.swift` | WebSocket 传输 |
| `FluentWorkDiagnostics/Tracker.swift` | 事件追踪 |

---

## 二、Item 1: I20 遗留 P0/P1 收口

### 2.1 Turn 超时显式 `outcome=timeout` ⚠️ 重点

#### 问题分析

当前后端在 `volc_duplex.go` 的 `collectTurn` 中已正确设置 `TurnOutcome`，但 **`ai.turn.end` 帧本身没有携带 outcome 字段**，iOS 收不到明确的超时原因。

**当前 `voiceproto.AITurnEnd`：**
```go
type AITurnEnd struct {
    Type   string `json:"type"`
    TurnID string `json:"turn_id,omitempty"`
}
```

**影响：** iOS 只能通过"70s 后 ai.turn.end 没来"这种隐式方式感知超时，无法区分"正常完成"和"服务端 timeout"。

#### 实现方案

**Step 1: 后端 `voiceproto/frames.go` — AITurnEnd 增加 Outcome 字段**

```go
// AITurnEnd marks the explicit end boundary of one assistant turn.
type AITurnEnd struct {
    Type    string `json:"type"`
    TurnID  string `json:"turn_id,omitempty"`
    // B15: explicit terminal status so iOS can distinguish ok/partial/timeout/error
    // without relying on implicit timing heuristics.
    Outcome string `json:"outcome,omitempty"` // "" | "ok" | "partial" | "timeout" | "error"
}
```

**Step 2: 后端 `provider_volc_duplex.go` — turnToOutbound 传递 Outcome**

```go
// turnToOutbound: 在 AITurnEnd 中携带 outcome
outbound = append(outbound, ProviderOutbound{
    Control: voiceproto.AITurnEnd{
        Type:    voiceproto.TypeAITurnEnd,
        TurnID:  turnID,
        Outcome: string(turn.Outcome), // 传递 ok/partial/timeout/error
    },
})
```

**Step 3: iOS `WSControlFrame.swift` — AITurnEnd 解码**

```swift
case let .aiTurnEnd(turnID, outcome): // 解码带 outcome 的帧
    // 当 outcome == "timeout" 时，行为与 70s 超时相同
    if outcome == "timeout" {
        turnTimeoutTracking?.disarm()
        dispatch(.speakingRoom(.session(.failed("turn_timeout"))))
    }
```

**难度评估：** ⭐⭐ (低) — 字段扩展 + 解码器适配，两端各改 2-3 个文件。

**验收标准：**
- 后端日志：`ai.turn.end` 帧包含 `"outcome":"timeout"` (当超时时)
- iOS 收到 `outcome=timeout` 后触发 `.failed("turn_timeout")`，等价于 70s 兜底
- `TurnOutcomeOK` 和空 Outcome 在正常情况下不触发任何行为变化

---

### 2.2 iOS 超时兜底 ✅ 已实现

**当前状态：** `SpeechSessionMiddleware.swift` 已实现 70s 客户端兜底：

```swift
// B15: startTurnTimeout 标志在 .recording→.processing 转换时设置
// transport task 中等待 defaultTurnTimeoutSeconds=70s
// 若 ai.turn.end 先到，disarm() 取消计时器
// 若超时先到，触发 .failed("turn_timeout")
```

**文档化确认：** 已在代码注释中标注，TODO：补充单测覆盖 `turnTimeoutTracking` 的 disarm race 场景。

---

### 2.3 后端写失败立即退 session ⚠️ 重点

#### 问题分析

当前 `handler.go` 中音频帧写入失败时：

```go
// handleAudio 中的逻辑
rt.broken = true
h.logWarn(rt, "provider_audio_failed", ...)  // ✅ 有去重
// ⚠️ 但 return 后 loop 继续运行，iOS 不会收到退出信号
// 只能等 iOS 侧的 WSS 读超时才能结束
```

**根本问题：** `rt.broken = true` 后，后续音频帧被静默丢弃（正确），但 session 本身不退——iOS 需要等到 2 分钟 WSS idle 超时才能感知。

#### 实现方案

**方案 A（推荐）：在 handleAudio 返回时同时关闭 session**

```go
// handleAudio 写入失败后：
rt.broken = true
h.logWarn(rt, ...)
return writeJSON(ctx, conn, voiceproto.ErrorFrame{
    Type:    voiceproto.TypeError,
    Code:    "provider_audio_failed",
    Message: err.Error(),
}) 
// ⚠️ 注意：writeJSON 本身也可能失败（iOS 端已断）
// 若写入失败，直接返回非 nil error 让 loop 退出
```

**方案 B（更干净）：引入 sessionFatal 标志**

```go
type sessionRuntime struct {
    // ... 现有字段 ...
    // 新增：致命错误标志，一旦设置 loop 立即退出
    sessionFatal bool
}
```

在 `handleAudio` 失败时设置 `rt.sessionFatal = true`，然后在 `loop` 的下次迭代检查：

```go
if rt.sessionFatal {
    return fmt.Errorf("session fatal error")
}
```

**推荐方案 A**（改动最小，行为与现有错误处理一致）。

**难度评估：** ⭐⭐ (低) — handler.go 一处改动，添加 `return err` 或返回特定 sentinel error。

**验收标准：**
- Provider 写入失败后，iOS 应在 1s 内收到 `error` 帧
- 后端 loop 在发送错误帧后立即退出（不等 idle timeout）
- 日志中 `outcome` 字段标记为 `timeout`（见 2.1）

---

### 2.4 Warn 去重 ✅ 已实现

**当前状态：** `handler.go` 中的 `logWarn` 函数已实现 5s 窗口 + 每 10 次输出一行的去重逻辑。

**代码位置：** `handler.go:38-58`

```go
const warnDedupInterval = 10
func (h *Handler) logWarn(rt *sessionRuntime, key, msg string, args ...any) {
    // 5s 窗口内相同 key 只在第 1 次和第 10N 次输出完整行
    // 其余情况只计数
}
```

**TODO：** 在 `cmd/voice-gateway/main.go` 中确认所有 provider 相关 WARN 均通过 `logWarn` 输出（非直接 `logger.Warn`）。

---

## 三、Item 2: 本地可复现（dev-echo + PCM fixture）

### 3.1 现状分析

`DevEchoVoiceProvider` 已实现，但缺少以下能力：

1. **命令行 fixture 路径配置** — 当前 fixture path 只能通过代码注入，无法在 `cmd/voice-gateway` 启动时指定
2. **端到端 fixture 测试** — 没有 fixture-driven 的集成测试
3. **合成 PCM 生成** — `DevEchoFixtureGenerator` 已存在但未集成到测试框架

### 3.2 实现方案

#### Step 1: voice-gateway 启动参数支持 `--dev-echo-fixture`

```go
// cmd/voice-gateway/main.go
var devEchoFixturePath string
flag.StringVar(&devEchoFixturePath, "dev-echo-fixture", "", 
    "Path to 16kHz mono PCM fixture file (dev only)")

// 配置 provider
if providerName == "dev-echo" {
    provider = voicegateway.NewDevEchoVoiceProvider(
        os.Getenv("VOICE_DEV_ECHO_TEXT"),
        logger,
    )
    // 通过全局 config 传递 fixture path
}
```

#### Step 2: 生成测试 fixture 文件

```bash
# 生成一个 5 秒 1kHz 正弦波的 PCM 音频
# 使用 Go 测试代码中的 DevEchoFixtureGenerator
# 输出到 fixtures/test_5s.pcm
```

#### Step 3: 端到端测试流程

```go
// internal/voicegateway/dev_echo_test.go
func TestDevEchoWithPCMFixture(t *testing.T) {
    // 1. 启动 voice-gateway (dev-echo provider + fixture)
    // 2. iOS mock transport 连接到 WSS
    // 3. 发送 session.start
    // 4. 发送 user.speech.end
    // 5. 验证收到 ai.audio.chunk 音频帧
    // 6. 验证收到 ai.turn.end
    // 7. 验证 badge 触发（DevEcho 有 EchoText 时）
}
```

#### Step 4: iOS 本地测试

在 `LiveAudioEngineTests` 中使用 `PlaceholderAudioEngine` + 注入的 PCM 数据：

```swift
// 本地复现：模拟一个"说话结束"的 VAD 事件
func testLocalVADTrigger() async {
    let engine = PlaceholderAudioEngine()
    let events = engine.events()
    
    // 注入合成音频数据
    let syntheticPCM = generateTestPCM(durationMs: 2000) // 2s
    await engine.injectPCM(syntheticPCM)
    
    // 验证 VAD 触发 .speechEnded 事件
    // 验证 PCM chunk 事件
}
```

### 难度评估：⭐⭐⭐ (中)

- 关键路径：Go 侧 fixture 路径注入 + iOS mock audio engine
- 难点：iOS `PlaceholderAudioEngine` 是否已实现？需要确认 `events()` 方法可被测试调用

---

## 四、Item 3: 全链路 Trace 对齐

### 4.1 问题分析

当前三端标识符体系存在以下不一致：

| 问题 | 现状 | 影响 |
|------|------|------|
| `turn_id` 生成位置分散 | iOS: `"turn-N"`，Volc: `"volc-turn-N"`，DevEcho: `"dev-echo-turn"` | 日志关联困难 |
| `log_id` 未贯穿全链路 | Volc 返回后仅在 `volc_duplex.go` 内部使用 | 无法从 iOS 端直接追溯到供应商日志 |
| trace context 断层 | iOS 的 tracker 事件只有 `turn_id`，没有 `log_id` | 跨层调试需要人工匹配 |
| Segment timing 不统一 | Go 用 `logx.Begin/End`，iOS 用 `timings.mark` | 无法对齐同一个操作的时间戳 |

### 4.2 实现方案

#### Step 1: 统一 turn_id 命名规范

**规范：** `turn-{session_index}`，其中 `session_index` 为该 session 内用户 turn 的序号（从 1 开始）。

iOS 端（SpeechSessionMiddleware.swift）已经是 `"turn-\(count + 1)"`，后端统一采用：

```go
// provider_volc_duplex.go turnToOutbound
// 改前：turnID := fmt.Sprintf("volc-turn-%d", s.nextSeq)
// 改后：使用 s.activeTurnID（由 iOS 传入），避免重复生成
```

**需要改动：** VolcDuplexProvider 不再自行生成 `volc-turn-N`，改为使用 `s.activeTurnID`（在 `HandleClientControl` 时从 `UserSpeechEnd.TurnID` 读取）。

#### Step 2: log_id 贯穿全链路

在 `voiceproto.AITurnEnd` 增加 `LogID` 字段：

```go
type AITurnEnd struct {
    Type    string `json:"type"`
    TurnID  string `json:"turn_id,omitempty"`
    Outcome string `json:"outcome,omitempty"`
    // B15: vendor log_id for cross-layer trace
    LogID   string `json:"log_id,omitempty"`
}
```

在后端设置：

```go
// volc_duplex.go 中设置 log_id
// DuplexSession.LogID() 已在握手时获取

// provider_volc_duplex.go turnToOutbound 中：
outbound = append(outbound, ProviderOutbound{
    Control: voiceproto.AITurnEnd{
        Type:    voiceproto.TypeAITurnEnd,
        TurnID:  turnID,
        Outcome: string(turn.Outcome),
        LogID:   s.session.LogID(), // 新增
    },
})
```

#### Step 3: iOS Tracker 事件增加 log_id

```swift
// SpeechSessionMiddleware.swift
// 当收到 ai.turn.end 时，从帧中提取 log_id 并注入到后续 tracker 事件
case let .control(.aiTurnEnd(turnID, outcome, logID)):
    turnTimeoutTracking?.disarm()
    // 将 log_id 存入 timings recorder
    timings.setLogID(logID)
    // 后续事件自动携带 log_id
    tracker.track(event: "ai_turn_end", properties: [
        "turn_id": turnID ?? "nil",
        "log_id": logID ?? "",
    ])
```

#### Step 4: 日志时间线可回放格式

在 Go 侧添加 `logx.End` 的 JSON structured output 格式，便于 ELK / Loki 解析：

```go
// pkg/logx/logx.go
// Segment.End 已有 duration_ms，补充 trace_id
type SegmentLog struct {
    Event     string         `json:"event"`
    Module    string         `json:"module,omitempty"`
    SessionID string         `json:"session_id,omitempty"`
    TurnID    string         `json:"turn_id,omitempty"`
    LogID     string         `json:"log_id,omitempty"`
    DurationMS float64       `json:"duration_ms"`
    Outcome   string         `json:"outcome,omitempty"`
    Level     string         `json:"level"`
    TS        int64          `json:"ts"`
}
```

### 难度评估：⭐⭐⭐⭐ (中高)

- 涉及后端 provider_volc_duplex、voiceproto、iOS middleware 三端协同
- 关键是 VolcDuplexProvider 的 `activeTurnID` 来源需要从 iOS 端传入的 `UserSpeechEnd.TurnID` 同步过来
- `log_id` 传递需要确认 Volc DuplexSession 在整个 session 生命周期内 logID 不变

---

## 五、Item 4: 减少对自动 VAD 的依赖

### 5.1 现状分析

当前 iOS 端 VAD 流程：

```
音频能量 > threshold → .speechStarted
音频能量 < threshold 持续 1500ms → .speechEnded
```

**VAD 的不确定性来源：**
1. 环境噪音误触发 speechStarted
2. 用户停顿 < 1500ms 导致 VAD 过早 end
3. 真实设备上 AVAudioEngine 输入质量波动
4. 不同机型的麦克风特性不同

PRD 原文已有"按住说话/点击说话"的设计，但当前实现中**自动 VAD 是唯一主路径**。

### 5.2 MVP 方案：手动触发为主，自动 VAD 降级为可选

#### 架构设计

```
用户输入模式（Feature Flag 控制）：
┌─────────────────────────────────────────┐
│  Manual Mode (MVP 主路径，PRD 原生设计)   │
│  ─────────────────────────────────────  │
│  按住 PTT 按钮 → 采集音频 → 松开发送     │
│  点击 "按住说话" → 开始采集 → 点击结束   │
│  .holdStart / .holdEnd (已有)            │
└─────────────────────────────────────────┘
        ↕ Feature Flag: "voice_vad_auto"
┌─────────────────────────────────────────┐
│  Auto VAD Mode (降级为可选)              │
│  ─────────────────────────────────────  │
│  自动检测语音起止                        │
│  自动 VAD 需要额外配置 flag              │
└─────────────────────────────────────────┘
```

#### 实现方案

**Step 1: Feature Flag 定义**

```swift
// FluentWorkFeatureFlags/VoiceFeatureFlags.swift
public enum VoiceInputMode: String, FeatureFlagValue {
    case manual   // 按住说话/点击说话（MVP 主路径）
    case autoVAD  // 自动 VAD（可选降级）
}

public let voiceInputModeFlag = FeatureFlag(
    key: "voice_input_mode",
    defaultValue: VoiceInputMode.manual
)
```

**Step 2: LiveAudioEngine 增加手动模式**

```swift
public enum AudioInputMode: Sendable {
    case autoVAD   // 当前行为：能量检测
    case manualPTT // 按住说话：外部控制开始/结束
}

public actor LiveAudioEngine {
    private var inputMode: AudioInputMode = .autoVAD
    
    public func setInputMode(_ mode: AudioInputMode) {
        self.inputMode = mode
        if mode == .manualPTT {
            // 停止 VAD tracker，改为外部触发
            speechTracker = AudioSpeechActivityTracker() // 不再自动触发
        }
    }
    
    // 手动模式：外部显式调用开始/结束
    public func beginManualSpeech() async {
        guard inputMode == .manualPTT else { return }
        continuation.yield(.speechStarted)
    }
    
    public func endManualSpeech() async {
        guard inputMode == .manualPTT else { return }
        continuation.yield(.speechEnded)
    }
}
```

**Step 3: SpeakingRoomFeature UI 适配**

```swift
// SpeakingRoomFeature.swift
@ViewBuilder
var recordingControls: some View {
    switch featureFlagProvider.voiceInputMode {
    case .manualPTT:
        HoldToSpeakButton(
            onHoldStart: {
                await audioEngine.beginManualSpeech()
            },
            onHoldEnd: {
                await audioEngine.endManualSpeech()
            }
        )
    case .autoVAD:
        // 当前 VAD UI
        VADStatusIndicator(session.phase)
    }
}
```

**Step 4: Middleware 兼容两种模式**

`SpeechSessionMiddleware` 中 `.speechStarted` / `.speechEnded` 事件来源：

- 手动模式：`holdStart` / `holdEnd` → iOS dispatch → middleware
- 自动模式：VAD tracker 触发 → middleware

两者最终都走同一个 `interpretSpeechSessionSideEffect` 处理，无需改动副作用解释器。

### 5.3 关键决策点

| 决策 | 方案 A（推荐） | 方案 B |
|------|---------------|--------|
| Feature Flag 粒度 | `voice_input_mode: manual | autoVAD` | `voice_vad_enabled: bool` |
| UI 变化 | PTT 按钮替换 VAD 状态条 | PTT 叠加在 VAD 上 |
| 后端影响 | 无（帧格式不变） | 无 |
| 测试策略 | 手动模式：单元测试覆盖 PTT 事件流 | 自动 VAD：现有测试保持 |

**推荐方案 A**（完全符合 PRD "按住说话/点击说话"设计，消除最大不确定性源）

### 难度评估：⭐⭐⭐⭐ (中高)

- 涉及 FeatureFlag 配置、iOS AudioEngine 接口变更、UI 层适配
- 关键路径：AudioEngine 的 `inputMode` 切换不破坏现有 VAD 路径
- 风险：手动和自动模式切换时 `speechTracker` 状态一致性

---

## 六、实现优先级与依赖关系

```
Item 1.1 (outcome→ai.turn.end) ──┐
Item 1.2 (write failure exit) ───┼── 优先级: P0 (I20 核心)
Item 1.3 (warn deduplication) ────┘         ↓
Item 3 (trace alignment)  ─────────────────→│ (依赖 outcome 字段)
Item 2 (dev-echo fixture)  ─────────────────→│ (独立，可并行)
Item 4 (manual VAD primary)  ─────────────────→ 优先级: P1
```

**推荐实施顺序：**

1. **Week 1**: Item 1.1 + 1.2（后端核心 + iOS 解码器，2-3 天）
2. **Week 2**: Item 3 trace 对齐（3 端协同，3-4 天）
3. **Week 3**: Item 2 dev-echo fixture（Go 测试 + iOS mock，2 天）
4. **Week 4**: Item 4 手动 VAD 主路径（FeatureFlag + AudioEngine + UI，3-4 天）

---

## 七、测试策略

### 7.1 后端单元测试

```
✓ TurnOutcome 枚举所有值 → ai.turn.end 帧 outcome 字段验证
✓ handleAudio write failure → session 立即退出
✓ logWarn 去重：同一 key 11 次调用 → 2 条日志输出
✓ DevEchoProvider + PCM fixture → ai.audio.chunk + ai.turn.end
✓ turn_id 格式统一："turn-N" 不再出现 "volc-turn-N"
```

### 7.2 iOS 单元测试

```
✓ TurnTimeoutTracking.arm() / disarm() / isArmed 竞态测试
✓ aiTurnEnd(turnID, outcome, logID) 解码
✓ ManualPTT 模式：begin/end 事件流
✓ FeatureFlag 切换 inputMode 不破坏 VAD 路径
```

### 7.3 E2E 测试（dev-echo + PCM fixture）

```
✓ iOS mock transport ↔ voice-gateway (dev-echo)
  → session.start → user.speech.end → ai.audio.chunk + ai.turn.end
✓ badge 命中验证（EchoText 非空时）
✓ 端到端 trace：session_id + turn_id + log_id 在同一 timeline
```

---

## 八、已知风险

| 风险 | 描述 | 缓解措施 |
|------|------|----------|
| Volc DuplexSession.LogID() 在整个 session 生命周期稳定 | 若 Volc 每次请求换 logID，log_id 传递无意义 | 验证 `DuplexSession.LogID()` 返回值在 session 生命周期内稳定 |
| iOS 70s 超时和后端 60s 超时竞态 | 后端 60s timeout 发 ai.turn.end 后，iOS 70s timer 还没 disarm | 确保 ai.turn.end 到达 iOS < 5s（当前 2min idle timeout 足够） |
| 手动 VAD FeatureFlag 灰度 | 全量开启后可能影响现有 VAD 用户体验 | 先在内部 dogfood 2 周再灰度 |
| PCM fixture 文件格式 | fixture 不对齐 16kHz mono PCM16LE | fixture 生成工具强制 16kHz 验证 + CI 检查 |
