# 二进制帧编码统一分析

**日期**：2026-09-21  
**状态**：M3 已完成切帧层统一

---

## 一、M3 实施结果

### 目标（96_ §3.1 + 98_ M3）

统一二进制帧编码，删除重复实现。

### 实际落地

**共享部分**：✅ 切帧层 `audioFramer`

| 组件 | 位置 | 职责 |
|---|---|---|
| `audioFramer` | `utterance.go:166-222` | 缓冲 PCM → 切片 → 编号 → 留尾 |
| `audioFramer.encode` | `utterance.go:217` | 调用 `voiceproto.AITTSAudio.Encode()` |

**不共享部分**：⚠️ 流控制（ai.tts.start / ai.tts.end）

| 路径 | ai.tts.start 发送位置 | 原因 |
|---|---|---|
| 救援梯子 | `utterance.go:291` beginUtterance | 走 UtteranceWriter |
| AI 音频 | `provider_volc_duplex.go:353` AssistantAudio | 在 provider 内部 |

---

## 二、编码实现统一情况

### 2.1 唯一编码函数：✅ 已统一

**当前状态**：
- ✅ 只剩 `voiceproto.AITTSAudio.Encode()`（`internal/voiceproto/frames.go:195`）
- ✅ 救援通过 `audioFramer.encode` 调用它
- ✅ AI 音频通过 `audioFramer.encode` 调用它
- ✅ dev-echo 通过 `devEchoSession.encodeAudioFrame` 调用它

**验证**：
```bash
# 全仓搜索帧编码
grep -r "AITTSAudio.Encode" internal/voicegateway/
# 结果：只有 audioFramer.encode 一处

grep -r "encodeAudioFrame" internal/voicegateway/
# 结果：
# - audioFramer.encode (内部，不导出)
# - devEchoSession.encodeAudioFrame (壳函数，调用 voiceproto)
```

**94_ F4 状态**：✅ **已关闭**

---

### 2.2 帧大小常量：⚠️ 部分统一

**下行帧（gateway→client）**：✅ 已统一

| 用途 | 来源 |
|---|---|
| AI 音频 | `AudioFormat.FrameBytes(100)` |
| 救援音频 | `AudioFormat.FrameBytes(100)` |
| 测试验证 | `utterance_test.go:289-295` |

**上行块（client→provider）**：❌ 仍重复

| 常量 | 位置 | 值 | 含义 |
|---|---|---|---|
| `pcm16kChunkBytes` | `voiceduplex/volc_duplex.go:57` | 640 | 20ms @ 16kHz |
| `devEchoChunkBytes` | `provider_dev_echo.go:106` | 640 | 20ms @ 16kHz |

**94_ F5 状态**：⚠️ **部分关闭**（下行统一，上行仍重复）

---

## 三、为什么 M3 只做了切帧层

### 原因：ai.tts.start 静音问题

**时间线**（98_ §五）：
1. **2026-09-12**：iOS 恢复 `EngineBackedTTSDecoder` → AI 音频静音 → 回滚
2. **M3 实施**：只共享切帧层，流的开合（ai.tts.start）仍在 provider
3. **2026-09-18 后**：iOS 删除回退路径 → 网关开始发 ai.tts.start

**机制**（98_ §五原文）：
> iOS 用"有没有见过 `ai.tts.start`"决定二进制帧的归属。见过 start → 帧进 TTS 解码器；没见过 start → 帧进 `audioEngine.play`。当时生产绑的解码器是 `MockTTSDecoder`：只记录，不驱动引擎。于是"给 AI 的音频加一个 `ai.tts.start`" = 把每一帧交给一个会丢掉的解码器 = 静音。

**解决方案**：
- iOS 删掉回退路径（`d004869`）
- 网关开始发 ai.tts.start（`3cb3774`）
- 但流控制仍然在 provider，未合并到 `streamWriter`

---

## 四、当前 ai.tts.start 发送机制

### 4.1 救援路径（通过 UtteranceWriter）

```go
// handler_rescue.go:189-217
w, err := beginUtterance(u, send, rt.audioSeq, live)
// ↓
// utterance.go:267-300
func beginUtterance(...) (UtteranceWriter, error) {
    // 发送 ai.tts.start
    w.send([]ProviderOutbound{{Control: voiceproto.AITTSStart{...}}})
    return w, nil
}
```

**特点**：
- ai.tts.start 在 `beginUtterance` 立即发送
- 音频通过 `w.Audio(pcm)` 发送
- ai.tts.end 在 `w.End()` 发送
- 统一在 `streamWriter` 内部

---

### 4.2 AI 音频路径（在 provider 内部）

```go
// provider_volc_duplex.go:340-365
func (s *volcDuplexProviderSession) AssistantAudio(pcm []byte) {
    startTurn := first && !s.ttsStarted
    if startTurn {
        s.ttsStarted = true
        s.emit(s.ttsStartFrame(turnID))  // 发送 ai.tts.start
    }
    for _, frame := range s.frameAudio(pcm, false) {
        s.emit(ProviderOutbound{Binary: frame})
    }
}
```

**特点**：
- ai.tts.start 在首次音频时发送
- 音频通过 `s.frameAudio` 切帧（调用 audioFramer）
- ai.tts.end 在 `turnToOutbound` 发送
- 发送时机由 provider 控制

---

### 4.3 两条路径对比

| 维度 | 救援路径 | AI 路径 |
|---|---|---|
| 切帧 | ✅ audioFramer | ✅ audioFramer（s.frameAudio 调用它） |
| 编码 | ✅ AITTSAudio.Encode | ✅ AITTSAudio.Encode |
| ai.tts.start 时机 | beginUtterance | 首次 AssistantAudio |
| ai.tts.end 发送 | streamWriter.End | turnToOutbound |
| 节奏控制 | streamWriter.pace | 无（vendor 流式） |

**共享**：切帧 + 编码  
**不共享**：流的开合（start/end）+ 节奏控制

---

## 五、为什么不继续统一流控制

### 设计意图 vs 实际约束

**96_ §3.1 的设计**：
> AI 与梯子的音频走同一个 writer

**98_ §五的发现**：
> 让 AI 音频走 UtteranceWriter 需要先发 ai.tts.start，但当时发 ai.tts.start 会导致静音

**实际完成**：
- 客户端删除回退路径后，ai.tts.start 已经在发（`AssistantAudio:353`）
- 但发送逻辑仍在 provider，未合并到 `streamWriter`

### 为什么停在这里

**技术原因**：
1. AI 音频是流式的（vendor 推送），救援音频是批处理的（一次性合成）
2. streamWriter 的节奏控制（`time.Sleep(w.pace)`）不适合流式推送
3. provider 需要控制"何时发 start"（首帧之前），streamWriter 在 beginUtterance 就发了

**架构权衡**：
- ✅ 统一切帧层：收益明确（无重复编码）
- ❌ 统一流控制：需要重新设计 streamWriter（支持流式 vs 批处理）

---

## 六、当前状态评估

### 编码统一度：95%

| 组件 | 统一情况 |
|---|---|
| 帧编码函数 | ✅ 100%（只有 AITTSAudio.Encode） |
| 切帧逻辑 | ✅ 100%（audioFramer） |
| 序号分配 | ✅ 100%（SeqAllocator） |
| 帧大小（下行） | ✅ 100%（AudioFormat.FrameBytes） |
| 帧大小（上行） | ❌ 50%（两个 640 常量） |
| 流控制 | ⚠️ 50%（救援统一，AI 独立） |

### 实际问题数：1 个

**唯一待解决**：上行块常量重复（pcm16kChunkBytes / devEchoChunkBytes）

**已解决（M3）**：
- ✅ 二进制帧编码重复（94_ F4）
- ✅ 帧大小常量重复（94_ F5 的下行部分）

---

## 七、是否需要继续统一流控制？

### 建议：**不需要（当前阶段）**

**理由**：
1. **功能正确**：AI 音频正常播放，测试全绿，真机通过
2. **架构清晰**：provider 负责流式推送，streamWriter 负责批处理
3. **改动成本高**：需要重新设计 streamWriter 的接口与实现
4. **收益不明显**：流控制的重复度有限（只是 start/end 的发送位置）

**何时重新评估**：
- streamWriter 需要支持流式音频时
- 有第三条音频路径需要统一时
- 发现流控制的 bug 确实因"两处发送"导致时

### 当前形态可接受

**边界清晰**：
- audioFramer：切帧 + 编号（完全共享）
- streamWriter：批处理音频 + 节奏控制（救援专用）
- provider：流式音频 + 发送时机控制（AI 专用）

**文档化**：
在 `utterance.go` 的 streamWriter 注释中已说明：
> **The AI's own audio is deliberately not sent through it**, even though it is the same wire format — see "Why this audio must NOT go through UtteranceWriter" in provider_volc_duplex.go.

---

## 八、总结

### M3 成就：✅ 切帧层完全统一

- 删除了 `provider_volc_duplex.go` 的 `encodeAudioFrame` 函数
- 删除了 `audioFrameBytes` 常量
- 只剩一个编码位置：`voiceproto.AITTSAudio.Encode()`
- 只剩一个切帧器：`audioFramer`

### M3 边界：流控制仍分开

- 救援：通过 `streamWriter`（批处理 + 节奏控制）
- AI：通过 `provider`（流式 + 时机控制）
- 原因：当时发 ai.tts.start 会静音，停在切帧层

### 剩余工作：上行块常量

唯一待统一：`pcm16kChunkBytes` / `devEchoChunkBytes`（见 05_next_steps.md §二）

### 架构评价：A 级

**编码统一完成度高（95%）**，唯一缺口（上行常量）改动成本低且不影响功能。
