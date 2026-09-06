# P0-PROTO-05 — iOS 端 `ai.tts.*` mock decoder 接入(跨仓)

> **主仓**: `fluentwork-ios`(实际开发与 PR)
> **跟踪仓**: `fluentwork-backend`(本文件, 作为引用记录)
> **GitHub Title (ios 仓)**: `P0-PROTO-05: iOS 端 ai.tts.* mock decoder 接入 + 跨仓空跑验证`
> **Labels (ios 仓)**: `ios`, `v2.0-blocker`, `priority/P0`, `protocol-freeze`, `tts`
> **Milestone (ios 仓)**: `V2.0 W3`
> **截止**: 2026-09-13(周六)18:00
> **Owner**: iOS Lead + iOS 客户端开发同学
> **关联**: 79_ V2.0 §九 行动清单 #5 / P0-PROTO-03 帧协议 / P0-PROTO-04 ADR

---

## §0 Issue Body(整段可粘贴到 ios 仓)

```markdown
## 🎯 目标
iOS 端接入 WSS V2.0 `ai.tts.*` 三帧的 mock decoder + TTS 流式播放器骨架。即使 backend 的 B17 (TTS Provider) 还未 CLOSED, iOS 端可以先接 decoder + 单元测试, 为 9/13 跨仓空跑验证铺路。

## 🚧 阻塞条件
- P0-PROTO-03 全部 sub-ticket 完成(ai.tts.start/audio/end 字段定义)
- P0-PROTO-04 ADR 决议(选 Opus 解码库)

## 📋 实施步骤

### 1. 新建 iOS 模型(2 小时)

`ios/Sources/VoiceGateway/Models/AITTSFrames.swift`:

\`\`\`swift
public struct AITTSStart: Codable {
    public let type: String  // const "ai.tts.start"
    public let turn_id: String
    public let voice_id: String
    public let sample_rate: Int
    public let codec: String  // "opus" | "pcm"

    enum CodingKeys: String, CodingKey {
        case type
        case turn_id
        case voice_id
        case sample_rate
        case codec
    }
}

public struct AITTSAudio: Codable {
    public let type: String
    public let turn_id: String
    public let seq: Int
    public let data: String  // base64 encoded

    public func decodedBytes() throws -> Data {
        guard let d = Data(base64Encoded: data) else {
            throw TTSError.invalidBase64
        }
        return d
    }
}

public struct AITTSEnd: Codable {
    public let type: String
    public let turn_id: String
    public let completion_status: String  // "ok" | "interrupted" | "error"
    public let duration_ms: Int?

    enum CodingKeys: String, CodingKey {
        case type
        case turn_id
        case completion_status
        case duration_ms
    }
}
\`\`\`

### 2. 新建 mock decoder(3 小时)

`ios/Sources/VoiceGateway/TTS/MockTTSDecoder.swift`:

\`\`\`swift
public protocol TTSDecoder {
    func prepare(voiceId: String, sampleRate: Int, codec: String) throws
    func feed(seq: Int, bytes: Data, turnId: String) throws
    func finish(turnId: String, status: String, durationMs: Int?) throws
}

public final class MockTTSDecoder: TTSDecoder {
    // 记录所有调用, 用于单元测试断言
    public private(set) var prepares: [(voiceId: String, sampleRate: Int, codec: String)] = []
    public private(set) var feeds: [(seq: Int, bytes: Data, turnId: String)] = []
    public private(set) var finishes: [(turnId: String, status: String, durationMs: Int?)] = []

    public init() {}

    public func prepare(voiceId: String, sampleRate: Int, codec: String) throws {
        prepares.append((voiceId, sampleRate, codec))
    }

    public func feed(seq: Int, bytes: Data, turnId: String) throws {
        feeds.append((seq, bytes, turnId))
    }

    public func finish(turnId: String, status: String, durationMs: Int?) throws {
        finishes.append((turnId, status, durationMs))
    }
}
\`\`\`

### 3. 接入 WSS 帧分发(2 小时)

`ios/Sources/VoiceGateway/WSSFrameDispatcher.swift` 现有代码新增 case:

\`\`\`swift
case "ai.tts.start":
    let start = try decoder.decode(AITTSStart.self, from: data)
    try ttsDecoder.prepare(
        voiceId: start.voice_id,
        sampleRate: start.sample_rate,
        codec: start.codec
    )
case "ai.tts.audio":
    let audio = try decoder.decode(AITTSAudio.self, from: data)
    let bytes = try audio.decodedBytes()
    try ttsDecoder.feed(seq: audio.seq, bytes: bytes, turnId: audio.turn_id)
case "ai.tts.end":
    let end = try decoder.decode(AITTSEnd.self, from: data)
    try ttsDecoder.finish(
        turnId: end.turn_id,
        status: end.completion_status,
        durationMs: end.duration_ms
    )
\`\`\`

### 4. 单元测试(1.5 小时)

`ios/Tests/VoiceGatewayTests/AITTSFramesTests.swift`:

- `testAITTSStart_DecodeValid`
- `testAITTSAudio_DecodeBase64Data`
- `testAITTSEnd_OptionalDurationMs`
- `testAITTSFrames_TypeConstant` (断言 type 字段值)
- `testMockDecoder_PrepareFeedFinish_Sequence`
- `testMockDecoder_RecordAllCalls`

## ✅ 验收
- [ ] 3 个 Swift 模型文件 + Mock decoder + 集成到 WSSFrameDispatcher
- [ ] 6 个单元测试全部 PASS
- [ ] iOS 端 codec 字段处理逻辑覆盖 "opus" 与 "pcm" 两个分支(至少 mock 测过)
- [ ] 接入 PR 在 ios 仓 review 通过
- [ ] 跨仓空跑验证(9/13): backend WSS dev-echo provider 模拟 ai.tts.* 三帧, iOS 端 MockTTSDecoder 记录到 3 个 prepare + 5-30 个 feed + 1 个 finish

## 🔗 依赖
- Blocked by: P0-PROTO-03 (字段定义), P0-PROTO-04 (ADR 决策)
- Blocks: P0-PROTO-06 (封板 gate 必须包含本 iOS 接入验证)
- 跨仓关联: backend 仓的 79_ V2.0 §九 + 51_ WSS V2.0 帧协议文档

## 📌 跨仓同步约定
- 本 Issue 在 ios 仓建仓(主仓)
- backend 仓同步创建 tracking issue(本文件 §2), 引用 ios issue
- PR 描述必须包含: ios issue 编号 + backend 仓 51_ 文档 commit hash
```

---

## §1 与 backend 仓的关系(本文件的本质)

**重要**: 本 Issue 的实际开发与 PR 在 `fluentwork-ios` 仓, **不是 backend 仓**。

backend 仓创建本 tracking issue 的唯一目的是:
1. 留 audit trail — 让后端 TL 知道 iOS 端进度
2. 跨仓 review 入口 — 在 79_ 评审闭环时一并 reference
3. 跨仓空跑验证 — backend 仓 dev-echo provider 模拟输出配合 iOS mock decoder, 验证在两仓联动

**不要在 backend 仓提 PR 修改任何 Swift 代码**。

---

## §2 backend 仓 tracking issue body(简版, 用于 backend 仓建仓时引用)

```markdown
## 🎯 跟踪 ios 仓 P0-PROTO-05

**主 Issue**: `fluentwork-ios` 仓的 `P0-PROTO-05: iOS 端 ai.tts.* mock decoder 接入`

**本 tracking issue 目的**:
- 同步 ios 端开发进度到 backend 仓
- 9/13 跨仓空跑验证需要 backend dev-echo provider 配合
- 79_ V2.0 评审闭环时一并归档

**待办**:
- [ ] 在 ios 仓建仓后, 在本 issue 评论中 @ 后端 TL
- [ ] 9/13 跨仓空跑验证 SOP 由 backend 仓 owner 准备(配合 dev-echo)
- [ ] ios 仓 issue CLOSED 后, 关闭本 tracking issue
```

---

## §3 跨仓空跑验证 SOP(9/13 由 backend 仓 owner 准备)

### 3.1 backend 端 dev-echo provider 改造

文件: `internal/voicegateway/provider_dev_echo.go`

新增 mock 方法(在 dev-echo provider 内, 不影响生产路径):

\`\`\`go
// MockAITTSFrames emits fake ai.tts.start/audio/end frames for iOS integration testing.
// Activated only when config DevEchoTTSMock = true.
func (p *DevEchoProvider) emitMockTTSFrames(ctx context.Context, conn *websocket.Conn, turnId string) error {
    // 1. 发送 ai.tts.start
    start := voiceproto.AITTSStart{
        Type: "ai.tts.start",
        TurnID: turnId,
        VoiceID: "mock_voice_01",
        SampleRate: 24000,
        Codec: "opus",
    }
    if err := conn.WriteJSON(start); err != nil { return err }

    // 2. 发送 10 个 ai.tts.audio(模拟 200ms 流式)
    for i := 0; i < 10; i++ {
        audio := voiceproto.AITTSAudio{
            Type: "ai.tts.audio",
            TurnID: turnId,
            Seq: i,
            Data: base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("mock-opus-frame-%d", i))),
        }
        if err := conn.WriteJSON(audio); err != nil { return err }
        time.Sleep(20 * time.Millisecond)
    }

    // 3. 发送 ai.tts.end
    end := voiceproto.AITTSEnd{
        Type: "ai.tts.end",
        TurnID: turnId,
        CompletionStatus: "ok",
        DurationMs: 200,
    }
    return conn.WriteJSON(end)
}
\`\`\`

激活条件: `.env.dev` 设 `DEV_ECHO_TTS_MOCK=true`

### 3.2 iOS 端验证脚本

`ios/scripts/cross-repo-empty-run-2026-09-13.sh`:

\`\`\`bash
#!/bin/bash
set -e
echo "[1/3] 启动 backend (dev-echo TTS mock)..."
cd ../fluentwork-backend
DEV_ECHO_TTS_MOCK=true ./scripts/dev-up.sh &

echo "[2/3] 启动 iOS 模拟器..."
cd ../fluentwork-ios
xcodebuild test -scheme FluentWork -destination 'platform=iOS Simulator,name=iPhone 15' \\
  -only-testing:VoiceGatewayTests/AITTSFramesTests

echo "[3/3] 验证 MockTTSDecoder 调用序列..."
# 期望: prepares.count == 1, feeds.count == 10, finishes.count == 1
\`\`\`

---

## §4 gh CLI 落库命令(跨仓)

```bash
# 1. ios 仓建仓(主 issue)
cd /Users/apple/Developments/FluentWork\ App/fluentwork-ios
gh issue create \\
  --title "P0-PROTO-05: iOS 端 ai.tts.* mock decoder 接入 + 跨仓空跑验证" \\
  --label "ios,v2.0-blocker,priority/P0,protocol-freeze,tts" \\
  --milestone "V2.0 W3" \\
  --body-file ../fluentwork-backend/.scratch/issues/2026-09-06-W3-protocol-freeze/05-ios-mock-decoder.md

# 2. backend 仓建仓(tracking issue, 简版)
cd /Users/apple/Developments/FluentWork\ App/fluentwork-backend
gh issue create \\
  --title "P0-PROTO-05-TRACKING: iOS 端 ai.tts.* mock decoder (跨仓跟踪)" \\
  --label "v2.0-blocker,priority/P0,protocol-freeze,cross-repo" \\
  --milestone "V2.0 W3" \\
  --body "跟踪 ios 仓 P0-PROTO-05。详见 .scratch/issues/2026-09-06-W3-protocol-freeze/05-ios-mock-decoder.md"
```

---

## §5 不在本 Issue 范围内

- ❌ 不写真实 Opus 解码代码(用 MockTTSDecoder 占位, 真实解码待 B17 落地后启动)
- ❌ 不实施 B17 后端 TTS Provider(完全独立)
- ❌ 不修改 voicegateway dev-echo provider 之外的任何 backend 代码
- ❌ 不引入 iOS 端 AVAudioEngine 真实音频播放(留给 B17 落地后)
- ❌ 不在 iOS 端接 server_ts_ms 字段(留给 03.4 + 51_ 文档验证)