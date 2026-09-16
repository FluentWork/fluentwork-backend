# B16 AIOrchestrator 启动计划

**创建日期**: 2026-09-16  
**状态**: 📋 待启动（B14/B15 已通过门禁）  
**优先级**: P0（解除 B18/B21/B23 阻塞链）

---

## 一、B14/B15 验证结果 ✅

### B15 离线评估基线
```bash
$ ./scripts/eval-prompt-regression.sh
{
  "samples": 103,
  "passed": 103,
  "failed": 0
}
=== B15 prompt regression PASS (103 samples) ===
```
**结论**: ✅ B15 提测门禁通过，评价/炼化 Prompt 回归基线健康

### B14 注入 POC
```bash
$ go test ./internal/voicepoc/...
ok      github.com/FluentWork/fluentwork-backend/internal/voicepoc      (cached)
```
**结论**: ✅ B14 Phase 0 设计闭环完成，档位②候选成立

### 门禁通过确认
- ✅ B15: 103/103 样本通过，Schema 合法性、评价引用、炼化三元组完整性全部验证通过
- ✅ B14: duplex live、ASR、注入窗口 T9 已跑通
- ✅ 第一波核心闭环: session → WSS → review ready 已稳定

**可以启动 B16**

---

## 二、为什么需要 B16 AIOrchestrator

### 当前现状：临时缝（ArkCompleter）

当前 B18/B21/B22/B23 都在用 `drill.NewArkCompleter` 作为 B16 的临时替代：

```go
// internal/drill/ark.go:18
// ArkCompleter calls Ark chat-completions. Used as the B16 seam until AIOrchestrator lands.
type ArkCompleter struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}
```

**已知使用方**:
1. **B22 闪测**: `drill.NewArkCompleter` → 判定用户答案
2. **B18 review eval**: 计划复用 `Completer` 接口
3. **B21 素材提炼**: 已在用 `Completer` 缝
4. **B23 话题卡**: 生成走 `Completer` 缝

### 问题与风险

1. **没有统一抽象**: 每个模块各自实例化 `ArkCompleter`，配置分散
2. **缺少降级策略**: Ark 失败直接暴露给业务层，没有 fallback
3. **成本记账缺失**: `ArkCompleter` 不写 `ai_cost_logs`，B18/B21/B23 需各自补
4. **测试困难**: 没有统一 mock 层，每个模块要造自己的假 LLM
5. **供应商耦合**: 直接依赖 Ark API，切换模型需要改 4+ 处代码

### B16 应该提供什么

根据 B18/B21/B23 实施经验，B16 AIOrchestrator 应该是：

1. **统一的 LLM 客户端抽象**:
   ```go
   type LLMClient interface {
       Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
   }
   ```

2. **供应商适配器层**:
   - ArkAdapter（当前）
   - OpenAIAdapter（备用）
   - LocalAdapter（测试）

3. **自动成本记账**:
   - 每次调用自动写 `ai_cost_logs`
   - 统一 `operation` 命名规范（`review.eval`, `material.refine`, `topic.generate`）

4. **降级与重试**:
   - 超时自动重试 1 次
   - 主供应商失败 → fallback 供应商
   - 记录降级 metrics

5. **测试辅助**:
   - `MockLLMClient` 返回预设响应
   - `RecordingLLMClient` 记录所有调用供回放

---

## 三、B16 最小启动包（0.5 dev-day）

### 目标
让 B18 可以把 `drill.NewArkCompleter` 换成 `orchestrator.NewClient()`，其他模块逐步迁移。

### 范围（极简版）

#### 3.1 接口定义（2h）

新建 `internal/orchestrator/client.go`:

```go
package orchestrator

import "context"

// CompletionRequest 统一的 LLM 请求
type CompletionRequest struct {
    Prompt         string
    MaxTokens      int
    Temperature    float64
    SystemPrompt   string
    ResponseFormat string // "json_object" or ""
    Operation      string // for ai_cost_logs: "review.eval", "material.refine"
}

// CompletionResponse 统一的 LLM 响应
type CompletionResponse struct {
    Content       string
    PromptTokens  int
    OutputTokens  int
    TotalTokens   int
    Model         string
    LatencyMS     int64
}

// Client LLM 客户端（支持 Ark / OpenAI / Mock）
type Client interface {
    Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

// NewClient 根据 config 返回配置好的 Client（当前只返回 ArkClient）
func NewClient(cfg config.Config, costWriter CostWriter) Client {
    return &ArkClient{
        baseURL:    cfg.ArkBaseURL,
        apiKey:     cfg.ArkAPIKey,
        model:      cfg.ArkReviewRefineEP, // 可从 env 覆盖
        costWriter: costWriter,
    }
}

// CostWriter 成本记账接口（隔离 aicost 包依赖）
type CostWriter interface {
    Write(ctx context.Context, log CostLog) error
}

type CostLog struct {
    UserID       string
    SessionID    string
    Operation    string
    PromptTokens int
    OutputTokens int
    TotalTokens  int
    Model        string
    LatencyMS    int64
}
```

#### 3.2 Ark 适配器（2h）

新建 `internal/orchestrator/ark.go`，迁移 `drill.ArkCompleter` 代码:

```go
type ArkClient struct {
    baseURL    string
    apiKey     string
    model      string
    costWriter CostWriter
    httpClient *http.Client
}

func (a *ArkClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
    // 1. 构造 Ark API 请求（复用 drill.ArkCompleter 逻辑）
    // 2. 调用 Ark API
    // 3. 解析响应
    // 4. 自动写 ai_cost_logs（如果 costWriter 非 nil）
    // 5. 返回统一响应
}
```

#### 3.3 测试支持（1h）

新建 `internal/orchestrator/mock.go`:

```go
// MockClient 测试用 LLM 客户端
type MockClient struct {
    Response CompletionResponse
    Err      error
}

func (m *MockClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
    return m.Response, m.Err
}
```

#### 3.4 B18 迁移示例（1h）

在 `internal/review/evaluator.go` 中:

```go
// 旧代码（临时缝）
completer := drill.NewArkCompleter(cfg)
result, err := completer.Complete(ctx, prompt)

// 新代码（B16）
client := orchestrator.NewClient(cfg, costWriter)
resp, err := client.Complete(ctx, orchestrator.CompletionRequest{
    Prompt:       prompt,
    MaxTokens:    2000,
    Temperature:  0.7,
    Operation:    "review.eval",
})
```

### 不在启动包范围

- ❌ 多供应商降级（下一批）
- ❌ 自动重试逻辑（下一批）
- ❌ 限流与并发控制（下一批）
- ❌ Prompt 模板管理（各模块自己管理）
- ❌ 流式响应（当前全部批处理）

---

## 四、启动检查清单

### 前置条件 ✅
- [x] B14 POC 测试通过
- [x] B15 评估基线通过
- [x] `drill.ArkCompleter` 已证明可用（B22 已 live）
- [x] `ai_cost_logs` 表已就绪
- [x] Ark API Key 配置已到位

### 实施步骤（Tech Lead + 1 人，0.5 dev-day）

#### Day 1 上午（3h）
- [ ] 创建 `internal/orchestrator` 包
- [ ] 定义 `Client` / `CompletionRequest` / `CompletionResponse` 接口
- [ ] 实现 `NewClient()` 工厂函数
- [ ] 迁移 `drill.ArkCompleter` → `orchestrator.ArkClient`
- [ ] 添加自动 `ai_cost_logs` 写入

#### Day 1 下午（2h）
- [ ] 实现 `MockClient` 测试辅助
- [ ] 编写 `TestArkClient_Complete` 单测（用 httptest）
- [ ] 编写 `TestMockClient` 示例
- [ ] 更新 `B18.md` 启动包文档，加入 Orchestrator 接入说明

#### Day 1 收尾（1h）
- [ ] 在 `feat/b18-review-eval` 分支中实际接入
- [ ] 跑通端到端：review eval 调用 → Orchestrator → Ark → cost log
- [ ] 提交 PR（单独 commit，易回滚）

### 验收标准

1. **接口可用**: `orchestrator.NewClient(cfg, costWriter)` 返回可调用的 Client
2. **成本自动记账**: 调用 `Complete()` 后 `ai_cost_logs` 有对应记录
3. **测试覆盖**: `go test ./internal/orchestrator/...` 通过
4. **B18 可接入**: `internal/review/evaluator.go` 可切换到 Orchestrator
5. **向后兼容**: `drill.ArkCompleter` 仍可用（给 B21/B22/B23 迁移窗口）

---

## 五、下游解除阻塞时间表

### B16 完成后（预计 9/17）
- **B18 review eval**: 立即接入 Orchestrator，本周五前完成
- **B21 素材提炼**: 下周一迁移
- **B23 话题卡**: 下周一迁移
- **B22 闪测**: 保留 `drill.ArkCompleter`（已 live，不急迁移）

### iOS 解除阻塞时间表
```
9/17 (周二) B16 完成 → B18 接入
9/20 (周五) B18 CLOSED → I16 完整转录浮层 可启动
9/23 (周一) B21 CLOSED → I14 创建练习弹层 可启动
9/23 (周一) B23 CLOSED → I18 话题卡 UI 可启动
```

---

## 六、风险与缓解

| 风险 | 影响 | 缓解措施 |
|---|---|---|
| B16 启动延迟 | B18/B21/B23 继续阻塞 | 用启动包而非完整版，0.5d 可交付 |
| Orchestrator 设计过度 | 交付拖到下周 | 极简版只做统一接口 + Ark 适配器 |
| B18 迁移失败 | 回滚到 ArkCompleter | 保留旧代码，新旧并存 1 周 |
| 成本记账遗漏 | 费用统计不准 | 单测覆盖 cost log 写入 |

---

## 七、与 W3 任务对齐

### W3 Backend 票务状态更新

**已完成**:
- ✅ B14: 注入 POC（档位②候选成立）
- ✅ B15: 离线评估基线（103/103 通过）

**本周启动**:
- 🚀 B16: AIOrchestrator（今日启动，明日交付）
- 🔵 B17: TTS Provider（等协议冻结）
- 🔵 B22: 闪测模块（进行中）
- 🔵 B19: B7 命中检测（准备中）
- 🔵 B25: F3 收藏置顶（进行中）

**等待 B16**:
- 🔴 B18: review eval（B16 完成后立即启动）
- 🔴 B21: 素材模块（B16 完成后启动）
- 🔴 B23: 话题卡（B16 完成后启动）

---

## 八、Tech Lead 行动项

### 今日（9/16）
- [ ] Review 本启动计划
- [ ] 分配人力：Tech Lead + 1 后端工程师
- [ ] 确认 Ark API Key 和 endpoint 配置
- [ ] 创建分支 `feat/b16-ai-orchestrator`

### 明日（9/17）
- [ ] 完成 Orchestrator 启动包实施
- [ ] B18 实际接入验证
- [ ] 提交 PR 并 merge
- [ ] 通知 B21/B23 负责人可开始迁移

### 本周五前（9/20）
- [ ] B18 完整实现并 CLOSED
- [ ] iOS I16 启动准备就绪

---

## 九、参考文档

- `docs/02_第二波开发范围与任务清单.md` - B16 在第二波中的定位
- `docs/27_B21_素材模块_实现说明.md` - Completer 缝的实际应用
- `docs/28_B23_话题卡_实现说明.md` - Completer 缝的另一个实例
- `.scratch/issues/2026-09-06-W3-backend-tickets/06-skill-B18-review-eval.md` - B18 对 B16 的依赖说明
- `internal/drill/ark.go` - 当前 ArkCompleter 实现（迁移源）

---

**下一步**: Tech Lead 确认本计划后，立即创建 `feat/b16-ai-orchestrator` 分支并启动实施。
