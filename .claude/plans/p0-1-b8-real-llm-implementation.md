# P0-1 B8 真实 LLM 接入实施计划

**创建日期**: 2026-09-17  
**预计工期**: 1.5-2 dev-days  
**状态**: 待审批

---

## 一、现状分析

### 1.1 已完成的基础设施

✅ **orchestrator 框架** (docs/69):
- `orchestrator.Client` 接口已定义
- `ArkClient` 实现完成（支持 Ark API 调用）
- 成本记账自动化（`CostWriter`）
- Mock 客户端用于测试

✅ **reviewgen 适配器** (`internal/reviewgen/orchestrator_adapter.go`):
- `OrchestratorAdapter` 已实现 `Generator` 接口
- 自动调用 B15 评估集校验
- System Prompt 使用 `eval.SystemPrompt()`
- 返回格式包含 review + refine

✅ **worker 配置** (`cmd/worker/main.go` L61-67):
```go
reviewGenerator := &reviewgen.OrchestratorAdapter{
    Client: orchestrator.NewClient(cfg, costWriter),
}
svc.SetReviewGenerator(reviewGenerator)
```

✅ **评估集** (docs/05):
- 103 条样本回归基线
- B15 Schema 校验规则
- `eval.ValidateSample()` 自动验证

### 1.2 当前问题

❌ **stub 生成器正在使用** (`internal/session/review.go` L269):
```go
// 当前逻辑：reviewGen == nil 时使用 stub
if s.reviewGen == nil {
    return buildStubReviewArtifacts(session, utterances)
}
```

❌ **失败日志不完整** (docs/77 P0-15 B):
- 6 次失败记录为 `decode generated json: unexpected end of JSON input`
- **模型原始响应未落日志**，无法调试

❌ **空会话进入管线** (docs/77 P0-15 A):
- 每次「重新开始」创建空会话
- 空会话进 review 管线 → 生成器正确拒绝 → 产生 10 次空记录

---

## 二、实施方案

### 2.1 核心改动（零侵入式）

**关键发现**: worker 已经配置了 `OrchestratorAdapter`，但运行时发现 `reviewGen == nil`。

**根因**: 需要检查 worker 启动时 orchestrator.Client 是否正确初始化。

### 2.2 检查清单

1. ✅ Ark API 凭证配置 (`configs/volc.env.example`):
   - `ARK_API_KEY`
   - `ARK_BASE_URL`
   - `ARK_REVIEW_REFINE_EP` (模型 endpoint)

2. ⚠️ `orchestrator.NewClient()` 单例模式:
   ```go
   // internal/orchestrator/client.go L57
   func NewClient(cfg config.Config, costWriter CostWriter) Client {
       if defaultClient == nil {
           defaultClient = NewArkClient(cfg, costWriter)
       }
       return defaultClient
   }
   ```
   
   **潜在问题**: 如果 `cfg.ArkAPIKey` 为空，`ArkClient` 仍会创建但无法工作。

3. ⚠️ `reviewgen.OrchestratorAdapter.Enabled()`:
   ```go
   func (a *OrchestratorAdapter) Enabled() bool {
       return a.Client != nil
   }
   ```
   
   **仅检查 Client 非 nil**，不检查凭证是否配置。

### 2.3 待实施的改动

#### Phase 1: 诊断与修复（0.5 dev-day）

**Issue #1: 增强 orchestrator.ArkClient 凭证校验**

`internal/orchestrator/ark.go`:
```go
// NewArkClient 创建 Ark 客户端
func NewArkClient(cfg config.Config, costWriter CostWriter) *ArkClient {
    model := strings.TrimSpace(cfg.ArkReviewRefineEP)
    baseURL := strings.TrimSpace(cfg.ArkBaseURL)
    apiKey := strings.TrimSpace(cfg.ArkAPIKey)
    
    // 新增：凭证校验
    if apiKey == "" || model == "" {
        return nil // 或返回一个 disabled client
    }
    
    if baseURL == "" {
        baseURL = "https://ark.cn-beijing.volces.com/api/v3"
    }
    
    return &ArkClient{...}
}
```

**Issue #2: OrchestratorAdapter.Enabled() 改进**

`internal/reviewgen/orchestrator_adapter.go`:
```go
func (a *OrchestratorAdapter) Enabled() bool {
    if a.Client == nil {
        return false
    }
    // 新增：检查是否为 disabled ArkClient
    if ark, ok := a.Client.(*orchestrator.ArkClient); ok {
        return ark != nil && ark.IsConfigured()
    }
    return true
}
```

需要在 `ArkClient` 添加 `IsConfigured()` 方法：
```go
func (a *ArkClient) IsConfigured() bool {
    return a != nil && a.apiKey != "" && a.model != ""
}
```

**Issue #3: Worker 启动时诊断日志**

`cmd/worker/main.go` L104-109:
```go
reviewEnabled := reviewGenerator.Enabled()
logger.Info("review generator",
    "enabled", reviewEnabled,
    "endpoint", cfg.ArkReviewRefineEP,
)

// 新增：如果未启用，明确警告
if !reviewEnabled {
    logger.Warn("review generator is DISABLED - will use stub artifacts",
        "ark_api_key_set", cfg.ArkAPIKey != "",
        "ark_endpoint_set", cfg.ArkReviewRefineEP != "",
    )
}
```

#### Phase 2: 可观测性增强（P0-2，0.5 dev-day）

**Issue #4: 失败响应日志**

`internal/reviewgen/orchestrator_adapter.go` L41-50:
```go
resp, err := a.Client.Complete(ctx, orchestrator.CompletionRequest{
    SystemPrompt:   eval.SystemPrompt(),
    Prompt:         userPrompt(req),
    MaxTokens:      800,
    Temperature:    0,
    ResponseFormat: "json_object",
    Operation:      "reviewgen.generate",
})
if err != nil {
    // 新增：记录失败上下文
    logx.Error(ctx, "orchestrator completion failed",
        "session_id", req.SessionID,
        "scene_type", req.SceneType,
        "transcript_length", len(req.Transcript),
        "error", err,
    )
    return Result{}, err
}

content := strings.TrimSpace(resp.Content)
if content == "" {
    // 新增：区分空响应类型
    logx.Warn(ctx, "orchestrator returned empty content",
        "session_id", req.SessionID,
        "model", resp.Model,
        "tokens_in", resp.PromptTokens,
        "tokens_out", resp.OutputTokens,
        "latency_ms", resp.LatencyMS,
    )
    return Result{}, fmt.Errorf("orchestrator response missing content")
}

doc, err := parseGeneratedDocument(content)
if err != nil {
    // 新增：记录原始响应用于调试
    logx.Error(ctx, "failed to parse generated document",
        "session_id", req.SessionID,
        "model", resp.Model,
        "content_length", len(content),
        "content_preview", truncate(content, 200), // 新增辅助函数
        "raw_content", content, // 完整响应
        "error", err,
    )
    return Result{}, err
}
```

新增辅助函数：
```go
func truncate(s string, maxLen int) string {
    if len(s) <= maxLen {
        return s
    }
    return s[:maxLen] + "..."
}
```

**Issue #5: 区分失败类型**

`internal/session/review.go` L293-318，改进错误分类：
```go
result, err := retry(ctx, reviewRetryAttempts, func() (reviewgen.Result, error) {
    return s.reviewGen.Generate(ctx, reviewgen.Request{...})
})

if err != nil {
    // 新增：失败类型分类
    failureType := classifyReviewFailure(err, session, utterances)
    s.logger.Warn("review generator failed after retries",
        "session_id", session.ID,
        "user_id", session.UserID,
        "scene_type", session.SceneType,
        "stage", "orchestration",
        "attempts", reviewRetryAttempts,
        "failure_type", failureType, // 新增
        "err", err,
    )
    return buildStubReviewArtifacts(session, utterances)
}
```

新增辅助函数：
```go
func classifyReviewFailure(err error, session Session, utterances []Utterance) string {
    errMsg := err.Error()
    
    // 空会话（正确拒绝）
    if len(utterances) == 0 {
        return "empty_session"
    }
    
    // JSON 解析失败
    if strings.Contains(errMsg, "unexpected end of JSON") {
        return "json_truncated"
    }
    if strings.Contains(errMsg, "invalid character") {
        return "json_malformed"
    }
    
    // Schema 校验失败
    if strings.Contains(errMsg, "B15 validation") {
        return "schema_violation"
    }
    
    // 超时
    if strings.Contains(errMsg, "context deadline exceeded") {
        return "timeout"
    }
    
    // API 错误
    if strings.Contains(errMsg, "ark api error") {
        return "api_error"
    }
    
    return "unknown"
}
```

#### Phase 3: 空会话过滤（P0-3 依赖产品决策，暂缓）

当前先让生成器正确拒绝，等待产品决策后再改入口。

---

## 三、验收标准

### 3.1 功能验收

1. ✅ Worker 启动时日志显示 `review generator enabled=true`
2. ✅ 处理一个真实会话（≥1 utterance）后，回顾页显示真实评价（不是 stub）
3. ✅ `practice_sessions.review_json` 包含真实 `goal_achievement` / `issues` / `suggestions` / `comparisons`
4. ✅ `phrase_blocks` 表新增 3-5 个话术块（迷你会话 1-3 个）
5. ✅ 失败率 < 5%（幂等重试）

### 3.2 数据验收

1. ✅ `ai_cost_logs` 包含 `task_type=review.eval` 记录
2. ✅ Token 数量 > 0（`tokens_in` + `tokens_out`）
3. ✅ `cost_fen` 按公式计算正确（当前 = 0 待账单确认，但记录存在）
4. ✅ 锚点在转录中可找到（`anchor_user_said` ⊆ transcript）

### 3.3 质量验收

1. ✅ 评估集 103/103 样本通过（`go test ./internal/eval/...`）
2. ✅ Schema 校验通过（B15 规则）
3. ✅ 幂等键防重（`session_id` + `task_type`）

### 3.4 可观测性验收

1. ✅ 失败时日志包含 `failure_type`（空会话 / JSON 截断 / Schema 违约 / 超时 / API 错误）
2. ✅ JSON 解析失败时日志包含 `raw_content`（完整响应）
3. ✅ Worker 启动时若凭证缺失，显示明确警告

---

## 四、测试计划

### 4.1 本地测试

**Step 1: 凭证配置验证**
```bash
cd fluentwork-backend
source configs/volc.env.example  # 或实际凭证文件
echo "ARK_API_KEY=$ARK_API_KEY"
echo "ARK_REVIEW_REFINE_EP=$ARK_REVIEW_REFINE_EP"
```

**Step 2: 单元测试**
```bash
go test ./internal/orchestrator/... -v
go test ./internal/reviewgen/... -v
go test ./internal/eval/... -v
```

**Step 3: Worker 启动测试**
```bash
./scripts/dev-up.sh  # 启动 MySQL
go run cmd/worker/main.go

# 检查启动日志
# 期望: "review generator" "enabled" true
# 或: WARNING "凭证缺失"
```

**Step 4: 端到端测试（真实会话）**
```bash
# 1. 创建会话 + 说话 + 结束
go run cmd/smoke-review-ready/main.go

# 或手动：
curl -X POST http://localhost:8080/api/v1/sessions \
  -H "Authorization: Bearer <guest_token>" \
  -d '{"scene_type":"standup"}'

# 2. 通过 WSS 说话（或使用 iOS 客户端）

# 3. 结束会话
curl -X POST http://localhost:8080/api/v1/sessions/<id>/end

# 4. 等待 worker 处理（poll every 5s）

# 5. 查询回顾
curl http://localhost:8080/api/v1/sessions/<id>/review

# 验证：review_json 不是 stub（无 "Review generation is unavailable"）
```

**Step 5: 失败场景测试**
```bash
# 测试空会话
curl -X POST .../sessions -d '{"scene_type":"standup"}'
curl -X POST .../sessions/<id>/end  # 立即结束，无 utterance

# 检查日志: failure_type=empty_session

# 测试凭证缺失
unset ARK_API_KEY
go run cmd/worker/main.go
# 期望: WARNING "凭证缺失" + enabled=false
```

### 4.2 评估集回归

```bash
# B15 评估集（103 样本）
go test ./internal/eval/... -v -run TestValidateSample

# 预期：全部 PASS
```

### 4.3 真机测试

1. iOS 客户端完成一场对话
2. 进入回顾页，验证：
   - ✅ 目标达成度显示真实判断
   - ✅ 问题清单 ≥ 1 条
   - ✅ 提高建议 ≥ 1 条
   - ✅ 双栏对照 3-8 条
   - ✅ 炼化区 3-5 个话术块
3. 一键入库 → 语料库出现该块

---

## 五、回滚方案

### 5.1 Feature Flag（推荐）

如果需要紧急回退到 stub：

`cmd/worker/main.go`:
```go
reviewGenerator := &reviewgen.OrchestratorAdapter{
    Client: orchestrator.NewClient(cfg, costWriter),
}

// 新增：环境变量控制
if os.Getenv("FORCE_STUB_REVIEW") == "true" {
    logger.Warn("FORCE_STUB_REVIEW enabled - review generator disabled")
    reviewGenerator = nil
}

svc.SetReviewGenerator(reviewGenerator)
```

使用：
```bash
FORCE_STUB_REVIEW=true ./cmd/worker/main
```

### 5.2 代码回滚

如果出现严重问题，回退到当前 commit：
```bash
git revert <this-pr-commit>
```

Stub 生成器逻辑未改动，回退后自动使用 stub。

---

## 六、风险评估

### 6.1 技术风险

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| Ark API 不稳定（超时 / 限流） | 🟡 中 | 🟡 中 | 重试 3 次 + stub 兜底 |
| JSON 格式不稳定 | 🟡 中 | 🟡 中 | B15 校验 + 失败日志完整 |
| 凭证未配置 | 🟢 低 | 🔴 高 | 启动时检查 + 明确警告 |
| 成本超预期 | 🟢 低 | 🟡 中 | ai_cost_logs 实时监控 |

### 6.2 产品风险

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| 评价质量低于预期 | 🟡 中 | 🟡 中 | B15 评估集回归 + 人工抽查 |
| 空会话产生垃圾数据 | 🟡 中 | 🟢 低 | 生成器正确拒绝（当前） + 入口过滤（P0-3） |
| 延迟过高（P90 > 15s） | 🟢 低 | 🟡 中 | Ark API 超时 30s + 异步处理 |

---

## 七、实施步骤

### Day 1 上午（2h）

1. **凭证验证**
   - 检查 `configs/volc.env.example`
   - 测试 Ark API 连通性（curl）
   
2. **Phase 1: Issue #1-3**
   - `orchestrator.ArkClient` 凭证校验
   - `OrchestratorAdapter.Enabled()` 改进
   - Worker 启动诊断日志

3. **单元测试**
   - `TestArkClient_IsConfigured()`
   - `TestOrchestratorAdapter_Enabled()`

### Day 1 下午（4h）

4. **Phase 2: Issue #4-5**
   - 失败响应日志（`raw_content`）
   - 失败类型分类（`classifyReviewFailure`）

5. **集成测试**
   - Worker 启动 + 凭证正常
   - Worker 启动 + 凭证缺失
   - 空会话拒绝（failure_type=empty_session）

6. **端到端测试**
   - `smoke-review-ready` 脚本
   - 验证 review_json 真实内容
   - 验证 phrase_blocks 生成

### Day 2 上午（2h）

7. **评估集回归**
   - `go test ./internal/eval/...`
   - 103/103 通过

8. **真机测试**
   - iOS 客户端完整流程
   - 回顾页验证

### Day 2 下午（2h）

9. **失败场景测试**
   - JSON 截断模拟
   - API 超时模拟
   - Schema 违约模拟

10. **文档更新**
    - 更新 `docs/问题分析与规划_2026-09-17.md`
    - 创建 `docs/82_B8_真实LLM接入实现说明.md`
    - 更新 `README.md`（如需）

---

## 八、后续工作

### W6 紧接（P0-2 完成后）

- **E1-E5 闪测**（依赖话术块数据）
- **H1-H2 话题建议**（依赖语料库数据）
- **P1-1 救援→炼化接口**（完善炼化输入）

### W7

- **P1-2 命中回写**（`real_use_count` + `success_streak`）
- **P1-3 救援音频**（依赖 P2-4 TTS 授权）

### 产品决策后

- **P0-3 空会话过滤**（入口改造）
- **P0-13 工作台模块形态**（会话列表 vs 新建）

---

## 九、成功指标

### 即时指标（W5 Day 5）

- ✅ Worker 启动 `enabled=true`
- ✅ 真实会话回顾率 ≥ 95%
- ✅ Stub 降级率 ≤ 5%
- ✅ P90 延迟 ≤ 15s

### 短期指标（W6-W7）

- ✅ 话术块生成率 ≥ 90%（每场 3-5 个）
- ✅ 闪测数据充足（语料库 ≥ 10 块即可调度）
- ✅ 北极星路径打通（说话 → 评价 → 炼化 → 入库 → 闪测 → 命中）

### 长期指标（V1.0 发布）

- ✅ 用户满意度（回顾页有用性）
- ✅ 话术块复用率（实战命中次数）
- ✅ LLM 成本 ≤ 订阅价 30%

---

*计划结束。等待审批后开始实施。*
