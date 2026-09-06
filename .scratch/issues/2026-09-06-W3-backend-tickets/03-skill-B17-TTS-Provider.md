# Skill B17 — TTS Provider + 流式集成(新建 master)

> **Master**: 待新建 `B17: TTS Provider + 流式集成`
> **Sub-tickets**: 5 (T-TTS-1..5)
> **总工时**: 1.5 dev-day(与 56_ 启动包一致,内部强耦合不切碎)
> **阻塞**: C-2 火山 TTS 凭证 + 4 个 voice_id 真 resource_id 查表
> **关联**: I15 iOS TTS 播放(下游)/ B20 每日一读内容生成(下游)

---

## §0 Master Issue Body(整段可粘贴)

**Title**: `B17: TTS Provider + 流式集成`  
**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `tts`  
**Milestone**: `V2.0 W3`

```markdown
## 🎯 目标
在 `internal/content/tts` 新建 TTS Provider 抽象层,落地火山独立 TTS(主路径)+ 双工内置 TTS(fallback),按 D-2 拍板音色表实施。给后续 B20 每日一读批量生成、iOS I15 流式播放铺平接口。

## 🚧 阻塞条件
- D-2 凭证到位(C-2:火山 TTS API Key + 流式 TTS endpoint + 4 个 voice_id 真 resource_id 查表)
- SLA:9/10 W3 Day 1 启动日,首日花 1h 查音色

## 📐 Sub-tickets(Skills-to-Ticket 切分)

| # | Ticket | 工时 | 阻塞 |
|---|---|---|---|
| T-TTS-1 | Provider 接口定义 + unit tests | 0.3d | 无 |
| T-TTS-2 | 火山独立 TTS HTTP/2 client + 流式 chunk channel | 0.6d | T-TTS-1 |
| T-TTS-3 | 双工内置 TTS fallback 实现 | 0.2d | T-TTS-2 |
| T-TTS-4 | 4 个 D-2 音色常量 + voice_config 透传 | 0.2d | T-TTS-2 |
| T-TTS-5 | 5xx 触发 fallback metrics + OpenAPI 同步 | 0.2d | T-TTS-3, T-TTS-4 |

## ✅ Master 验收(53_ §六 EPIC-A / 56_ DoD)
- [ ] 5 个 sub-ticket 全部完成并 merge
- [ ] `go test ./internal/content/tts/...` 6 个测试通过
- [ ] 集成测试:与真实火山 TTS 端到端 1 次成功(凭证到位后)
- [ ] 流式首字 P90 ≤ 400ms
- [ ] OpenAPI 同步:手动编辑 `api/openapi-v1.yaml` 加 TTS Provider 配置说明
- [ ] `48_` 契约冻结文档 §1.6.1 确认 TTS 集成路径
- [ ] PR 通过 OpenCodeReview 自动 review + 1 名后端 maintainer review

## 🔗 关联
- 上游:无(首启动)
- 下游:B20 每日一读内容生成、I15 iOS 流式播放
- 启动包:`docs/40_研发流程与协作/56_B17_TTS_Provider_Issue_Draft_2026-09-06.md`
- 详见:`.scratch/issues/2026-09-06-W3-backend-tickets/03-skill-B17-TTS-Provider.md`
```

---

## §1 T-TTS-1 — Provider 接口定义 + unit tests

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `tts`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
在 `internal/content/tts/provider.go` 定义 Provider 接口 + AudioChunk / VoiceConfig 结构体。

## 📋 实施步骤
1. 新建 `internal/content/tts/provider.go`:
   ```go
   package tts

   type VoiceConfig struct {
       VoiceID string
       Speed   float64
   }

   type AudioChunk struct {
       Data       []byte
       Seq        int
       IsFinal    bool
       DetectedAt int64
   }

   type Provider interface {
       Stream(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error)
       Ping(ctx context.Context) error
       Close() error
   }
   ```
2. 新建 `internal/content/tts/provider_test.go`:
   - `TestProvider_Interface_Contract`(用 mock 实现验证接口)
   - `TestAudioChunk_Schema`
   - `TestVoiceConfig_Default`

## ✅ 验收
- [ ] 文件可编译,接口可被 mock 实现
- [ ] 3 个单测 PASS
- [ ] 无外部依赖(不引火山 SDK)

## 🔗 依赖
- Blocked by: 无
- Blocks: T-TTS-2..5
- Master: B17
```

---

## §2 T-TTS-2 — 火山独立 TTS HTTP/2 client + 流式 chunk channel

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `tts`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `VolcStreamingProvider`,走火山 HTTP/2 StreamTTS,首字 P90 ≤ 400ms。

## 📋 实施步骤
1. 新建 `internal/content/tts/volc_streaming.go`:
   - 用 `net/http` HTTP/2 client(无 SDK 依赖)
   - 端点:`https://openspeech.bytedance.com/api/v3/tts/unidirectional`
   - Headers:`X-Api-Key: <VOLC_SPEECH_API_KEY>` + `X-Api-Resource-Id: <voice_resource_id>`
   - 流式读取 chunk,推到 channel
2. `Stream` 实现:
   - 返回 `<-chan AudioChunk`,ctx cancel 或服务端断开时关闭
   - `IsFinal=true` 标记最后一帧
3. 鉴权 + 超时 + 重试(只对 5xx 重试 1 次)
4. 单测 `TestVolcStreaming_Stream_Success` / `TestVolcStreaming_Stream_ContextCancel` / `TestVolcStreaming_Stream_5xxRetry`

## ✅ 验收
- [ ] 真实火山 TTS 端到端 1 次成功
- [ ] 首字 P90 ≤ 400ms(用 staging 环境跑 10 次)
- [ ] ctx cancel 时 channel 立即关闭
- [ ] 5xx 自动重试 1 次,失败后 error 上抛

## 🔗 依赖
- Blocked by: T-TTS-1, C-2(4 个 voice_id 真 resource_id 查表)
- Blocks: T-TTS-3, T-TTS-4
- Master: B17
```

---

## §3 T-TTS-3 — 双工内置 TTS fallback 实现

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `tts`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `VolcDuplexFallbackProvider`,封装已落地的 voicegateway duplex 会话 TTS 路径(音色固定)。

## 📋 实施步骤
1. 新建 `internal/content/tts/volc_duplex_fallback.go`:
   - 复用 `internal/voicepoc.OpenDuplex(ctx, cfg)`,但只读 TTS 事件
   - VoiceConfig 默认 `zh_female_vv_jupiter_bigtts`
2. `Stream` 实现:从 duplex 事件流解析 TTS 帧,转 `AudioChunk`
3. `Ping` 失败计数:3 次连续失败返回 error,触发 fallback 切换

## ✅ 验收
- [ ] 与 voicegateway duplex 兼容(同一会话内多次切换不报错)
- [ ] Ping 失败 3 次返回 error
- [ ] 单测 `TestDuplexFallback_Ping_Fail3` / `TestDuplexFallback_Stream_FromEvents` PASS

## 🔗 依赖
- Blocked by: T-TTS-2
- Blocks: T-TTS-5
- Master: B17
```

---

## §4 T-TTS-4 — 4 个 D-2 音色常量 + voice_config 透传

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `tts`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
按 D-2 拍板音色表落地 4 个常量,handler 注入支持外部传入 voice_config。

## 📋 实施步骤
1. 新建 `internal/content/tts/voices.go`(参考 56_ §4):
   ```go
   var (
       VoiceAIMaleTech        = VoiceConfig{VoiceID: "<查表 resource_id zh_male_tech_01>", Speed: 0.9}
       VoiceAIFemalePro       = VoiceConfig{VoiceID: "<查表 resource_id en_female_professional>", Speed: 1.0}
       VoiceDailyReadNarrator = VoiceConfig{VoiceID: "<查表 resource_id en_male_narrator>", Speed: 0.85}
       VoiceDrillCountdown    = VoiceConfig{VoiceID: "<查表 resource_id en_female_clear>", Speed: 1.0}
   )
   ```
2. handler 透传:`POST /internal/v1/tts/synthesize` 接受 `voice_id` 参数
3. 音色查表 SOP:运行 `scripts/check-volc-voice-resource-ids.sh`(待建)

## ✅ 验收
- [ ] 4 个常量编译通过
- [ ] `scripts/check-volc-voice-resource-ids.sh` 4 个 voice 全部 probe PASS
- [ ] 切换 voice_id 实际音色变化(端到端集成测试)

## 🔗 依赖
- Blocked by: T-TTS-2, C-2 音色查表
- Blocks: T-TTS-5
- Master: B17
```

---

## §5 T-TTS-5 — 5xx 触发 fallback metrics + OpenAPI 同步

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `tts`, `observability`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
VolcStreamingProvider 5xx 连续 3 次 → 触发 VolcDuplexFallbackProvider 切换 + 写 metric。

## 📋 实施步骤
1. 新建 `internal/content/tts/manager.go`:
   - 主 provider + fallback provider 包装
   - 5xx 连续 3 次(用 sync.atomic 计数)切换 fallback
   - 切换后写 metric:`tts_fallback_triggered_total{from="volc_streaming", to="volc_duplex"}`
2. OpenAPI 同步:手动编辑 `api/openapi-v1.yaml`:
   - 新增 `POST /internal/v1/tts/synthesize` 路径
   - 描述主 + fallback 行为
3. metric 暴露:接 OpenTelemetry 或 Prometheus(看现有配置)

## ✅ 验收
- [ ] 5xx 连续 3 次 → fallback 切换成功
- [ ] metric `tts_fallback_triggered_total` 出现在 `/metrics`
- [ ] OpenAPI 文件 commit + 注释
- [ ] 单测 `TestManager_FallbackTrigger` PASS

## 🔗 依赖
- Blocked by: T-TTS-3, T-TTS-4
- Blocks: 无(Master 收口)
- Master: B17
```

---

## §6 执行顺序与总工时

```
C-2 凭证 ──┐
           ├─▶ T-TTS-1 (0.3d) ──▶ T-TTS-2 (0.6d) ──┬──▶ T-TTS-3 (0.2d) ──┐
           │                                        ├──▶ T-TTS-4 (0.2d) ──┼──▶ T-TTS-5 (0.2d)
           │                                        │                       │
           └─▶ 音色查表 SOP ────────────────────────┘                       │
                                                                          ▼
                                                                       B17 CLOSED
```

**总工时**:1.5 dev-day
**推荐 Owner**:后端语音组(`@voice-gateway 当周值班` 兼任)
**启动建议**:W3 Day 1(9/10 周三)上午先做音色查表(1h),再开始 T-TTS-1
