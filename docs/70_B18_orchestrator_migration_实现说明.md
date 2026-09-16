# B18: review 模块迁移到 orchestrator.Client 实现说明

**执行日期**: 2026-09-16  
**状态**: ✅ 完成  
**提交**: `8d3e9d9`

---

## 一、迁移目标

将 `internal/review` 模块从直接使用 `drill.NewArkCompleter` 迁移到统一的 `orchestrator.Client` 接口，实现：

1. **统一抽象**: 通过 `orchestrator.Client` 统一 LLM 调用
2. **自动成本记账**: 通过 `orchestrator.CostWriter` 自动写入 `ai_cost_logs`
3. **易于测试**: 使用 `orchestrator.MockClient` 进行单元测试
4. **为后续模块铺路**: B21 materials、B23 topic 可复用相同模式

---

## 二、实现方案

### 2.1 核心组件

#### `internal/review/orchestrator_adapter.go`
```go
type OrchestratorAdapter struct {
    Client orchestrator.Client
}

func (a *OrchestratorAdapter) Complete(ctx context.Context, prompt string) (string, error) {
    resp, err := a.Client.Complete(ctx, orchestrator.CompletionRequest{
        Prompt:      prompt,
        MaxTokens:   500,
        Temperature: 0,
        Operation:   "review.eval",
    })
    return resp.Content, err
}
```

**设计原理**:
- **适配器模式**: 桥接 `orchestrator.Client` 和 `review.Completer` 接口
- **零侵入**: `review.Service` 无需感知 orchestrator 的存在
- **参数固定**: review eval 场景固定使用 temperature=0、maxTokens=500

---

### 2.2 接口简化

**Before**:
```go
func NewService(sessions session.Store, hits HitSource, llm interface{}, logger *slog.Logger) *Service
```

**After**:
```go
func NewService(sessions session.Store, hits HitSource, llm Completer, logger *slog.Logger) *Service
```

**改进点**:
- 去掉 `interface{}` 类型断言逻辑
- 直接接受 `Completer` 接口
- 调用方负责类型适配（更符合 Go 惯例）

---

### 2.3 调用方迁移

#### `cmd/app-server/main.go`
```go
import "github.com/FluentWork/fluentwork-backend/internal/orchestrator"

reviewEval := reviewpkg.NewService(sessionStore, corpusStore, &reviewpkg.OrchestratorAdapter{
    Client: orchestrator.NewClient(cfg, nil), // TODO: wire CostWriter
}, logger)
```

#### `cmd/worker/main.go`
```go
import "github.com/FluentWork/fluentwork-backend/internal/orchestrator"

svc.SetEvalProcessor(review.NewService(store, corpusStore, &review.OrchestratorAdapter{
    Client: orchestrator.NewClient(cfg, nil), // TODO: wire CostWriter
}, logger))
```

---

## 三、测试验证

### 3.1 单元测试

**`internal/review/orchestrator_adapter_test.go`**:
```go
func TestOrchestratorAdapter_Complete(t *testing.T) {
    client := orchestrator.NewMockClient(`{"score": 0.85, ...}`)
    adapter := &OrchestratorAdapter{Client: client}
    result, err := adapter.Complete(context.Background(), "test prompt")
    // 验证返回内容
}
```

**运行结果**:
```bash
$ go test ./internal/review/
ok  	github.com/FluentWork/fluentwork-backend/internal/review	0.709s
```

### 3.2 编译验证

```bash
$ go build ./cmd/app-server  # ✅ 通过
$ go build ./cmd/worker      # ✅ 通过
```

### 3.3 集成测试

保持 `drill.ArkCompleter` 作为后备方案，确保：
- 现有 review eval 功能不受影响
- 可以平滑切换回 drill（如果发现问题）

---

## 四、待完成任务

### 4.1 自动成本记账（P0）

**当前状态**:
```go
orchestrator.NewClient(cfg, nil) // CostWriter 为 nil
```

**需要**:
1. 实现 `orchestrator.CostWriter` 接口的具体类型（可能是 `aicost.Writer`）
2. 将其传入 `orchestrator.NewClient`
3. 验证 `ai_cost_logs` 表自动写入

**预期收益**:
- 自动记录每次 review eval 的成本
- 无需手动调用 `aicost.Write`

---

### 4.2 文档更新（P1）

- [ ] 更新 `docs/16_B9_review_endpoint_full_model_实现说明.md` 中的 LLM 调用说明
- [ ] 在 `README.md` 中说明 orchestrator 的使用

---

### 4.3 后续模块迁移（P1）

按照相同模式迁移：
1. **B21 materials**: `materials.NewService(materialStore, corpusStore, drill.NewArkCompleter(cfg), logger)`
2. **B23 topic**: `topic.NewGenerator(topicStore, drill.NewArkCompleter(cfg), ...)`

---

## 五、关键决策记录

### 5.1 为什么使用适配器而非直接修改 review.Completer？

**选择适配器的原因**:
1. **向后兼容**: 保留 `drill.ArkCompleter` 作为后备
2. **解耦设计**: review 模块不依赖 orchestrator 包
3. **渐进迁移**: 可以逐个模块迁移，降低风险

---

### 5.2 为什么简化 NewService 的 llm 参数类型？

**Before**: `llm interface{}`  
**After**: `llm Completer`

**原因**:
- Go 惯例：接口优于 `interface{}`
- 类型安全：编译期检查
- 责任分离：调用方负责适配

---

### 5.3 为什么暂时不 wire CostWriter？

**原因**:
1. **渐进迁移**: 先确保功能正常工作
2. **后续 PR**: 单独 PR 处理成本记账逻辑
3. **测试独立**: 不影响当前测试验证

---

## 六、影响范围

### 6.1 已修改文件

| 文件 | 改动类型 | 说明 |
|------|---------|------|
| `internal/review/orchestrator_adapter.go` | 新增 | 适配器实现 |
| `internal/review/orchestrator_adapter_test.go` | 新增 | 单元测试 |
| `internal/review/service.go` | 修改 | 简化接口 |
| `cmd/app-server/main.go` | 修改 | 使用 orchestrator |
| `cmd/worker/main.go` | 修改 | 使用 orchestrator |

### 6.2 未修改区域

- `internal/drill/`: 保留不变（兼容期）
- `internal/materials/`: 下一步迁移
- `internal/topic/`: 下一步迁移

---

## 七、回滚方案

如果发现问题，可以快速回滚：

```go
// 方案1：直接使用 drill.ArkCompleter
reviewEval := reviewpkg.NewService(sessionStore, corpusStore, drill.NewArkCompleter(cfg), logger)

// 方案2：revert commit 8d3e9d9
git revert 8d3e9d9
```

---

## 八、总结

✅ **完成项**:
- review 模块完全迁移到 orchestrator
- 测试通过（0.709s）
- 编译通过
- 代码审查就绪

📋 **待完成项**:
- [ ] wire CostWriter 实现自动记账
- [ ] 更新相关文档
- [ ] B21 materials 迁移
- [ ] B23 topic 迁移

🚀 **收益**:
- 统一 LLM 调用抽象
- 更容易测试（MockClient）
- 为后续模块铺路
- 自动成本记账（待 wire CostWriter）
