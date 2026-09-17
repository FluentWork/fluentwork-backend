# LLM Provider 接入说明（防腐层契约）

**文档版本**: V1.0
**创建日期**: 2026-09-18
**适用**: 想换模型、换厂商、或新增一个 LLM 供应商时读这一份。

---

## 一、接缝在哪

```
drill / reviewgen / topic / materials / review        ← 功能代码，不含任何厂商细节
        │  只依赖接口
        ▼
orchestrator.Client                                    ← 唯一的 LLM 接缝
   Complete(ctx, CompletionRequest) (CompletionResponse, error)
        ├── ArkClient    （火山方舟，internal/orchestrator/ark.go）
        └── MockClient   （测试与本地）
```

**不变量**：功能包**不得** import 任何厂商 SDK 或自建 HTTP 调用。2026-09-18 前 `reviewgen` 里有一个绕过接缝的 `ArkGenerator`（自己的 HTTP 调用），已删除——那正是防腐层最怕的并行实现。

**换厂商的代价 = 新增一个 Client 实现 + 配置**；prompt、校验、失败分类、账本、评估全部不动。

---

## 二、新 provider 必须实现什么

### 2.1 方法

```go
Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
```

### 2.2 请求字段（必须遵守）

| 字段 | 要求 |
|---|---|
| `SystemPrompt` / `Prompt` | 分别作为 system 与 user 消息；不得合并 |
| `MaxTokens` / `Temperature` | 透传；不支持时取最近似值并在实现里注明 |
| `ResponseFormat` | `"json_object"` 表示要求 JSON 输出。**不支持该参数的厂商必须靠 prompt 约束**，并在实现里说明 |
| `Operation` | 用于**路由**（选哪个部署/模型）与**账本 task_type**；不得改写 |
| `UserID` | 写进账本行做归属；可为空 |

### 2.3 响应字段（必须回填）

| 字段 | 为什么必须 |
|---|---|
| `Content` | 上层解析的唯一来源 |
| `Model` | **账本按它归因**。填厂商返回的真实模型名，不要填请求里写的别名 |
| `PromptTokens` / `OutputTokens` / `TotalTokens` | 账本用量 |
| `LatencyMS` | 观测 |
| `FinishReason` | **截断判定依赖它**（`length` → 记为 truncated_json 而不是 invalid_json） |

厂商特有字段（如火山的 `thinking`）**只能存在于该厂商的实现里**，不得进入 `CompletionRequest`。火山的 `thinking` 现在由 `ArkClient` 自己发送，可用 `ARK_THINKING=disabled|auto` 配置。

### 2.4 错误与成本

- 传输/超时错误返回 `error`；上层按 `GenerateError` 分类（见 `reviewgen/failure.go`）。
- 每次成功调用后调用 `costWriter.Write`（若配置），写一行账本：`task_type = Operation`、`model = resp.Model`、三个 token 数与成本。**一次调用一行**——重复计数是这套账本最容易出的错（review 生成曾同时写两行）。

---

## 三、现在就能做的两种"换"

### 3.1 同厂商换模型/端点 —— 只改配置

```
ARK_EP_REVIEW_REFINE / ARK_EP_DRILL_JUDGE / ARK_EP_TOPIC_CARD / …   已按 operation 路由
ARK_PRICING_FILE    费率表（单位：CNY/百万 tokens，`_` 开头为注释键）
ARK_HTTP_TIMEOUT    单次调用上限（默认 30s）
ARK_THINKING        思考链开关（默认 disabled）
```

### 3.2 换到 DeepSeek（或任何 OpenAI 兼容厂商）—— 一个新文件 + 配置

> 具体实施方案（映射表、配置项、灰度步骤、验收门禁）见 `89_OpenAI兼容Provider_实施方案`。

DeepSeek 的 API 是 OpenAI 兼容的：`POST https://api.deepseek.com/chat/completions`，`model = deepseek-chat` / `deepseek-reasoner`。步骤：

1. 新增 `internal/orchestrator/openai.go`（≈120 行）：实现 `Client`，把 `CompletionRequest` 映射成 chat-completions，回填 §2.3 的全部字段。**不要**发 `thinking`；`response_format: {"type":"json_object"}` DeepSeek 支持。
2. 新增配置项（如 `DEEPSEEK_API_KEY` / `DEEPSEEK_BASE_URL` / `DEEPSEEK_MODEL`），并在 `orchestrator.NewClient` 里按环境变量选择实现（现在是返回 Ark 单例）。
3. 费率写进 `ARK_PRICING_FILE`（该文件是"内部费率表"，与厂商无关，键用**API 返回的模型名**，如 `deepseek-chat`）。
4. `endpointToModel` / 定价表里的火山条目可保留（换回去时还用得上）。

**要留意的三处**：
- 模型名不同 → 账本的 `model` 列会变成 `deepseek-chat`，按模型的统计会分成两段（这是应该的）；
- 火山的"endpoint id 当 model 传"是它独有的约定，DeepSeek 传模型名即可；
- 评估与冒烟工具全部走同一个 `Client`，**无需改动**（`cmd/eval-moat-flow`、`cmd/smoke-*`）。

### 3.3 换不了的部分：语音链路

`orchestrator.Client` 只管**文本 LLM**。语音是另一条链路，接缝是 `VoiceProviderSession` 接口（已有 mock / dev-echo / volc-duplex 三个实现），但下面的火山双工协议（二进制分帧、事件名、会话配置）换厂商要重写 provider。

**DeepSeek 没有实时语音 API**，所以换文本模型不影响语音：文本可以换 DeepSeek，语音仍留火山（或另找语音厂商单独换）。

---

## 四、目前仍属厂商专属的部分（清单，供换厂商时对照）

| 位置 | 内容 |
|---|---|
| `internal/orchestrator/ark.go` | base URL、`thinking`、endpoint-id 当 model、Bearer 认证 |
| `internal/orchestrator/pricing.go` | `endpointToModel` 映射表（火山 endpoint → 模型名）；费率表本身已可外部化 |
| `internal/voicepoc/`、`provider_volc_duplex.go` | 火山实时语音协议（与文本链路无关） |

除以上位置，仓库里没有任何文件引用火山。
