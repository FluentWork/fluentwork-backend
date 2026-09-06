# P0-PROTO-01 — 协议冻结会议召集 + WSS V2.0 帧字段草稿输出

> **GitHub Title**: `P0-PROTO-01: 协议冻结会议召集 + WSS V2.0 帧字段草稿输出`
> **Labels**: `v2.0-blocker`, `priority/P0`, `protocol-freeze`, `meeting`
> **Milestone**: `V2.0 W3`
> **截止**: 2026-09-06(今晚)23:59
> **Owner**: 后端 TL (会议召集) / iOS Lead (共同决策)
> **关联**: 79_ §四 + §九 行动清单 #1

---

## §0 Issue Body(整段可粘贴到 GitHub)

```markdown
## 🎯 目标
今晚(9/6 周日)召开 30 分钟协议冻结会议, 由后端 TL 召集 iOS Lead 共同决策 WSS V2.0 帧字段, 输出 `scratch/wss-v2-frames-2026-09-06.md` 作为 Issue 03 的输入。

## 🚧 阻塞条件
- iOS Lead 可参会(需 9/6 晚 21:00-22:00 窗口)
- 后端 TL 提供 WSS V1.0 已冻结清单(13 个帧, 见 `schemas/transport/wss-control-frames-v1.json`)

## 📋 会议议程(30 分钟)

| 时间 | 议题 | 负责人 | 产出 |
|---|---|---|---|
| 0-5 min | 现状对齐: V1.0 13 帧清单 + V2.0 增量预测(ai.tts.* 三帧) | 后端 TL | (口头) |
| 5-15 min | ai.tts.start 字段决策: voice_id / sample_rate / codec | 后端 TL | 字段值 |
| 15-25 min | ai.tts.audio 字段决策: Opus bytes / chunk_size / seq 起始值 | 后端 TL | 字段值 |
| 25-30 min | ai.tts.end 字段决策: completion_status / duration_ms | 后端 TL | 字段值 |

## ✅ 验收
- [ ] 会议召开 + 30 分钟内产出 4 个字段决策(3 帧 + 1 codec)
- [ ] 产出文件 `scratch/wss-v2-frames-2026-09-06.md` commit 到 `feature/wss-v2-protocol` 分支
- [ ] 该文件包含: 帧名 / JSON 字段 / 类型 / required / 默认值 / 示例
- [ ] iOS Lead 在文件中签字(用 GitHub 评论 +1 reaction, 或 commit co-author)
- [ ] 后端 TL 在 Issue 03 评论中 @iOS Lead 确认字段决策

## 🔗 依赖
- Blocked by: 无
- Blocks: Issue 02 (会议决议后才能加注释), Issue 03 master (输出文档是 Issue 03 输入)
- 关联文档: `docs/30_技术方案/79_Backend架构Review_辩证分析与执行校准_2026-09-06.md` §四
```

---

## §1 会议预读材料(发给参会者)

**后端 TL 准备**:
1. `internal/voiceproto/frames.go` 当前 13 帧清单(已冻结)
2. `schemas/transport/wss-control-frames-v1.json` JSON Schema
3. `docs/30_技术方案/50_FluentWork全链路架构设计V1_0_2026-09-03.md` §3.4(server_ts_ms 决策点)
4. `docs/30_技术方案/79_Backend架构Review_辩证分析与执行校准_2026-09-06.md` §四 协议冻结清单

**iOS Lead 准备**:
1. iOS 仓现有 WSS 客户端代码(`WSSClient.swift` 帧解析)
2. iOS 端 TTS 播放器现状(AVAudioEngine + Opus 解码选型)
3. 端到端往返延迟容忍度(目标 ≤ 400ms 首字, P90 ≤ 800ms 整句)

---

## §2 产出文件模板(`scratch/wss-v2-frames-2026-09-06.md`)

```markdown
# WSS V2.0 帧字段冻结草稿(2026-09-06)

**出席**: 后端 TL / iOS Lead
**产出**: 本文件作为 Issue 03 master 的输入, 评审通过后纳入 `schemas/transport/wss-control-frames-v2.json`

## 1. ai.tts.start 帧

| 字段 | 类型 | required | 默认值 | 决策 |
|---|---|---|---|---|
| type | const "ai.tts.start" | ✅ | "ai.tts.start" | (V1 已有) |
| turn_id | string | ✅ | (无) | 与 ai.text.delta 一致 |
| voice_id | string | ✅ | (无) | 取自 voices.go VoiceAIMaleTech 等常量 |
| sample_rate | int (Hz) | ✅ | 24000 | 火山 TTS 默认输出 |
| codec | enum ["opus", "pcm"] | ✅ | "opus" | 节省带宽 70% |

## 2. ai.tts.audio 帧

| 字段 | 类型 | required | 默认值 | 决策 |
|---|---|---|---|---|
| type | const "ai.tts.audio" | ✅ | "ai.tts.audio" | (新增) |
| turn_id | string | ✅ | (无) | 与 ai.tts.start 一致 |
| seq | int | ✅ | (无) | 从 0 开始, 单调递增 |
| data | base64 bytes | ✅ | (无) | Opus 编码帧(20ms / 960 samples @ 24kHz) |

## 3. ai.tts.end 帧

| 字段 | 类型 | required | 默认值 | 决策 |
|---|---|---|---|---|
| type | const "ai.tts.end" | ✅ | "ai.tts.end" | (新增) |
| turn_id | string | ✅ | (无) | 与 ai.tts.audio 最后一帧一致 |
| completion_status | enum ["ok", "interrupted", "error"] | ✅ | "ok" | 与 ai.turn.end outcome 对齐 |
| duration_ms | int | ❌ | (无) | 可选, 用于客户端 metrics |

## 4. 50_ server_ts_ms 决策

| 选项 | 决定 |
|---|---|
| ai.text.delta 加 server_ts_ms 字段 | ✅ 是(便于客户端打点) |
| ai.audio.chunk 是否也加 server_ts_ms | ❌ 否(冗余, 用 turn_id + seq 即可) |
| 字段类型 | int64 (Unix 毫秒) |

## 5. 签字

- 后端 TL: ____________
- iOS Lead: ____________
```

---

## §3 gh CLI 落库命令(评审通过后)

```bash
cd /Users/apple/Developments/FluentWork\ App/fluentwork-backend

# 1. 建仓
gh issue create \
  --title "P0-PROTO-01: 协议冻结会议召集 + WSS V2.0 帧字段草稿输出" \
  --label "v2.0-blocker,priority/P0,protocol-freeze,meeting" \
  --milestone "V2.0 W3" \
  --body-file .scratch/issues/2026-09-06-W3-protocol-freeze/01-protocol-freeze-meeting.md

# 2. 拿到 issue number 后, 关联到 Project 看板
gh issue edit <NEW_ISSUE_NUM> --add-project "V2.0 W3 Protocol Freeze"
```

---

## §4 风险与回滚

| 风险 | 应对 |
|---|---|
| iOS Lead 今晚无法参会 | 顺延到 9/7 上午, 但 Issue 02 注释与 Issue 03 master 启动同步顺延 |
| 帧字段决策未达成一致 | 记录分歧点, 启动 Issue 04 提前, 9/8 前必须决议 |
| 会议超时 | 严格 30 分钟, 未决议项降级到 Issue 04 决策评审 |