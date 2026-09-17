# B23 topic 模块迁移到 orchestrator

**迁移时间**: 2026-09-16  
**Commit**: `3744a93`  
**状态**: ✅ 已完成并合并到 main

---

## 一、迁移目标

将 `internal/topic` 模块从直接使用 `drill.ArkCompleter` 迁移到统一的 `orchestrator.Client`，实现：

1. **统一 LLM 抽象**: 三大核心模块（review/materials/topic）全部使用 orchestrator
2. **双端迁移**: app-server 和 worker 都迁移完成
3. **自动成本记账**: 话题生成成本自动记录

---

## 二、实现方案

### 2.1 适配器模式

创建 `topic.OrchestratorAdapter` 桥接两个接口：

```go
// topic.Completer 接口（不变）
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
        MaxTokens:   800,
        Temperature: 0.7,
        Operation:   "topic.generate",
    })
    if err != nil {
        return "", err
    }
    return resp.Content, nil
}
```

**设计要点**：
- `MaxTokens=800`: 话题卡生成需要更多 token（三张卡 + metadata）
- `Temperature=0.7`: 话题生成需要创造性，避免重复话题
- `Operation="topic.generate"`: 成本日志中标识为话题生成操作

### 2.2 app-server 集成

修改 `cmd/app-server/main.go`：

```go
topicGen := topic.NewGenerator(topicStore, &topic.OrchestratorAdapter{
    Client: orchestrator.NewClient(cfg, costWriter),
}, topic.PracticeSignals{Blocks: corpusStore, Sessions: sessionStore})
```

### 2.3 worker 集成

修改 `cmd/worker/main.go`：

```go
topicSched := topic.NewScheduler(topic.NewGenerator(topicStore, &topic.OrchestratorAdapter{
    Client: orchestrator.NewClient(cfg, nil),
}, topic.PracticeSignals{Blocks: corpusStore, Sessions: store}), store, logger)
```

**注意**: worker 暂时传 `nil` 给 CostWriter（后续统一 worker 成本记账方案）

### 2.4 清理未使用 import

worker 迁移后不再使用 `drill` 包：

```go
// 移除
import "github.com/FluentWork/fluentwork-backend/internal/drill"
```

---

## 三、改动文件

### 新增文件
- `internal/topic/orchestrator_adapter.go` - 适配器实现
- `internal/topic/orchestrator_adapter_test.go` - 单元测试

### 修改文件
- `cmd/app-server/main.go` - app-server 使用 OrchestratorAdapter + costWriter
- `cmd/worker/main.go` - worker 使用 OrchestratorAdapter，移除 drill import

---

## 四、验证结果

### 4.1 编译验证
```bash
$ go build ./cmd/app-server
# 通过

$ go build ./cmd/worker
# 通过（修复 drill import 未使用错误后）
```

### 4.2 单元测试
```bash
$ go test ./internal/topic/
ok  github.com/FluentWork/fluentwork-backend/internal/topic  (cached)
```

**测试覆盖**：
- ✅ 适配器正确调用 orchestrator.Client
- ✅ 参数映射正确（MaxTokens=800, Temperature=0.7, Operation="topic.generate"）
- ✅ 返回值正确传递
- ✅ MockClient 记录调用参数

### 4.3 全模块集成测试
```bash
$ go test ./internal/orchestrator/ ./internal/review/ ./internal/materials/ ./internal/topic/
ok  github.com/FluentWork/fluentwork-backend/internal/orchestrator  0.649s
ok  github.com/FluentWork/fluentwork-backend/internal/review        1.168s
ok  github.com/FluentWork/fluentwork-backend/internal/materials     1.647s
ok  github.com/FluentWork/fluentwork-backend/internal/topic         (cached)
```

---

## 五、迁移里程碑

### ✅ 已完成三大核心模块迁移

| 模块 | Commit | MaxTokens | Temperature | Operation | 状态 |
|------|--------|-----------|-------------|-----------|------|
| review | `8d3e9d9` | 500 | 0 | review.eval | ✅ |
| materials | `dd9dee1` | 600 | 0.7 | materials.select | ✅ |
| topic | `3744a93` | 800 | 0.7 | topic.generate | ✅ |

### 统一收益

1. **统一抽象**: 三个模块都使用 `orchestrator.Client` 接口
2. **自动成本记账**: app-server 统一通过 `costWriter` 自动记录
3. **测试友好**: 统一使用 `orchestrator.MockClient` 简化测试
4. **参数语义化**: 每个模块的 MaxTokens/Temperature 反映业务需求

---

## 六、后续计划

### 已完成
- ✅ B16 orchestrator 启动包（commit `2e69945`）
- ✅ B18 review 迁移（commit `8d3e9d9`）
- ✅ Wire CostWriter（commit `9a73df0`）
- ✅ B21 materials 迁移（commit `dd9dee1`）
- ✅ B23 topic 迁移（commit `3744a93`）

### 待完成
- [ ] drill 模块自身迁移（drill.Service 的 TTS/打分功能）
- [ ] worker 统一成本记账方案（目前 worker 传 `nil` 给 CostWriter）
- [ ] 废弃 `drill.ArkCompleter`（所有模块迁移后）
- [ ] 监控 `ai_cost_logs` 数据质量

---

## 七、技术亮点

1. **双端迁移**: app-server 和 worker 都完成迁移，覆盖所有 topic 生成场景
2. **import 清理**: 及时移除未使用的 `drill` import，保持代码整洁
3. **参数调优**: `MaxTokens=800` 是三个模块中最大的，反映话题卡内容复杂度
4. **完整性**: 三大核心模块迁移完成，orchestrator 成为 LLM 调用的统一入口

---

## 八、drill.ArkCompleter 废弃路线图

### 当前状态（2026-09-16）

```
✅ review    → orchestrator.Client  
✅ materials → orchestrator.Client  
✅ topic     → orchestrator.Client  
⏳ drill     → 仍使用 drill.ArkCompleter（TTS/打分功能）
```

### 废弃计划

1. **Phase 1（已完成）**: 迁移三大核心模块
2. **Phase 2（下一步）**: 迁移 drill 模块自身
3. **Phase 3（最后）**: 移除 `drill.ArkCompleter` 代码

**预计时间**: 1-2 周观察期后废弃

---

**迁移人**: Claude Code  
**审查人**: （待补充）  
**合并方式**: rebase and merge
