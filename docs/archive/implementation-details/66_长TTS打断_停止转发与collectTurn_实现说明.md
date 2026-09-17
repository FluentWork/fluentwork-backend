# 长 TTS 打断：停止转发、帧序、collect 不提前结束

**日期**：2026-09-12
**状态**：实现与测试已齐。
**对应**：真机会话 `8dffe643` / `d5090fe1`；meta `95_` / `96_`；iOS `docs/67`（先发 interrupt）

## 1. 缺陷（叠了四层）

长 TTS 还在播时点「开始说话」：

1. 本地停播是对的，但左侧 AI 气泡被拆成两条，后半句也不是刚问那句的回答。
2. iOS 先发 `user.speech.start` 再发 `interrupt`，网关 `delivered_chars: 0`。
3. `WaitTurnResult`（`collectTurn`）堵在 WSS 读循环上，interrupt 进不了 handler，上一轮整段回复继续转发。
4. `collectTurn` 把上一轮残留的 `output_*.delta` 拼进本轮；并把 `output_audio.done` 当成轮结束，把后面的 `response.done` 留给下一轮 → 60s hang / `outcome=partial`。

顺带：`client.asr.transcription` 要等 collect 结束才发出，「正在转写…」挂到 TTS 已经在播。

## 2. 修法

| 层 | 改什么 |
|---|---|
| 帧序 | `turnToOutbound` closeout：ASR/文本 → 残留/整段音频 → `ai.tts.end` → **`ai.turn.end` 最后** |
| start | `collectingTurn` 时 `user.speech.start` **不** `resetTurnStreamingState()` |
| 读循环 | `user.speech.end` 把 collect 丢到 goroutine；interrupt 仍走同一条读循环。`writeMu` 串行写出。`close()` 先关 provider 再 `collectWG.Wait()` |
| 停转发 | `interruptedThisTurn` 之后 sink 不再推文本/音频；`turnToOutbound` 丢掉 `audioPending` |
| collect | 本轮见到用户进度之前，丢掉残留 `output_text.delta` / `output_audio.delta` |
| 轮结束 | **只有 `response.done` 结束 collectTurn**。`output_audio.done` 只标记 `seenResponse` |
| ASR | `TurnSink.UserTranscript` 在 ASR started/delta/completed 当时转发（去重） |

没有取消火山侧生成（没有 cancel API）。停的是客户端听到的，不是账单。

## 3. 测试

### 修复前的实际失败输出

`TestTurnToOutbound_DoesNotFinalizeBeforeTheAudioTail`（closeout 把 `ai.turn.end` 放在音频之前）：

```
--- FAIL: TestTurnToOutbound_DoesNotFinalizeBeforeTheAudioTail/streamed_leftover_frame
    audio at outbound[N] follows ai.turn.end at [M] — leftover voice after finalize splits the AI bubble
```

`TestHandler_ProcessesInterruptWhileSpeechEndIsCollecting`（读循环同步 WaitTurnResult）：

```
--- FAIL: TestHandler_ProcessesInterruptWhileSpeechEndIsCollecting
    interrupt was not processed while user.speech.end was still collecting; controls=[user.speech.end]
```

`TestCollectTurn_DoesNotFinishOnOutputAudioDoneBeforeResponseDone`：

```
--- FAIL: TestCollectTurn_DoesNotFinishOnOutputAudioDoneBeforeResponseDone
    turn-1 leaked response.done into turn-2: [response.done …]
```

`TestCollectTurn_DropsStaleResponseDeltasUntilThisTurnHasUserProgress`：

```
AssistantText = "Want to break down its key parts?Let's start with core terms.", want this turn only
```

`TestCollectTurn_ForwardsUserTranscriptWhenASRCompletes`（等 reply 才转发 ASR）：

```
ASR was not forwarded until the assistant reply closed the turn
```

`TestBargeInStartThenInterruptStillRecordsWhatWasDelivered` / `TestInterruptStopsForwardingFurtherAssistantAudio`：见 `docs/63` §4。

## 4. 门禁

```bash
./scripts/dev-check.sh
# All checks passed. (2026-09-12)
```

相关测试：

| 测试 | 守什么 |
|---|---|
| `TestTurnToOutbound_DoesNotFinalizeBeforeTheAudioTail` | 尾巴音频 / `ai.tts.end` 在 `ai.turn.end` 之前 |
| `TestHandler_ProcessesInterruptWhileSpeechEndIsCollecting` | collect 在飞时 interrupt 仍被处理 |
| `TestInterruptStopsForwardingFurtherAssistantAudio` / `…TextDeltas` | 打断后不再转发该轮剩余 |
| `TestBargeInStartThenInterruptStillRecordsWhatWasDelivered` | collect 中的 start 不清 `deliveredText` |
| `TestCollectTurn_DropsStaleResponseDeltasUntilThisTurnHasUserProgress` | 上一轮残留 delta 不拼进本轮 |
| `TestCollectTurn_DoesNotFinishOnOutputAudioDoneBeforeResponseDone` | 只有 `response.done` 结束 collect |
| `TestCollectTurn_ForwardsUserTranscriptWhenASRCompletes` | ASR 完成当时就转发，不等 TTS |

## 5. 明确没做的

- **没有取消火山侧生成。**
- **没有把 `interrupted` 暴露给客户端 API。**
- **评价阶段 barge-in 仍不发 WSS `interrupt`**（iOS 只 `.stopPlayback`）。
- **结束练习时的沙沙声**是 iOS 拆图问题，不在本票。
