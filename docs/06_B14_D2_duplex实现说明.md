# B14 D2 实现说明：火山全双工 realtime duplex

**对应 issue**：`B14`  
**对应计划天**：D2（端到端最小会话打通）  
**日期**：2026-08-30  
**状态**：已落地并 live smoke PASS  
**上游**：`fluentwork-meta`《50_FluentWork端到端注入能力验证文档》；《34_FluentWork火山引擎选型与开通清单》  
**执行清单**：`docs/04_B14_注入POC执行清单.md`

---

## 一、本次交付解决什么

B14 要验证「说的房间」端到端链路是否支持 B7 所需的两项能力。D2 目标是：**不经 iOS、直连火山**，打通最小实时会话，并确认是否存在**会话中途注入通道**（验证项 V2）。

本次结论：

| 项 | 结果 |
|---|---|
| 鉴权 | 新版仅 `X-Api-Key`；不需要 AppID / Access Token / 控制台实例名 |
| 协议选型 | 官方全双工 **duplex**（JSON 文本帧），而非旧版 binary realtime |
| 最小会话 | `session.create` → `session.created` PASS |
| 注入通道（V2） | 同连接 `session.update` → `session.updated` PASS |
| 可否冻结 B12 | **否** — 尚无同轮音频观测（T9 / V8） |

---

## 二、为什么选 duplex 而不是旧 realtime

| | 旧 `/api/v3/realtime/dialogue` | 全双工 `/api/v3/duplex/realtime/dialogue` |
|---|---|---|
| 帧格式 | ByteDance 二进制 + 数字 event id | WebSocket **JSON 文本** + `type` 字符串 |
| 鉴权 | 文档常带 AppID / Resource-Id 等历史字段 | **`X-Api-Key` 即可**（与当前控制台口径一致） |
| 注入 | `UpdateConfig` / `ChatRAGText` 等 | `session.update`（改 `instructions`） |
| 工程成本 | 需自研二进制编解码 | 直接 `encoding/json` |

实测：用 Dev 语音 Key 握手 duplex，101 + `session.created`；随后 `session.update` 返回 `session.updated`。因此 Phase 0 POC **默认走 duplex**。

默认 Endpoint：

```text
wss://openspeech.bytedance.com/api/v3/duplex/realtime/dialogue
```

默认模型：`1.2.6.0`；默认音色：`zh_female_vv_jupiter_bigtts`（可用 env 覆盖）。

---

## 三、代码结构

```text
internal/voicepoc/
  window.go / window_test.go     # T9 窗口统计与 B7 档位映射（与供应商无关）
  mock_provider.go              # 无凭证管线
  volc_duplex.go                # OpenDuplex / UpdateInstructions / SmokeDuplex
  volc_provider.go              # VolcDuplexInjectionProvider（live 通道延迟探针）

cmd/smoke-volc-realtime/        # D2 live 入口
cmd/poc-injection-window/       # mock T9；有 Key 时附带 D2；VOLC_POC_LIVE_T9=1 时 live 探针
scripts/smoke-volc-realtime.sh
configs/volc.env.example
```

防腐边界：

- 火山类型与 WSS 细节只落在 `internal/voicepoc`。
- **不**写入 `voice-gateway` 主流程（那是 `B13`）。
- `InjectionProvider` 接口保持可替换；mock / live 共用 `RunT9` 统计。

---

## 四、关键时序（D2）

```text
Client                         Volcano duplex
  |-- WSS + X-Api-Key ---------->|
  |-- session.create ----------->|
  |<-- session.created ----------|   session.id
  |-- session.update ----------->|   改 instructions（注入探针）
  |<-- session.updated ----------|
  |-- session.close ------------>|
```

`SmokeDuplex` 成功时输出字段要点：

- `inject_channel=session.update`
- `inject_channel_ok=true`
- `log_id`（握手 `X-Tt-Logid`，排障给厂商用）
- `credential_mode=live`

---

## 五、如何跑

```bash
cd fluentwork-backend
# 若本机需代理：先执行 proxy_on
cp configs/volc.env.example .env.volc.local   # 填 VOLC_SPEECH_API_KEY（Dev）
./scripts/smoke-volc-realtime.sh

go test ./internal/voicepoc/...
./scripts/poc-injection-window.sh            # mock T9 + 自动 D2（有 Key 时）
VOLC_POC_LIVE_T9=1 ./scripts/poc-injection-window.sh
```

环境变量：

| 变量 | 用途 |
|---|---|
| `VOLC_SPEECH_API_KEY` / `_DEV` | 本地默认语音 Key |
| `VOLC_POC_API_KEY` | B14 POC，可与上相同 |
| `VOLC_POC_ENDPOINT` | 可选，覆盖 duplex URL |
| `VOLC_DUPLEX_MODEL` / `VOLC_DUPLEX_VOICE` | 可选 |
| `VOLC_POC_LIVE_T9=1` | live 通道延迟探针（**仍非** B12 冻结证据） |

---

## 六、明确不在本次范围

1. **V1**：用户话轮 ASR 文本事件（需真实/合成音频上行）  
2. **V3 / V5**：注入后同轮 AI 行为是否改变  
3. **V8 / T9 冻结**：有效窗口 P50/P90 → B7 档位①/②/③  
4. **B13**：`voice-gateway` 生产接入与降级  
5. **meta #12**：额度 / 不训练 / 并发商务闭环  

当前 `VolcDuplexInjectionProvider.RunDelayedInject` 只验证「延迟后 `session.update` 仍可下发」，**故意不**把结果当作同轮生效证据。

---

## 七、验收记录（2026-08-30）

| 检查 | 结果 |
|---|---|
| `go test ./internal/voicepoc/...` | PASS |
| `./scripts/smoke-volc-realtime.sh` | PASS（`inject_channel_ok=true`） |
| 密钥 | 仅 `.env.volc.local`（gitignore）；example 无真值 |

---

## 八、下一 ticket（D3-D4）

对齐执行清单第 3 步：

1. duplex 上行 PCM（16 kHz mono s16le）+ `input_audio_buffer.commit`  
2. 监听 `conversation.item.input_audio_transcription.*` → **V1**  
3. 话轮结束后 `session.update` 注入，观察 `response.output_text.*` → **V3/V5 初测**  
4. 仍不冻结 B12，直到完整 T9（D5）回写 meta doc 50
