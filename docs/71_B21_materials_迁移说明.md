# B21 materials 模块迁移到 orchestrator

**迁移时间**: 2026-09-16  
**Commit**: `dd9dee1`  
**状态**: ✅ 已完成并合并到 main

---

## 一、迁移目标

将 `internal/materials` 模块从直接使用 `drill.ArkCompleter` 迁移到统一的 `orchestrator.Client`，实现：

1. **统一 LLM 抽象**: 复用 B16 orchestrator 的 Client 接口
2. **自动成本记账**: 通过 CostWriter 自动写入 `ai_cost_logs`
3. **测试友好**: 使用 `orchestrator.MockClient` 简化单元测试

---

## 二、实现方案

### 2.1 适配器模式

创建 `materials.OrchestratorAdapter` 桥接两个接口：

```go
// materials.Completer 接口（不变）
type Completer interface {
    Complete(ctx context.Context, prompt string) (string, error)
}

// OrchestratorAdapter 实现 Completer 接口
type OrchestratorAdapter struct {
    Client orchestrator.Client
}

func (a *OrchestratorAdapter) Complete(ctx context.Context, prompt string) (string, error) {
    resp, err := a.Client.Complete(ctx, orchestrator.CompletionRequest{
        Prompt:      prompt,
        MaxTokens:   600,
        Temperature: 0.7,
        Operation:   "materials.select",
    })
    if err != nil {
        return "", err
    }
    return resp.Content, nil
}
```

**设计要点**：
- `MaxTokens=600`: 素材选择需要返回 JSON 结构，略大于 review
- `Temperature=0.7`: 素材挑选需要一定创造性，高于 review 的 0
- `Operation="materials.select"`: 成本日志中标识为素材选择操作

### 2.2 app-server 集成

修改 `cmd/app-server/main.go`：

```go
materialSvc := materials.NewService(materialStore, corpusStore, &materials.OrchestratorAdapter{
    Client: orchestrator.NewClient(cfg, costWriter),
}, logger)
```

**收益**：
- 每次 LLM 调用自动写入 `ai_cost_logs`
- 成本归因到 `materials.select` 操作
- 无需手动调用 cost logging

### 2.3 测试增强

修复 `orchestrator.MockClient` 初始化问题：

```go
func NewMockClient(content string) *MockClient {
    return &MockClient{
        Response: CompletionResponse{Content: content},
        Calls:    []CompletionRequest{},  // 修复：初始化 Calls 切片
    }
}
```

**问题背景**: 之前 `Calls` 字段为 `nil`，导致测试中无法记录调用。修复后 `materials` 和 `topic` 测试都能验证 LLM 调用参数。

---

## 三、改动文件

### 新增文件
- `internal/materials/orchestrator_adapter.go` - 适配器实现
- `internal/materials/orchestrator_adapter_test.go` - 单元测试

### 修改文件
- `cmd/app-server/main.go` - 使用 OrchestratorAdapter + costWriter
- `internal/orchestrator/mock.go` - 修复 Calls 初始化问题

---

## 四、验证结果

### 4.1 编译验证
```bash
$ go build ./cmd/app-server
# 通过，无编译错误
```

### 4.2 单元测试
```bash
$ go test ./internal/materials/
ok  github.com/FluentWork/fluentwork-backend/internal/materials  1.647s
```

**测试覆盖**：
- ✅ 适配器正确调用 orchestrator.Client
- ✅ 参数映射正确（MaxTokens=600, Temperature=0.7, Operation="materials.select"）
- ✅ 返回值正确传递
- ✅ MockClient 记录调用参数

### 4.3 集成测试
```bash
$ go test ./internal/orchestrator/ ./internal/materials/
ok  github.com/FluentWork/fluentwork-backend/internal/orchestrator  0.649s
ok  github.com/FluentWork/fluentwork-backend/internal/materials     1.647s
```

---

## 五、后续计划

### 已完成
- ✅ B18 review 迁移（commit `8d3e9d9`）
- ✅ B21 materials 迁移（commit `dd9dee1`）
- ✅ B23 topic 迁移（commit `3744a93`）

### 待完成
- [ ] drill 模块自身迁移（drill 的 TTS/打分功能）
- [ ] 废弃 `drill.ArkCompleter`（所有模块迁移后）
- [ ] 监控 `ai_cost_logs` 成本数据质量

---

## 六、技术亮点

1. **零侵入迁移**: `materials.Service` 不需要改动，只改变依赖注入
2. **参数语义化**: `Temperature=0.7` 反映素材选择需要创造性的业务需求
3. **测试可维护性**: MockClient 简化了 materials 模块的单元测试

---

**迁移人**: Claude Code  
**审查人**: （待补充）  
**合并方式**: rebase and merge
