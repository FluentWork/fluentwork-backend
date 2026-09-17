# B17 TTS Provider 实现说明

**对应 issue**：#44 / #52–#56  
**代码**：`92fe324` `06106ed` `7c940ca` `91020ef` `0dcad87`（本提交补说明与过期包注释）  
**门禁**：`go test ./internal/content/tts/...` 与全仓 `./scripts/dev-check.sh`

## 原理与背景

iOS I15 和每日一读 B20 需要一条**独立于实时对话 WSS** 的 TTS 路径：给定文本，流式拿音频。实时会话里的双工 TTS 音色固定、和 ASR 绑在一起，不能当通用合成接口。

火山侧实际有两条能力：

1. HTTP/2 单向 StreamTTS（`/api/v3/tts/unidirectional`）—— 按 speaker 合成，适合朗读 / 闪测倒计时
2. 已落地的 duplex 会话 TTS —— 失败兜底，音色固定为 `zh_female_vv_jupiter_bigtts`

C-2 的「voice_id = X-Api-Resource-Id」在查表后不成立：Resource-Id 是产品 SKU（`seed-tts-2.0`），真正换音色的是 `req_params.speaker`。

## 方案

`internal/content/tts`：

| 层 | 职责 |
| --- | --- |
| `Provider` | `Stream` / `Ping` / `Close`。`AudioChunk` 带 0-based `Seq`、`IsFinal`、`DetectedAt`（首字节毫秒，给 TTFB） |
| `VolcStreamingProvider` | `net/http` HTTP/2，无 SDK。5xx 自动再打 1 次。ctx cancel 关闭 channel |
| `VolcDuplexFallbackProvider` | `voicepoc.OpenDuplex`，只消费 `response.output_audio.*`，连接复用 |
| `Manager` | 主路径连续 3 次 HTTP 5xx → 切 fallback，打 `tts_fallback_triggered_total{from="volc_streaming",to="volc_duplex"}` |
| `voices.go` | D-2 四套 speaker + speed；未知 id 原样透传 |
| `POST /internal/v1/tts/synthesize` | 内网 token；`voice_id` 可走 JSON 或 query |

app-server：`VOLC_SPEECH_API_KEY` 为空则不接线，synthesize 返回 UNAVAILABLE。探测脚本：`scripts/check-volc-voice-resource-ids.sh`。

## 缺口根因

票面把 speaker id 和 `X-Api-Resource-Id` 写成同一个「真 resource_id」。按那个去填 Header，四套音色会打到错误 SKU。根因是把火山产品 SKU 和 speaker 目录混成一列。

GitHub #44 一直 OPEN，是因为实现分 5 个 commit 合进 `main` 后没有关单、也没有实现说明。功能本身已在 `0dcad87` 收口。

## 新方案理由

- **SKU 与 speaker 分开**：Header 固定 `seed-tts-2.0`（可用 `VOLC_SPEECH_RESOURCE_TTS` 覆盖），`VoiceConfig.VoiceID` 只进 `req_params.speaker`。
- **Manager 而不是调用方自己切**：HTTP handler 和以后的 B20 批处理共用同一套 5xx×3 策略，metric 只有一处递增。
- **双工只做 fallback**：主路径要可选音色；双工会话的 TTS 不能换 D-2 catalog，所以不放主路径。
- **内网 synthesize 聚合音频**：iOS 流式播放仍走 chunk channel；HTTP 给运营 / 冒烟把 chunk 拼成 `audio_base64`，避免再开一条 WSS。

## 明确不做 / 本环境未跑

- CI 里打真实火山端到端和「staging 10 次首字 P90 ≤ 400ms」—— 需要本机 `VOLC_SPEECH_API_KEY`。单测用 httptest 覆盖成功流、cancel、5xx 重试与 fallback。凭证到位后跑 `scripts/check-volc-voice-resource-ids.sh`。
- OpenCodeReview / maintainer PR：当前工作流是 `main` 直推，不作为关单条件。
