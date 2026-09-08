# ADR-0073: iOS 端 TTS Opus 解码方案

**Status**: ACCEPTED

**Date**: 2026-09-09

**Decision owners**: iOS Lead, 后端 TL

## Context

WSS V2.0 已在 `51_WSS_V2_帧协议设计_2026-09-08.md` 中约定：

- gateway 发送单声道 Opus packet，每条 WebSocket binary message 携带一个 packet；
- `ai.tts.start` 声明输入采样率，默认 24 kHz；
- iOS 现有 `WSAudioFrameDecoder` 输出 16 kHz、单声道、interleaved PCM16，供
  `LiveAudioEngine` 播放；
- TTS 在 backend 合成并经 FluentWork WSS 下发，iOS 不需要再次建立火山 TTS 会话。

原 Issue #98 把“系统原生”和“libopus wrapper”合并成同一选项，并声称
`AVAudioEngine + AVAudioConverter` 等同于直接集成 libopus。两者实际是不同依赖与
风险模型，本 ADR 将其拆开评估。

## Decision drivers

1. 不绕过 FluentWork 既有 WSS transport 和鉴权边界。
2. 能逐 packet 解码 24 kHz mono Opus，并输出 16 kHz mono PCM16。
3. 支持 iOS 17 及 Swift 6 并发边界。
4. 最小化二进制供应链、许可证和包管理风险。
5. decoder 必须可注入、可 mock，并能在真实音频 fixture 上确定性测试。

## Decision

选择 **Apple 系统 `AVAudioConverter` / AudioToolbox Opus decoder** 作为首选实现，
不引入火山 iOS SDK，也不在首版引入第三方 Opus package。

iOS 实现应：

1. 将现有只有 `decode(_:)` 的 `WSAudioFrameDecoder` 演进为流生命周期接口：
   `prepare(sampleRateHz:codec:)` 接收 `ai.tts.start`，`decode(_:)` 处理 binary
   packet，`reset()` 处理 interrupt、`ai.tts.end` 和失败清理。decoder 继续由
   `LiveAudioEngine` 持有，engine 的 interrupt 路径必须调用 `reset()`。
2. 根据 `ai.tts.start.sample_rate` 动态创建 `kAudioFormatOpus` mono compressed
   input format。V2.0 只接受协议枚举中的 16/24/48 kHz；其他值返回 unsupported
   format error。20 ms packet 的 `mFramesPerPacket` 分别为 320/480/960，
   `mChannelsPerFrame = 1`。
3. 将每条 WSS payload 写入 `AVAudioCompressedBuffer`，设置
   `packetCount = 1`、`byteLength` 和对应的 `AudioStreamPacketDescription`。
   packet description 的 start offset 为 0，data byte size 等于 payload 长度。
4. 让 Apple converter 解码并直接输出现有 decoder contract 所需的 16 kHz mono
   interleaved PCM16。
5. 在 actor 或专用串行执行器内持有有状态 converter；不得使用 `NSLock`。
6. 将初始化失败、坏 packet、无输出和转换失败映射为明确的 decoder error。
7. 在接入生产路径前，用 backend 产生的真实 20 ms Opus fixture 在模拟器和真机验证。

### Go / no-go 条件

只有同时满足以下条件，系统 decoder 才进入生产路径：

- 连续解码至少 500 个 20 ms packet，无崩溃、无空洞；
- 每个 20 ms packet 输出 320 个 mono Int16 sample，即 640 bytes；
- interrupt 后 converter 可重置，下一 turn 首帧不携带上一 turn 状态；
- iPhone 真机播放无明显爆音；
- malformed packet 单测返回可识别错误。

若任一条件失败，使用本 ADR 的 fallback：引入固定版本的 libopus XCFramework，
通过最薄的 Swift adapter 调用 `opus_decode`。切换 fallback 不改变 WSS 协议。

## Consequences

### Positive

- 不新增 CocoaPods、vendor SDK、预编译第三方二进制或 C 源码依赖。
- 继续使用现有 `WSAudioFrameDecoder` 注入边界和 `LiveAudioEngine` 播放格式。
- Apple codec 解码后直接输出 16 kHz PCM，减少中间 buffer。
- vendor TTS API 与客户端播放解耦，backend 可独立替换 TTS provider。

### Negative

- `AVAudioCompressedBuffer` 的 packet description 配置比 Swift wrapper 更底层。
- Apple API 不暴露 libopus 的 packet-loss concealment / FEC 控制；本协议基于可靠且
  有序的 WebSocket，因此首版不依赖这些能力。
- 系统实现必须通过真实 packet fixture 和真机验证，不能只凭 API 可构造就判定可用。

### Neutral

- wire codec 仍为 `opus`，PCM 仍保留为协议级 fallback。
- 本 ADR 只决定 decoder 技术路线，不实现 P0-PROTO-05 的实际 iOS 代码。

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| raw Opus packet 的 format / packet description 不正确 | 以 backend 实际输出 fixture 建立 golden test，不使用容器化 Ogg fixture 代替 |
| converter 跨 turn 残留状态 | interrupt 与 `ai.tts.end` 路径销毁并重建 converter |
| 设备实现与模拟器行为不同 | iOS 17 真机作为 go/no-go 必测项 |
| 系统 decoder 不满足低延迟或稳定性要求 | 切换到固定版本 libopus XCFramework adapter，不改协议 |
| 第三方 binary fallback 许可证元数据不足 | 引入前由 owner 核对 package 与上游 libopus 许可证，并归档 notices |

## Alternatives rejected

### Third-party Swift wrapper as the default

拒绝将 `alta/swift-opus` 作为默认方案。它提供 Swift API 和 BSD-3-Clause 许可证，但
最新 release 为 2022-02-28，last code push 为 2024-08-05，README 的 Usage 仍为
TODO。相比系统 codec，它增加了 C 源码编译和维护面。

`sbooth/opus-binary-xcframework` 维护更新，0.3.0 包含 libopus 1.6.1 并支持 iOS 15+，
可作为 fallback；但它是预编译 binary target，不提供 FluentWork 所需的 Swift
decoder contract，且 GitHub API 当前未识别 package 自身许可证，因此不作为首选。
若启用 fallback，必须 pin 0.3.0 及其 SwiftPM checksum，并归档许可证审计结果。

### Volcengine iOS SDK

拒绝。官方双向流式 TTS SDK 是完整的 TTS 网络客户端，通过 CocoaPods
`SpeechEngineToB` 接入，而 FluentWork 已由 backend 调用 seed-tts-2.0 并通过自有 WSS
发送 packet。在 iOS 再引入该 SDK 会形成第二套会话、鉴权和 transport；官方公开 API
未提供可单独嵌入当前 `WSAudioFrameDecoder` 的 raw-packet decoder。

### PCM-only transport

拒绝作为默认值。PCM 可以保留为紧急 fallback，但 24 kHz mono PCM16 的固定码率为
384 kbit/s，明显高于语音 Opus，会持续增加移动网络流量和 gateway egress。

## Follow-up actions

- [ ] P0-PROTO-05：实现可注入的 system Opus decoder 与 MockTTSDecoder。
- [ ] P0-PROTO-05：提交 backend 真实 Opus fixture，并在模拟器与真机运行 go/no-go。
- [ ] P0-PROTO-06：确认 `codec` 默认值保持 `opus`，`pcm` 保留 fallback。
- [ ] 若 system decoder 未通过 go/no-go，单独提交 fallback dependency ADR amendment。
- [ ] W7：独立评估 79_ §七-B.3 的 WSS 写入缓冲池 ADR-0072，不与 decoder 接入耦合。

## References

- Backend WSS V2.0 protocol:
  `docs/30_技术方案/51_WSS_V2_帧协议设计_2026-09-08.md`
- Meta backend architecture review:
  `fluentwork-meta/docs/30_技术方案/79_Backend架构Review_辩证分析与执行校准_2026-09-06.md`
  §四（WSS 冻结清单）与 §七-B.3（WSS 写入缓冲池）
- Apple `AudioConverterNew`:
  <https://developer.apple.com/documentation/audiotoolbox/audioconverternew(_:_:_:)>
- Volcengine 双向流式 TTS iOS SDK:
  <https://docs.volcengine.com/docs/6561/2586552?lang=zh>
- `alta/swift-opus`: <https://github.com/alta/swift-opus>
- `sbooth/opus-binary-xcframework`:
  <https://github.com/sbooth/opus-binary-xcframework>
- GitHub #98: <https://github.com/FluentWork/fluentwork-backend/issues/98>

External package and vendor metadata checked on 2026-09-08.
