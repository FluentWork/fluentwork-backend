# I20 Item 2：dev-echo PCM fixture 接到进程启动

**票**：I20 Item 2（`docs/i20-fix-plan.md` §三）。  
**状态**：已落地。`DevEchoVoiceProvider` 本来就能播 fixture；缺口是网关进程读不到路径。  
**关联**：不是 B15；不是 iOS 手动开口（Item 4）。

## 1. 要守住的原理

本地不接火山时，`user.speech.end` 之后仍要能听到一段 16 kHz mono PCM，用来走播放链路。这条路径必须能从**进程配置**打开，而不是只在单测里给 `provider.Fixture` 赋值。

1. `VOICE_DEV_ECHO_FIXTURE=/path/to.pcm`（或 WAV）→ `LoadConfig` → `NewVoiceProvider` 读入字节
2. `cmd/voice-gateway --dev-echo-fixture PATH` 覆盖环境变量
3. `VOICE_DEV_ECHO_TTS_MOCK=true` 时不加载 PCM，避免和冻结 `ai.tts.*` 混流
4. 文件缺失只 Warn，进程继续（badge / ASR echo 仍可用）

## 2. 根因

`FixturePath` / `FixturePCMLoader` / handler E2E 已经存在。`CLAUDE.md` 也写了 `VOICE_DEV_ECHO_FIXTURE`。但 `Config` 没有这个字段，`NewVoiceProvider` 从不赋值，CLI 也没有 flag。文档是真的，启动路径是假的。

## 3. 方案与文件

| 文件 | 职责 |
|---|---|
| `config.go` | `DevEchoFixturePath` + `ApplyDevEchoFixtureFlag` |
| `provider_factory.go` | 启动时 `FixturePCMLoader`，写入 `Fixture` |
| `cmd/voice-gateway/main.go` | `--dev-echo-fixture` |

不提交 5s 二进制。单测用 `DevEchoFixtureGenerator` 写临时文件。本地任意 16 kHz mono s16le PCM / 标准 WAV 即可。

## 4. 为何不折进已有路径

不把 fixture 塞进 `MockVoiceProvider`：mock 是空 ASR。不在 Open() 里每次读盘：WAV 头剥离和缺文件应在进程启动时做一次。

iOS `PlaceholderAudioEngine.injectPCM` 不在本票：那是客户端假采集；本票是网关把 fixture 打回 WSS。

## 5. 影响面

- 状态：无 session 相位变化
- 协议：仍是现有 binary audio + `ai.turn.end`
- 音频：仅 `dev-echo` 且非 TTS mock 时回放文件
- 发布：生产不要选 `dev-echo`

## 6. 测试

```bash
go test ./internal/voicegateway/ -run 'DevEchoFixture|FixturePCMLoader|ApplyDevEchoFixture'
go test ./...
go build ./...
```
