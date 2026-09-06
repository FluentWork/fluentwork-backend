# P0-PROTO-03 — WSS V2.0 `ai.tts.*` 帧协议 Markdown 草稿 + server_ts_ms 决策

> **Master**: 待新建 `P0-PROTO-03: WSS V2.0 ai.tts.* 帧协议 Markdown 草稿`
> **Sub-tickets**: 4 (P0-PROTO-03.1 / 03.2 / 03.3 / 03.4)
> **总工时**: 4 dev-hour(分散在 9/7-9/8)
> **阻塞**: Issue 01 会议产出 + Issue 04 SDK 决策(只读, 不阻塞字段定义)
> **关联**: 79_ V2.0 §四 / 50_ §3.4 / 47_ §1.6.1 / I15 iOS TTS 播放

---

## §0 Master Issue Body(整段可粘贴)

**Title**: `P0-PROTO-03: WSS V2.0 ai.tts.* 帧协议 Markdown 草稿 + server_ts_ms 决策`
**Labels**: `v2.0-blocker`, `priority/P0`, `protocol-freeze`, `wss-v2`, `docs`
**Milestone**: `V2.0 W3`

```markdown
## 🎯 目标
基于 Issue 01 会议产出, 把 WSS V2.0 帧协议(ai.tts.start / ai.tts.audio / ai.tts.end 三帧 + ai.text.delta 的 server_ts_ms 决策)落到:
1. Markdown 设计文档 `docs/30_技术方案/51_WSS_V2_帧协议设计_2026-09-08.md`
2. (后续) `internal/voiceproto/frames.go` 新增 3 个结构体
3. (后续) `schemas/transport/wss-control-frames-v2.json` 新增 3 个 schema

本 Issue 只负责 1, 3 在 Issue 06 封板 gate 后单独建仓。

## 🚧 阻塞条件
- Issue 01 会议产出 `scratch/wss-v2-frames-2026-09-06.md`
- iOS Lead review(本 Issue 涉及 iOS 端兼容性)

## 📐 Sub-tickets(Skills-to-Ticket 切分)

| # | Ticket | 工时 | 阻塞 |
|---|---|---|---|
| 03.1 | `ai.tts.start` 帧字段定义 + Markdown section | 1h | 无 |
| 03.2 | `ai.tts.audio` 帧字段定义 + Markdown section | 1.5h | 03.1 |
| 03.3 | `ai.tts.end` 帧字段定义 + Markdown section | 0.5h | 03.2 |
| 03.4 | `ai.text.delta` server_ts_ms 决策落地 + 50_ §3.4 更新 | 1h | 无 |

## ✅ Master 验收
- [ ] 4 个 sub-ticket 全部完成并 PR merged
- [ ] `docs/30_技术方案/51_WSS_V2_帧协议设计_2026-09-08.md` 文件存在
- [ ] 文档包含: ai.tts.start / audio / end 三帧的完整字段表 + JSON Schema 草案 + iOS 端集成示例
- [ ] 50_ §3.4 的 server_ts_ms 决策落地(是/否 + 字段类型 + 默认值)
- [ ] iOS Lead 在 PR 评论中 approve
- [ ] 文档 PR 编号引用: 79_ V2.0 §四 + §九

## 🔗 关联
- 上游: Issue 01 会议
- 下游: Issue 04 (SDK 决策基于本协议文档) + Issue 05 (iOS mock decoder 基于本协议)
- 后续(不在本 Issue 范围): Issue 06 封板 gate 触发 voiceproto 代码与 JSON Schema 落库
```

---

## §1 P0-PROTO-03.1 — `ai.tts.start` 帧字段定义

**Labels**: `v2.0-blocker`, `priority/P0`, `protocol-freeze`, `wss-v2`
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
在 51_ WSS V2.0 帧协议设计文档中, 起草 `ai.tts.start` 帧的字段定义 Markdown section。

## 📋 实施步骤
1. 新建文件 `docs/30_技术方案/51_WSS_V2_帧协议设计_2026-09-08.md`, 包含以下 section:

```markdown
### 2.1 ai.tts.start 帧(gateway → client)

**触发**: gateway 即将推送 TTS 音频流, 用于客户端预热解码器。

| 字段 | 类型 | required | 默认值 | 说明 |
|---|---|---|---|---|
| type | const "ai.tts.start" | ✅ | "ai.tts.start" | 帧类型常量 |
| turn_id | string | ✅ | (无) | 与 ai.text.delta 一致, 用于关联一整段 TTS 输出 |
| voice_id | string | ✅ | (无) | 音色标识, 取自 `internal/content/tts/voices.go` 的 4 个常量 |
| sample_rate | int (Hz) | ✅ | 24000 | 火山 TTS 默认采样率 |
| codec | enum ["opus", "pcm"] | ✅ | "opus" | 音频编码格式, opus 节省带宽 70% |

#### JSON Schema 草案
\`\`\`json
{
  "type": "object",
  "required": ["type", "turn_id", "voice_id", "sample_rate", "codec"],
  "additionalProperties": false,
  "properties": {
    "type": { "const": "ai.tts.start" },
    "turn_id": { "type": "string", "minLength": 1 },
    "voice_id": { "type": "string", "minLength": 1 },
    "sample_rate": { "type": "integer", "enum": [16000, 24000, 48000] },
    "codec": { "type": "string", "enum": ["opus", "pcm"] }
  }
}
\`\`\`

#### iOS 端集成示例
\`\`\`swift
case "ai.tts.start":
    let start = try decoder.decode(AITTSStart.self, from: data)
    try ttsDecoder.prepare(
        voiceId: start.voice_id,
        sampleRate: start.sample_rate,
        codec: start.codec
    )
\`\`\`
```

2. (本 Issue 不提交 voiceproto 代码, 仅 Markdown)

## ✅ 验收
- [ ] 51_ 文档创建并 commit 到 `feature/wss-v2-protocol` 分支
- [ ] Markdown section 字段表与 §0 模板**逐字一致**
- [ ] JSON Schema 草案字段与字段表**严格对应**(无遗漏, 无新增)
- [ ] iOS 端示例代码语法可编译(Swift 5.9+)
- [ ] 引用 79_ §四 + Issue 01 会议产出

## 🔗 依赖
- Blocked by: Issue 01 会议产出
- Blocks: 03.2(ai.tts.audio 必须引用 voice_id + sample_rate + codec)
- Master: P0-PROTO-03
```

---

## §2 P0-PROTO-03.2 — `ai.tts.audio` 帧字段定义

**Labels**: `v2.0-blocker`, `priority/P0`, `protocol-freeze`, `wss-v2`
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
在 51_ 文档中, 起草 `ai.tts.audio` 帧字段定义 + 流式 Opus 数据约定。

## 📋 实施步骤
追加以下 section 到 51_ 文档:

```markdown
### 2.2 ai.tts.audio 帧(gateway → client, 二进制)

**触发**: ai.tts.start 之后, 持续推送 TTS 音频 chunk, 直到 ai.tts.end。

| 字段 | 类型 | required | 默认值 | 说明 |
|---|---|---|---|---|
| type | const "ai.tts.audio" | ✅ | "ai.tts.audio" | 帧类型常量 |
| turn_id | string | ✅ | (无) | 与 ai.tts.start 一致 |
| seq | int | ✅ | (无) | 从 0 开始单调递增, 单个 turn 内唯一 |
| data | base64 bytes | ✅ | (无) | Opus 编码帧(codec=opus) 或 PCM 16-bit LE (codec=pcm) |

#### 二进制传输约定
- 音频帧大小: 20ms / 960 samples @ 24kHz = ~480 bytes Opus
- 单 turn 帧数: 中文短句 5-30 帧, 长句 50-150 帧
- **不**使用 WSS text 帧传输, 必须走 binary frame(performance)

#### JSON Schema 草案
\`\`\`json
{
  "type": "object",
  "required": ["type", "turn_id", "seq", "data"],
  "additionalProperties": false,
  "properties": {
    "type": { "const": "ai.tts.audio" },
    "turn_id": { "type": "string", "minLength": 1 },
    "seq": { "type": "integer", "minimum": 0 },
    "data": { "type": "string", "contentEncoding": "base64" }
  }
}
\`\`\`

#### iOS 端集成示例
\`\`\`swift
case "ai.tts.audio":
    let audio = try decoder.decode(AITTSAudio.self, from: data)
    let opusBytes = Data(base64Encoded: audio.data)!
    try ttsDecoder.feed(
        seq: audio.seq,
        bytes: opusBytes,
        turnId: audio.turn_id
    )
\`\`\`
```

## ✅ 验收
- [ ] Markdown section 与 §0 模板**逐字一致**
- [ ] 二进制传输约定明确(20ms / 960 samples)
- [ ] 引用 03.1 的 voice_id / sample_rate / codec 字段, 无重复定义
- [ ] JSON Schema 草案字段一致
- [ ] iOS 示例代码可编译

## 🔗 依赖
- Blocked by: P0-PROTO-03.1
- Blocks: 03.3(ai.tts.end 必须引用 turn_id 与 seq 范围)
- Master: P0-PROTO-03
```

---

## §3 P0-PROTO-03.3 — `ai.tts.end` 帧字段定义

**Labels**: `v2.0-blocker`, `priority/P0`, `protocol-freeze`, `wss-v2`
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
在 51_ 文档中, 起草 `ai.tts.end` 帧字段定义。

## 📋 实施步骤
追加以下 section 到 51_ 文档:

```markdown
### 2.3 ai.tts.end 帧(gateway → client)

**触发**: TTS 音频流结束, 用于客户端清理解码器 buffer + 触发 metrics 上报。

| 字段 | 类型 | required | 默认值 | 说明 |
|---|---|---|---|---|
| type | const "ai.tts.end" | ✅ | "ai.tts.end" | 帧类型常量 |
| turn_id | string | ✅ | (无) | 与 ai.tts.audio 最后一帧一致 |
| completion_status | enum ["ok", "interrupted", "error"] | ✅ | "ok" | 与 ai.turn.end outcome 对齐(80_ §五) |
| duration_ms | int | ❌ | (无) | 可选, 实际音频时长(去静音), 客户端 metrics 用 |

#### JSON Schema 草案
\`\`\`json
{
  "type": "object",
  "required": ["type", "turn_id", "completion_status"],
  "additionalProperties": false,
  "properties": {
    "type": { "const": "ai.tts.end" },
    "turn_id": { "type": "string", "minLength": 1 },
    "completion_status": { "type": "string", "enum": ["ok", "interrupted", "error"] },
    "duration_ms": { "type": "integer", "minimum": 0 }
  }
}
\`\`\`

#### iOS 端集成示例
\`\`\`swift
case "ai.tts.end":
    let end = try decoder.decode(AITTSEnd.self, from: data)
    ttsDecoder.finish(
        turnId: end.turn_id,
        status: end.completion_status,
        durationMs: end.duration_ms
    )
    // 触发 metrics: tts_played_duration_total{status="ok|interrupted|error"}
\`\`\`
```

## ✅ 验收
- [ ] Markdown section 与 §0 模板**逐字一致**
- [ ] completion_status 枚举与 ai.turn.end outcome 字段一致(避免双定义)
- [ ] duration_ms 字段标注为可选, 不阻塞主流程
- [ ] 引用 03.1 + 03.2 的字段, 形成完整 TTS 帧序列

## 🔗 依赖
- Blocked by: P0-PROTO-03.2
- Blocks: 无(03.4 server_ts_ms 决策独立)
- Master: P0-PROTO-03
```

---

## §4 P0-PROTO-03.4 — `ai.text.delta` server_ts_ms 决策落地 + 50_ §3.4 更新

**Labels**: `v2.0-blocker`, `priority/P0`, `protocol-freeze`, `wss-v2`, `meta-doc`
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
基于 79_ V2.0 §四 WSS 冻结清单 #4, 决议 `ai.text.delta` 是否携带 `server_ts_ms` 字段, 并更新 `50_FluentWork全链路架构设计V1_0_2026-09-03.md` §3.4。

## 🚧 阻塞条件
- iOS Lead 决策(用于客户端 metrics 与 RTT 监控)

## 📋 实施步骤
1. 决议(写入 51_ 文档):

```markdown
### 2.4 server_ts_ms 字段决策

| 帧 | 是否加 server_ts_ms | 决定 |
|---|---|---|
| ai.text.delta | ✅ 加 | int64 Unix 毫秒, optional, 默认 null |
| ai.audio.chunk | ❌ 不加 | 用 turn_id + seq 已足够, 不冗余 |
| ai.tts.start | ❌ 不加 | client 端用 Date().timeIntervalSince1970 本地记录即可 |
| ai.tts.audio | ❌ 不加 | 同 ai.audio.chunk, 用 turn_id + seq |
| ai.tts.end | ❌ 不加 | duration_ms 已隐含时间信息 |
| ai.turn.end | ❌ 不加 | 同上 |

#### ai.text.delta JSON Schema 更新
\`\`\`json
{
  "type": "object",
  "required": ["type", "text"],
  "additionalProperties": false,
  "properties": {
    "type": { "const": "ai.text.delta" },
    "text": { "type": "string" },
    "turn_id": { "type": "string" },
    "server_ts_ms": { "type": "integer", "minimum": 0 }
  }
}
\`\`\`
```

2. 更新 `docs/30_技术方案/50_FluentWork全链路架构设计V1_0_2026-09-03.md` §3.4:

把原文的「待决议」段落替换为:

> **§3.4 server_ts_ms 决策(2026-09-08 落地)**: ai.text.delta 帧加 server_ts_ms 字段(int64 Unix 毫秒, optional), 用于客户端 metrics 与 RTT 监控; ai.audio.* / ai.tts.* 帧不加, 用 turn_id + seq 表达顺序关系。

3. 51_ 文档 PR 引用 50_ §3.4 的更新 commit。

## ✅ 验收
- [ ] 51_ 文档 §2.4 段落存在, 表格 6 行完整
- [ ] 50_ §3.4 段落已更新, git diff 显示具体改动
- [ ] iOS Lead 在 PR 评论中确认字段类型(int64)与精度(毫秒)
- [ ] 与 79_ V2.0 §四 WSS 冻结清单 #4 决议一致

## 🔗 依赖
- Blocked by: 无(可与 03.1/03.2/03.3 并行)
- Blocks: 无
- Master: P0-PROTO-03
- 跨仓关联: meta 仓 50_ 文档(本仓 backend 仓 51_ 文档)
```

---

## §5 实施顺序与总工时

```
9/7 (周一):
  03.1 (1h) ──▶ 03.2 (1.5h) ──▶ 03.3 (0.5h)
                                     │
  03.4 (1h, 与 03.1-03.3 并行) ──────┴──▶ 03 master 验收

9/8 (周二):
  评审通过 + PR merge
  iOS Lead 在 PR 评论中 approve
  51_ 文档 commit 到 feature/wss-v2-protocol 分支
```

**总工时**: 4 dev-hour
**推荐 Owner**: 后端语音组 + iOS Lead 协作
**启动建议**: 9/7 上午先做 03.1, 03.4 可并行启动

---

## §6 不在本 Issue 范围内

- ❌ 不修改 `internal/voiceproto/frames.go`(留给 Issue 06 封板 gate 后单独建仓)
- ❌ 不修改 `schemas/transport/wss-control-frames-v1.json`(同样留给封板 gate)
- ❌ 不实施 B17 TTS Provider 代码(完全独立的 W3-backend-tickets 范畴)
- ❌ 不写 iOS 端实际代码(留给 Issue 05)

任何范围蔓延都需关闭本 Issue 并开新 Issue。