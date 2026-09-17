# OpenAI 兼容 Provider 实施方案（留存，未实施）

**文档版本**: V1.0
**创建日期**: 2026-09-18
**状态**: ⏸ **未实施**。本文档留存方案，供需要换厂商/换模型时直接照做。
**前置阅读**: `88_LLM_Provider_接入说明`（接缝契约与现状）

---

## 一、什么时候该翻出这份文档

满足任一条再动手，**不要为了换而换**：

- 火山侧成本、配额或稳定性成为问题（本次评估期间就出现过一次账号欠费导致全链路 403）；
- 需要更强的模型做 review/炼化（当前 pro 端点已够用）；
- 需要某家特有的能力（更长上下文、更便宜的批量、更快的判定）。

**换模型不是免费午餐**：prompt 是为当前模型调过的，换家必须重跑 `cmd/eval-moat-flow`
并对比指标（§七）。这也是本方案存在的意义——让"换"变成一次可测量的实验，而不是一次赌博。

---

## 二、范围

| 在范围内 | 不在范围内 |
|---|---|
| 文本 LLM（review/炼化、闪测判定、话题卡、素材提炼） | 实时语音链路（`VoiceProviderSession`，火山双工协议，见 88_ §3.3） |
| `orchestrator.Client` 的新实现 | 每日一读的 TTS 合成（`internal/content/tts`，独立的语音供应商面） |

DeepSeek 没有实时语音 API，因此现实组合是**文本换家、语音留火山**。

---

## 三、设计

### 3.1 新增一个实现，不动其它任何东西

```
internal/orchestrator/
   client.go        接口（不变）
   ark.go           火山实现（不变）
   openai.go        ★ 新增：OpenAI 兼容实现（DeepSeek / Qwen / OpenAI / vLLM 通用）
   routing.go       ★ 可选：把 operation→模型 的映射从 ark.go 抽出来共用
```

### 3.2 请求映射

| `CompletionRequest` | OpenAI 兼容字段 | 备注 |
|---|---|---|
| `SystemPrompt` + `Prompt` | `messages: [{role:system},{role:user}]` | 不合并 |
| `MaxTokens` | `max_tokens` | |
| `Temperature` | `temperature` | |
| `ResponseFormat == "json_object"` | `response_format: {"type":"json_object"}` | DeepSeek 支持；prompt 里已含 "JSON" 字样（其要求） |
| `Operation` | *不发* | 只用于选模型 + 账本 task_type |
| `UserID` | *不发* | 只用于账本归属 |
| —— | ~~`thinking`~~ | **不发**：火山独有 |

### 3.3 响应映射

| OpenAI 兼容字段 | `CompletionResponse` | 备注 |
|---|---|---|
| `choices[0].message.content` | `Content` | |
| `model` | `Model` | **进账本**，必须回填真实模型名 |
| `usage.prompt_tokens` / `completion_tokens` / `total_tokens` | 三个 token 字段 | |
| `choices[0].finish_reason` | `FinishReason` | **截断判定依赖它**（`length`） |
| —— | `LatencyMS` | 自己计时 |

### 3.4 选择实现与配置

```go
// client.go
func NewClient(cfg config.Config, costWriter CostWriter) Client {
    switch strings.ToLower(strings.TrimSpace(cfg.LLMProvider)) {
    case "openai", "deepseek":
        return NewOpenAIClient(cfg, costWriter)
    case "mock":
        return NewMockClient(...)
    default:
        return NewArkClient(cfg, costWriter)   // 默认不变，切家是显式动作
    }
}
```

| 新配置 | 说明 | 默认 |
|---|---|---|
| `LLM_PROVIDER` | `ark`（默认）/ `openai` / `mock` | `ark` |
| `OPENAI_API_KEY` | | 空 |
| `OPENAI_BASE_URL` | 如 `https://api.deepseek.com` | 空 |
| `OPENAI_MODEL_DEFAULT` | 兜底模型，如 `deepseek-chat` | 空 |
| `OPENAI_MODEL_<OPERATION>` | 按 operation 覆盖，如 `OPENAI_MODEL_DRILL_JUDGE=deepseek-chat` | 空 |

**路由的差异**：火山把 endpoint id 当 model 传；OpenAI 兼容侧直接传模型名。
因此 `endpointRouting` 的映射来源要按 provider 分开——这正是 §3.1 里
`routing.go` 存在的理由。

### 3.5 账本与失败分类（零改动）

- 账本：仍是"一次调用一行"，`task_type = Operation`、`model = resp.Model`、成本走
  `CalculateCostMicroYuan`（费率表键用**模型名**，如 `deepseek-chat`）；
- 失败分类：复用 `reviewgen.GenerateError`（transport / http_status / truncated_json /
  schema_violation …），截断判定继续依赖 `finish_reason`；
- **模型名进账本会让按模型的统计分成两段**——这是应该的：换模型前后的成本与质量
  本就不该混在一起看。

---

## 四、代码骨架（约 120 行，未落地）

```go
type OpenAIClient struct {
    baseURL, apiKey, defaultModel string
    models   map[string]string   // operation → model
    http     *http.Client
    costWriter CostWriter
}

func (c *OpenAIClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
    start := time.Now()
    model := c.modelFor(req.Operation)
    body := openAIChatRequest{
        Model: model,
        Messages: buildMessages(req.SystemPrompt, req.Prompt),
        MaxTokens: req.MaxTokens, Temperature: req.Temperature,
    }
    if req.ResponseFormat == "json_object" {
        body.ResponseFormat = &openAIResponseFormat{Type: "json_object"}
    }
    // POST {baseURL}/chat/completions, Authorization: Bearer
    // 解析 → CompletionResponse（含 Model 与 FinishReason）
    // 成功且 req.Operation != "" → costWriter.Write(CostLog{...})
    // 错误 → fmt.Errorf("openai api error: status=%d body=%s", ...)
}
```

---

## 五、测试计划

| 层 | 测试 |
|---|---|
| 单元 | 请求映射（含 json_object 与不发 thinking）、响应映射（缺 usage / 缺 choices / finish_reason=length）、错误映射 |
| 路由 | operation → 模型；未配置 → 兜底模型 |
| 账本 | 一次调用一行、model 为响应里的真实模型名、成本按费率表算 |
| 契约 | 与 `ArkClient` 对齐：同一 `CompletionRequest` 两者都能满足 `Client`，且失败分类一致 |
| 端到端 | `cmd/eval-moat-flow` 在 `LLM_PROVIDER=openai` 下跑 100 条，与火山基线对比（§七） |

---

## 六、灰度步骤

1. **只切一个最便宜的 operation**：`OPENAI_MODEL_DRILL_JUDGE=deepseek-chat`，其余留火山
   （路由已支持按 operation 分开）；
2. 跑 `cmd/eval-moat-flow --reuse-run <上次全量> --out eval-out`，只重跑判定那一段
   （约 200 次调用），对比：判定准确率、生产预算超时率、账本成本；
3. 达标（判定准确率 ≥0.95 且不低于火山基线 0.974 的 0.02 以内）再扩到 review/炼化；
4. 每扩一段都保留"回滚开关"：`LLM_PROVIDER=ark` 一个环境变量即可切回。

---

## 七、验收标准（换家的门禁）

沿用 `docs/87` 的门禁，且**不得低于火山基线**：

| 指标 | 门槛 |
|---|---|
| 结构合法率 / 锚点在转录内 / 标签合法 | = 1.0 |
| 判定准确率 | ≥ 0.95（火山基线 0.974） |
| 生产预算超时率 | ≤ 0.10（火山基线 0.005） |
| 炼化质量（忠实/地道/可迁移） | ≥ 4.0（火山基线 4.62/4.83/4.88） |
| 话题落地度 / 泛话题率 | 落地 ≥4.0；泛话题 ≤0.05 |

任何一项不达标就**不切**——prompt 为当前模型调过，换家的质量回归是常态而非意外。

---

## 八、已知风险

| 风险 | 缓解 |
|---|---|
| JSON 输出稳定性不同，review/炼化的 schema 违约率上升 | 门禁里的结构合法率 = 1.0；失败分类已有 `invalid_json`/`truncated_json` 可定位 |
| 速率限制与并发画像不同 | `ARK_HTTP_TIMEOUT` 同类旋钮（新增 `OPENAI_HTTP_TIMEOUT`）；评估时观察超时率 |
| 定价口径不同（缓存命中价、批量价） | 费率表按模型名逐条填，缺失即记 0 并计入 `UnpricedModels` |
| 双供应商并存导致归因混乱 | 账本 `model` 列天然分段；看板按 model 分组即可 |
