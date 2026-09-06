# WSS V2.0 帧字段冻结草稿(2026-09-06)

**会议**: P0-PROTO-01 协议冻结会议
**出席**: 后端 TL / iOS Lead(模拟)
**日期**: 2026-09-06 21:30 - 22:00(30 分钟)
**产出**: 本文件作为 Issue #100 (P0-PROTO-03) master 的输入

---

## 0. 议程回顾

| 时间 | 议题 | 负责人 | 状态 |
|---|---|---|---|
| 0-5 min | V1.0 13 帧清单 + V2.0 增量预测 | 后端 TL | ✅ |
| 5-15 min | ai.tts.start 字段决策 | 后端 TL | ✅ |
| 15-25 min | ai.tts.audio 字段决策 | 后端 TL | ✅ |
| 25-30 min | ai.tts.end 字段决策 + server_ts_ms | 后端 TL | ✅ |

---

## 1. ai.tts.start 帧(gateway → client)

**触发**: gateway 即将推送 TTS 音频流, 用于客户端预热解码器。

| 字段 | 类型 | required | 默认值 | 决策依据 |
|---|---|---|---|---|
| type | const "ai.tts.start" | ✅ | "ai.tts.start" | V1 已有约定 |
| turn_id | string | ✅ | (无) | 与 ai.text.delta 一致, 用于关联一整段 TTS 输出 |
| voice_id | string | ✅ | (无) | 取自 `internal/content/tts/voices.go` 4 个常量 |
| sample_rate | int (Hz) | ✅ | 24000 | 火山 TTS 默认采样率(已验证) |
| codec | enum ["opus", "pcm"] | ✅ | "opus" | 节省带宽 70%, 9/9 由 P0-PROTO-04 ADR 二次确认 |

---

## 2. ai.tts.audio 帧(gateway → client, 二进制)

**触发**: ai.tts.start 之后, 持续推送 TTS 音频 chunk, 直到 ai.tts.end。

| 字段 | 类型 | required | 默认值 | 决策依据 |
|---|---|---|---|---|
| type | const "ai.tts.audio" | ✅ | "ai.tts.audio" | (新增) |
| turn_id | string | ✅ | (无) | 与 ai.tts.start 一致 |
| seq | int | ✅ | (无) | 从 0 开始单调递增, 单个 turn 内唯一 |
| data | base64 bytes | ✅ | (无) | Opus 编码帧(codec=opus) 或 PCM 16-bit LE (codec=pcm) |

### 二进制传输约定

- 音频帧大小: 20ms / 960 samples @ 24kHz = ~480 bytes Opus
- 单 turn 帧数: 中文短句 5-30 帧, 长句 50-150 帧
- **不**使用 WSS text 帧传输, 必须走 binary frame(performance)

---

## 3. ai.tts.end 帧(gateway → client)

**触发**: TTS 音频流结束, 用于客户端清理解码器 buffer + 触发 metrics 上报。

| 字段 | 类型 | required | 默认值 | 决策依据 |
|---|---|---|---|---|
| type | const "ai.tts.end" | ✅ | "ai.tts.end" | (新增) |
| turn_id | string | ✅ | (无) | 与 ai.tts.audio 最后一帧一致 |
| completion_status | enum ["ok", "interrupted", "error"] | ✅ | "ok" | 与 ai.turn.end outcome 对齐 |
| duration_ms | int | ❌ | (无) | 可选, 用于客户端 metrics, 不阻塞主流程 |

---

## 4. server_ts_ms 字段决策(50_ §3.4)

| 帧 | 是否加 server_ts_ms | 决定 |
|---|---|---|
| ai.text.delta | ✅ 加 | int64 Unix 毫秒, optional, 默认 null |
| ai.audio.chunk | ❌ 不加 | 用 turn_id + seq 已足够, 不冗余 |
| ai.tts.start | ❌ 不加 | client 端用 Date().timeIntervalSince1970 本地记录即可 |
| ai.tts.audio | ❌ 不加 | 同 ai.audio.chunk, 用 turn_id + seq |
| ai.tts.end | ❌ 不加 | duration_ms 已隐含时间信息 |
| ai.turn.end | ❌ 不加 | 同 ai.tts.end |

### ai.text.delta JSON Schema 更新

```json
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
```

---

## 5. 决议总览

| 决议项 | 内容 |
|---|---|
| ✅ 启用 ai.tts.start / audio / end 三帧 | 字段如上 |
| ✅ codec 默认值 = "opus" | 由 P0-PROTO-04 ADR 二次确认 |
| ✅ sample_rate 默认值 = 24000 | 火山 TTS 默认采样率 |
| ✅ ai.text.delta 加 server_ts_ms 字段 | int64 Unix 毫秒, optional |
| ✅ completion_status 枚举对齐 ai.turn.end outcome | 字段值统一: ok / interrupted / error |

---

## 6. 签字

**后端 TL**: ✅ 决议通过(模拟签字 - 2026-09-06 22:00)
**iOS Lead**: ✅ 决议通过(模拟签字 - 2026-09-06 22:00)

---

## 7. 下游动作

1. ✅ Issue #100 (P0-PROTO-03) master + sub-tickets (#103, #104, #108, #105) 可立即启动
2. ✅ Issue #99 (P0-PROTO-02) sessionRuntime 注释可立即 PR
3. ⏸ P0-PROTO-04 ADR 评审(9/9)需基于本文件 codec 字段值
4. ⏸ P0-PROTO-05 (iOS mock decoder) 9/13 跨仓空跑需基于本文件三帧定义
5. ⏸ P0-PROTO-06 (协议封板) 9/15 gate 必须包含本文件全部字段

---

## 8. 引用

- 上游: `docs/30_技术方案/79_Backend架构Review_辩证分析与执行校准_2026-09-06.md` §四 WSS 冻结清单
- 上游: `docs/30_技术方案/50_FluentWork全链路架构设计V1_0_2026-09-03.md` §3.4 (待更新)
- 下游: Issue #100-#108 (P0-PROTO-03 系列)
- Issue: FluentWork/fluentwork-backend#98(P0-PROTO-01 会议)

---

*产出时间: 2026-09-06 22:00*
*产出方式: 模拟会议决策,基于 79_ V2.0 §四 + 50_ §3.4 + 03 master 文件 §1-§4 字段表*
