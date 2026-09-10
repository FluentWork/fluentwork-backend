# duplex 是什么,以及后端真正消费什么

**日期**：2026-09-10  
**定位**：解释型文档。回答两个反复被问到的问题:duplex 在整条链路里扮演什么角色,以及后端到底关心上传的音频还是它转出来的文本。  
**相关**：`docs/47`（Volc 读上限）、`docs/04`/`06`/`07`（B14 duplex POC）、iOS `docs/40`、meta `docs/30_技术方案/37_B14_Client_ASR_Relay_Architecture.md`

---

## 1. duplex 是什么

**Volc 的 Seeduplex 是「双向流式语音对话」会话,不是「上传音频换文字」的批处理 ASR。**

区别是决定性的:它是一条**长连 WebSocket**,双方持续互推事件,连接生命周期内**同时**做两件事 —— 语音识别**和**对话生成。

| 方向 | 事件 |
|---|---|
| 我们 → Volc | `session.create` / `session.update` / `input_audio_buffer.append`（base64 PCM）/ `input_audio_buffer.commit` / `session.close` |
| Volc → 我们 | `session.created` · `conversation.item.input_audio_transcription.{started,delta,completed}`（**ASR**）· `response.output_text.{delta,done}`（**文本回复**）· `response.output_audio.{started,delta,done}`（**TTS 音频**）· `error` |

所以:**把它当 ASR 用是不够的**。它给的是「听懂 + 回一句」两件事,`response.output_audio.delta` 就是它生成的语音。当前网关**读完即丢**(只用来判断响应是否开始),真正把这些音频送给 iOS 听是 I15 的范围。

**`commit` 是轮次的关键。** 网关在 `user.speech.end` 时调 `input_audio_buffer.commit`,告诉供应商"这段说完了,出结果"。供应商把**上一次 commit 到本次 commit 之间的全部音频**转录成 transcript —— 这也正是 iOS `docs/40` 那个 bug 的机制:客户端在轮次外持续 append,那段音频就被并进下一轮的 transcript。

---

## 2. 一次 turn 的数据流

```
iOS 采集 16kHz mono PCM16
  │  binary 帧（20ms / 640B，或任意分片）
  ▼
voice-gateway  ── base64 后 append ──►  Volc duplex
  │                                        │
  │                                        ├─ ASR ──► transcript ─┐
  │                                        └─ LLM ──► reply ──────┤
  │                                                               │
  ◄───────────── client.asr.transcription / ai.text.delta / ai.turn.end ─┘
```

**网关不解析音频。** 它把二进制帧原样 base64 塞进 `input_audio_buffer.append`,再把供应商的事件翻成 WSS 帧推给 iOS。中间没有编解码、没有重采样、没有落盘。

---

## 3. 后端真正持久化的:只有文本

这是本文最重要的一条,已在代码里核实:

- `EndUtterance` 的结构是 **`{Seq, Speaker, Text}`** —— 没有音频字段（`internal/voicegateway/session_client.go`）
- `utterances` 表**没有** `audio_url` 列
- 全仓**没有任何**把 PCM 写文件或写库的代码
- （`daily_reads` 上确实有 `audio_url`,那是「每日一读」的 TTS 产物,另一条链路,与说的房间无关）

**结论:整条语音链路存在的意义是产出文本。音频是手段,不是目的。** 会话结束后留在系统里的是 transcript + AI 回复 + 命中记录,没有声音。

### 这条结论推出的两件事

**一、音频在协议里是可以缺席的。** `user.speech.end` 本来就带可选 `text` 字段,网关优先用它做命中检测。B13/B14 的 **client ASR relay** 就是这条路径:客户端本地识别出文本直接发上来,网关可以完全跳过供应商。弱网或成本压力下这是现成的降级方案。

**二、回放/重听需要另外存音频。** 现在做不到,因为没有音频留存。PRD 里「点击命中标记重听原句」属于这一类,要做必须先把音频留下来。

---

## 4. 四个「60 秒」分别是什么

排查时最容易混的一组数字:

| 值 | 位置 | 含义 |
|---|---|---|
| **60s** | iOS `recordingAbortTimeout` | **客户端**单轮录音上限。到点发 `client.turn.abort`,会话继续（iOS `docs/24`） |
| **70s** | iOS B15 `turnTimeout` | 已发 `user.speech.end` 后等 `ai.turn.end` 的兜底 |
| **60s** | 网关 `defaultVolcTurnWait` | `user.speech.end` **之后**等供应商出结果的窗口 |
| **60s** | 网关 `keepaliveIdleThreshold` | 上游空闲多久后先探测再转发 |
| **2min** | 网关 `IdleTimeout` | WSS 读空闲超时 |

**服务端没有任何「单轮音频最长 60 秒」的限制。** 已核实:全仓没有对音频时长或累计字节的校验。60 秒是**客户端自己定的产品约束**,不是协议或服务端能力边界。

这意味着「用户每轮最多说 60 秒」这条产品规则,改与不改都只动客户端。

---

## 5. 两个帧大小限制

两端都用 `coder/websocket`,**默认每帧只读 32 KiB**。这个默认值对音频协议太小,两边都踩过:

| 侧 | 常量 | 位置 | 踩的坑 |
|---|---|---|---|
| 网关 ← Volc | `duplexReadLimit = 4 MiB` | `internal/voicepoc/volc_duplex.go` | 助手语音 `response.output_audio.delta` 超过 32 KiB,读失败**每轮断连**（`docs/47`） |
| 网关 ← 客户端 | `maxClientBinaryFrame = 4 MiB` | `internal/voicegateway/handler.go` | 单帧超过 32 KiB 直接踢连接 |

算一下:60 秒 × 16 kHz × 2 字节 = **1,920,000 字节 ≈ 1.83 MiB**。**如果客户端把一轮音频攒成一个帧发上来,旧代码必然失败。** 当前客户端按 20ms（640 字节）分片,所以没暴露;但**协议并没有规定必须分片** —— 分片是客户端的选择,不能当成服务端可以假设的前提。

---

## 6. 对设计的含义

1. **别把分片当协议。** 上表两边都证明了:任何"帧一定很小"的假设都会在真实音频量下崩掉。上限要按**最大可能的单帧**算,不是按当前客户端的行为算。
2. **降级路径现成。** 客户端本地 ASR → `user.speech.end{text}` 已经通,不需要供应商。
3. **想留声音得先决定存哪儿。** 现在音频在网关内存里过一遍就没了。
4. **供应商同时提供 ASR 与回复**,所以它的故障会同时打断"听懂"和"回答"两件事 —— 排查时要分清是哪一段(看 `collect_turn.done` 的 `transcript_len` 与 `assistant_text_len` 是否为空)。
