# drill.ArkCompleter 废弃计划

**文档编号**: 73  
**创建时间**: 2026-09-16  
**状态**: 观察期  
**预计废弃时间**: 2026-09-30 (观察期 2 周)

---

## 一、废弃原因

`drill.ArkCompleter` 是早期临时实现，现已被 `orchestrator.Client` 统一替代：

### 1.1 架构问题

- **职责不清**: drill 模块不应直接管理 LLM 调用
- **重复实现**: 每个模块都有自己的 Ark 客户端，代码冗余
- **成本记账**: 无法自动记录 LLM 成本到 `ai_cost_logs`
- **测试困难**: 每次测试都需要 mock HTTP，增加测试复杂度

### 1.2 迁移完成度

截至 2026-09-16，所有核心模块已完成迁移：

| 模块 | 迁移状态 | Commit | 适配器 |
|------|---------|--------|--------|
| review | ✅ 已迁移 | 8d3e9d9 | `review.OrchestratorAdapter` |
| materials | ✅ 已迁移 | dd9dee1 | `materials.OrchestratorAdapter` |
| topic | ✅ 已迁移 | 3744a93 | `topic.OrchestratorAdapter` |
| drill | ✅ 已迁移 | c1f306d | `drill.OrchestratorAdapter` |

---

## 二、废弃计划

### 2.1 观察期 (2周: 2026-09-16 ~ 2026-09-30)

**目标**: 确保迁移稳定，无生产问题

**监控指标**:
- `ai_cost_logs` 写入量是否正常
- LLM 调用延迟 (p50/p99)
- 错误率 (orchestrator vs drill)
- 测试套件稳定性

**回退条件**:
如果发现以下任一问题，立即回退到 `drill.ArkCompleter`：
- `ai_cost_logs` 丢失超过 5% 的记录
- LLM 调用 p99 延迟增加超过 20%
- orchestrator 相关错误率 > 0.1%

### 2.2 标记废弃 (2026-09-30)

在代码中添加废弃警告：

```go
// Deprecated: ArkCompleter is deprecated and will be removed in v1.2.
// Use orchestrator.Client with drill.OrchestratorAdapter instead.
//
// Example:
//   client := orchestrator.NewClient(cfg, costWriter)
//   adapter := &drill.OrchestratorAdapter{Client: client}
//   judge := &drill.LLMJudge{LLM: adapter}
type ArkCompleter struct {
	// ...
}
```

### 2.3 移除代码 (2026-10-14, ~2周后)

**前置条件**:
- ✅ 观察期无问题
- ✅ 所有模块已迁移
- ✅ CI/CD 测试全部通过
- ✅ 生产环境运行 2 周无异常

**移除清单**:
1. 删除 `internal/drill/ark.go`
2. 删除 `drill.NewArkCompleter(cfg)` 函数
3. 更新 `internal/drill/service.go` 移除对 `ArkCompleter` 的引用
4. 更新所有相关文档

---

## 三、迁移成果总结

### 3.1 统一架构

所有 LLM 调用现在通过 `orchestrator.Client` 接口：

```
┌─────────────────────────────────────────┐
│         orchestrator.Client              │
│  (统一 LLM 抽象 + 自动成本记账)           │
└─────────────────┬───────────────────────┘
                  │
      ┌───────────┼───────────┬───────────┐
      │           │           │           │
   review     materials    topic       drill
 Adapter      Adapter     Adapter     Adapter
```

### 3.2 自动成本记账

所有模块的 LLM 调用成本自动写入 `ai_cost_logs`：

| Operation | MaxTokens | Temperature | 记账状态 |
|-----------|-----------|-------------|---------|
| review.eval | 500 | 0.0 | ✅ 自动 |
| materials.select | 600 | 0.7 | ✅ 自动 |
| topic.generate | 800 | 0.7 | ✅ 自动 |
| drill.judge | 200 | 0.0 | ✅ 自动 |

### 3.3 测试体验提升

使用 `orchestrator.MockClient` 替代 HTTP mock：

**Before**:
```go
// 需要 httptest.NewServer，编写复杂的 handler
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    // ... 50+ lines of mock logic
}))
defer server.Close()
```

**After**:
```go
// 一行搞定
mock := orchestrator.NewMockClient(`{"pass":true}`)
adapter := &drill.OrchestratorAdapter{Client: mock}
```

### 3.4 代码简化

- **删除**: ~150 行重复的 HTTP 客户端代码
- **新增**: ~60 行适配器代码 (4个模块)
- **净减少**: ~90 行代码

---

## 四、技术债务登记

### 4.1 已解决

- ✅ 重复 Ark 客户端实现 → 统一到 orchestrator
- ✅ 成本记账手动/不一致 → 自动化到 orchestrator
- ✅ 测试 HTTP mock 复杂 → MockClient 简化

### 4.2 遗留问题

- `reviewgen.ArkGenerator` 未迁移 (B8 review 生成器，独立模块)
- 部分测试仍依赖环境变量 `ARK_EP_DRILL`

---

## 五、参考文档

- [69_B16_AIOrchestrator_实施说明.md](69_B16_AIOrchestrator_实施说明.md) - orchestrator 设计
- [70_B18_orchestrator_migration_实现说明.md](70_B18_orchestrator_migration_实现说明.md) - review 迁移
- [71_B21_materials_迁移说明.md](71_B21_materials_迁移说明.md) - materials 迁移
- [72_B23_topic_迁移说明.md](72_B23_topic_迁移说明.md) - topic 迁移

---

## 六、决策记录

| 日期 | 决策 | 理由 |
|------|------|------|
| 2026-09-16 | 观察期 2 周 | 确保生产稳定性 |
| 2026-09-16 | 保留 NewArkCompleter | 回退安全阀 |
| TBD | 移除时间 | 观察期后确定 |

---

**审查人**: @backend-team  
**批准人**: @tech-lead
