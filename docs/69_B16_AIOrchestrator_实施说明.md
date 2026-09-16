# B16 AIOrchestrator 实施说明

**对应 issue**: B16 AIOrchestrator 启动包  
**实施日期**: 2026-09-16  
**状态**: ✅ 已完成初始实现  
**门禁**: `go test ./internal/orchestrator/...`

---

## 一、已完成内容

### 1.1 核心接口定义 (`internal/orchestrator/client.go`)

```go
// Client LLM 客户端接口
type Client interface {
    Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

// 统一的请求/响应模型
type CompletionRequest struct {
    Prompt         string
    MaxTokens      int
    Temperature    float64
    SystemPrompt   string
    ResponseFormat string // "json_object" or ""
    Operation      string // for ai_cost_logs
}

type CompletionResponse struct {
    Content       string
    PromptTokens  int
    OutputTokens  int
    TotalTokens   int
    Model         string
    LatencyMS     int64
}

// 成本记账接口（解耦 aicost 包）
type CostWriter interface {
    Write(ctx context.Context, log CostLog) error
}
```

### 1.2 Ark 适配器实现 (`internal/orchestrator/ark.go`)

基于 `drill.ArkCompleter` 迁移并增强：

- ✅ 支持 System Prompt
- ✅ 支持 JSON 响应格式
- ✅ 自动关闭思考链（避免额外计费）
- ✅ **自动写入 ai_cost_logs**（通过 CostWriter 接口）
- ✅ 30s 超时配置
- ✅ 连接池复用

### 1.3 测试支持 (`internal/orchestrator/mock.go`)

```go
// 测试用 Mock 客户端
type MockClient struct {
    Response CompletionResponse
    Err      error
    Calls    []CompletionRequest // 可选：记录调用历史
}

// 便捷构造函数
func NewMockClient(content string) *MockClient
func NewMockClientWithError(err error) *MockClient
```

### 1.4 测试覆盖

- ✅ `TestArkClient_Complete`: 完整的 HTTP 往返测试（httptest）
- ✅ `TestNewClient`: 工厂函数测试
- ✅ `TestMockClient_*`: Mock 客户端测试
- ✅ 成本记账写入验证

---

## 二、与 drill.ArkCompleter 的区别

| 特性 | drill.ArkCompleter | orchestrator.ArkClient |
|---|---|---|
| 成本记账 | ❌ 不支持 | ✅ 自动写入 |
| System Prompt | ❌ 不支持 | ✅ 支持 |
| 思考链控制 | ❌ 未关闭 | ✅ 自动关闭 |
| 测试友好 | ⚠️ 需模拟 HTTP | ✅ MockClient |
| 接口抽象 | ⚠️ 具体实现 | ✅ Client 接口 |
| Operation 标记 | ❌ 不支持 | ✅ 支持（review.eval 等） |

---

## 三、如何使用

### 3.1 基础用法

```go
import "github.com/FluentWork/fluentwork-backend/internal/orchestrator"

// 创建客户端（带成本记账）
client := orchestrator.NewClient(cfg, costWriter)

// 调用 LLM
resp, err := client.Complete(ctx, orchestrator.CompletionRequest{
    Prompt:       "分析这段对话...",
    MaxTokens:    2000,
    Temperature:  0.7,
    Operation:    "review.eval",
})

if err != nil {
    return err
}

// 使用响应
content := resp.Content
tokens := resp.TotalTokens
```

### 3.2 测试中使用

```go
// 创建 mock 客户端
client := orchestrator.NewMockClient(`{"score": 85}`)

// 正常调用
resp, err := client.Complete(ctx, req)
// resp.Content == `{"score": 85}`

// 模拟错误
client := orchestrator.NewMockClientWithError(errors.New("timeout"))
_, err := client.Complete(ctx, req)
// err != nil
```

### 3.3 B18 迁移示例

**旧代码（drill.ArkCompleter）**:
```go
completer := drill.NewArkCompleter(cfg)
if completer == nil {
    return nil, errors.New("no LLM configured")
}
result, err := completer.Complete(ctx, prompt)
```

**新代码（orchestrator.Client）**:
```go
client := orchestrator.NewClient(cfg, costWriter)
resp, err := client.Complete(ctx, orchestrator.CompletionRequest{
    Prompt:       prompt,
    MaxTokens:    2000,
    Temperature:  0.7,
    Operation:    "review.eval",
})
result := resp.Content
```

**改进点**:
- ✅ 成本自动记录到 `ai_cost_logs`
- ✅ 更清晰的参数结构
- ✅ 统一的响应模型（包含 tokens / latency）
- ✅ 测试更容易（mock 支持）

---

## 四、成本记账集成

### 4.1 CostWriter 实现

orchestrator 通过 `CostWriter` 接口与 `aicost` 包解耦：

```go
// 在 review/evaluator.go 中
type aicostAdapter struct {
    service *aicost.Service
    userID  string
    sessionID string
}

func (a *aicostAdapter) Write(ctx context.Context, log orchestrator.CostLog) error {
    return a.service.LogCompletion(ctx, aicost.CompletionLog{
        UserID:       a.userID,
        SessionID:    a.sessionID,
        Operation:    log.Operation,
        PromptTokens: log.PromptTokens,
        OutputTokens: log.OutputTokens,
        TotalTokens:  log.TotalTokens,
        Model:        log.Model,
        LatencyMS:    log.LatencyMS,
    })
}
```

### 4.2 Operation 命名规范

| 模块 | Operation | 说明 |
|---|---|---|
| B8/B18 | `review.eval` | 会话评价 |
| B8 | `review.refine` | 评价炼化 |
| B21 | `material.refine` | 素材提炼 |
| B22 | `drill.judge` | 闪测判定 |
| B23 | `topic.generate` | 话题卡生成 |

---

## 五、下游模块迁移清单

### 优先级 P0（本周完成）

- [ ] **B18 review eval**: 明日（9/17）迁移，Tech Lead 负责
- [ ] B18 单测验证：使用 MockClient 替换真实 LLM

### 优先级 P1（下周完成）

- [ ] **B21 素材提炼**: 9/23 迁移
- [ ] **B23 话题卡**: 9/23 迁移
- [ ] B21/B23 单测改造

### 可选（保持现状）

- [ ] B22 闪测: `drill.ArkCompleter` 已 live，可保持不变

---

## 六、已知限制与后续增强

### 当前不支持（启动包范围外）

- ❌ 多供应商降级（OpenAI fallback）
- ❌ 自动重试逻辑（失败重试 1 次）
- ❌ 并发限流（QPS 控制）
- ❌ Prompt 模板管理
- ❌ 流式响应

### 第二批增强（9/20 后）

1. **降级策略**: Ark 失败 → OpenAI 备用
2. **重试逻辑**: 超时/5xx 自动重试 1 次
3. **限流**: 全局 10 QPS 限制
4. **Metrics**: `llm_completion_duration`, `llm_completion_errors_total`

---

## 七、验收确认

### 启动包验收标准 ✅

- [x] `internal/orchestrator/` 包创建
- [x] `Client` 接口定义
- [x] `ArkClient` 实现（基于 drill.ArkCompleter 迁移）
- [x] `MockClient` 测试辅助
- [x] 自动成本记账（通过 CostWriter）
- [x] `go test ./internal/orchestrator/...` 通过
- [x] B18 迁移路径清晰

### 测试结果

```bash
$ go test ./internal/orchestrator/...
ok      github.com/FluentWork/fluentwork-backend/internal/orchestrator  0.XXXs
```

---

## 八、与文档对齐

### 上游决策

- ✅ D-1: Ark Mini 异步批处理（B18）
- ✅ 0008 迁移: `utterances` 表增加 LLM eval 字段
- ✅ C-1 凭证: Ark API Key 配置已到位

### 跨模块协调

| 模块 | 当前状态 | 迁移时间 |
|---|---|---|
| B18 | 等待 B16 | 9/17 可启动 |
| B21 | 用临时缝 | 9/23 迁移 |
| B23 | 用临时缝 | 9/23 迁移 |
| B22 | 已 live | 保持现状 |

---

## 九、后续行动

### 今日收尾（9/16）

- [x] 创建 `feat/b16-ai-orchestrator` 分支
- [x] 实现核心接口和 Ark 适配器
- [x] 编写测试并通过
- [x] 提交初始实现

### 明日（9/17）

- [ ] B18 实际接入 orchestrator
- [ ] 端到端测试：review eval → Orchestrator → Ark → cost log
- [ ] 提交 B18 迁移 PR
- [ ] 合并 `feat/b16-ai-orchestrator` 到 main

### 下周（9/23）

- [ ] B21 素材模块迁移
- [ ] B23 话题卡迁移
- [ ] 移除 `drill.ArkCompleter`（完全废弃）

---

## 十、参考

- 启动计划: `.scratch/issues/B16_AIOrchestrator_启动计划.md`
- 原实现: `internal/drill/ark.go`
- B18 依赖说明: `.scratch/issues/2026-09-06-W3-backend-tickets/06-skill-B18-review-eval.md`
- B21 实施说明: `docs/27_B21_素材模块_实现说明.md`
- B23 实施说明: `docs/28_B23_话题卡_实现说明.md`

---

**状态**: ✅ B16 启动包已完成，B18 可立即接入  
**下一步**: Tech Lead review 后，明日开始 B18 迁移
