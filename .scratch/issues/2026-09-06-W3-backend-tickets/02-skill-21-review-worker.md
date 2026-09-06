# Skill #21 — review worker 真实化

> **Master**: `FluentWork/fluentwork-backend#21` (OPEN)
> **Sub-tickets**: 5 (T-REV-1..5)
> **总工时**: 2.4 dev-day(原 #21 估 5d)
> **SLA**: **9/20 W3 末**(53_ §五之二 F-2)
> **依赖**: T-EVAL-1(评估集数据脚本,前置)

---

## §0 Master Issue 评论草稿(贴 #21)

```markdown
## 🎯 Skills-to-Ticket 拆解(2026-09-06)

按 Matt Pocock skills-to-ticket 方法,把 #21 切成 5 个 atomic sub-ticket,每个 ≤ 0.5 dev-day。

| # | Ticket | 工时 | 阻塞 |
|---|---|---|---|
| T-REV-1 | stub 替换为真实 LLM call + JSON Schema 校验 | 1.0d | T-EVAL-1 |
| T-REV-2 | 失败重试 1 次 + 死信写 review_jobs.error | 0.4d | T-REV-1 |
| T-REV-3 | 幂等键 session_id+task_type + 重复消费跳过 | 0.3d | T-REV-1 |
| T-REV-4 | ai_cost_logs 落账(同事务) | 0.4d | T-REV-1 |
| T-REV-5 | session.status=reviewed 驱动 + Prompt 版本字段 | 0.3d | T-REV-1 |

### 验收口径(Master 级,53_ §F-3 #21)
- [ ] 评价 + 炼化合并为单次旗舰模型调用,输出 JSON Schema 校验
- [ ] 失败重试 1 次,重试仍失败写 review_jobs.error 死信
- [ ] 幂等键 session_id + task_type,重复消费直接跳过
- [ ] 成本落账必同步:ai_cost_logs 与 review 结果同一事务
- [ ] 完成后 session.status = reviewed 由本路径驱动
- [ ] Prompt 以 prompt_configs 最小配置 + 版本字段,避免硬编码

### 关联
- 启动包:`docs/40_研发流程与协作/69_B18_review_eval_Issue_Draft_2026-09-06.md`(下游)
- #21 是 #28 评估集的前置被依赖方
- 详见:`.scratch/issues/2026-09-06-W3-backend-tickets/02-skill-21-review-worker.md`
```

---

## §1 T-REV-1 — stub 替换为真实 LLM call + JSON Schema 校验

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `ai-worker`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
替换 `internal/aiworker/review.go` 的 stub 实现为真实 Ark 旗舰模型调用。

## 📋 实施步骤
1. 修改 `internal/aiworker/review.go`:
   - 删 stub,改调 `internal/ark.Client.ChatCompletion(ctx, req)`
   - req.Model = `ARK_EP_REVIEW_REFINE`
   - req.ResponseFormat = JSON Schema(`{evaluation: {...}, refine: {...}}`)
2. 新建 `internal/aiworker/review_schema.go`:JSON Schema 定义 + 校验
3. prompt 从 `prompt_configs` 表读(`SELECT template FROM prompt_configs WHERE name='review_v1' AND version='latest'`)
4. 失败处理:见 T-REV-2;幂等:见 T-REV-3

## ✅ 验收
- [ ] 真实 Ark Mini 调用成功(JSON Schema 校验通过)
- [ ] 评价输出含三层(intent / phrasing / delivery)+ 炼化三元组
- [ ] 单测 `TestReview_RunLLMCall_Success` / `TestReview_JSONSchema_Reject` PASS
- [ ] 集成测试 `TestReview_End2End_RealArk`(需凭证)PASS

## 🔗 依赖
- Blocked by: T-EVAL-1(评估集 min 30 条样本,提示词回归基线)
- Blocks: T-REV-2..5
- Master: #21
```

---

## §2 T-REV-2 — 失败重试 1 次 + 死信写 `review_jobs.error`

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `ai-worker`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
LLM 调用失败 / Schema 校验失败 → 重试 1 次,仍失败写 `review_jobs.error` 死信。

## 📋 实施步骤
1. 新建 `internal/aiworker/retry.go`:`withRetry(ctx, fn, opts)`:
   - 默认 opts:maxRetries=1, baseDelay=2s, maxDelay=8s
   - 仅对 transient error(5xx / network / timeout)重试
   - JSON Schema 校验失败**不重试**(直接死信)
2. 死信逻辑:`UPDATE review_jobs SET status='dead_letter', error=?, updated_at=NOW() WHERE id=?`
3. 死信 metric:`aiworker_review_dead_letter_total{task_type='review'}`

## ✅ 验收
- [ ] transient error 重试 1 次后成功(返回正常结果)
- [ ] JSON Schema 错误**不重试**,直接死信
- [ ] 单测 `TestRetry_TransientError_Once` / `TestRetry_SchemaError_NoRetry` PASS

## 🔗 依赖
- Blocked by: T-REV-1
- Blocks: 无
- Master: #21
```

---

## §3 T-REV-3 — 幂等键 `session_id+task_type` + 重复消费跳过

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `ai-worker`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
`review_jobs` 表加 unique key `(session_id, task_type)`,重复消费直接跳过。

## 📋 实施步骤
1. 新建迁移 `0013_add_review_jobs_unique_key.sql`:
   ```sql
   ALTER TABLE review_jobs
     ADD UNIQUE KEY uk_session_task (session_id, task_type);
   ```
2. 修改 `internal/aiworker/review.go` 消费逻辑:
   - `INSERT IGNORE` 或 `ON DUPLICATE KEY UPDATE` 处理重复
   - 重复时直接 ACK 消息,记录 metric `aiworker_review_dedup_total`
3. 软删除兼容:`status='dead_letter'` 行不算重复

## ✅ 验收
- [ ] 同一 `session_id+task_type` 并发 100 次,仅 1 次执行
- [ ] 软删除行存在时,新 INSERT 仍成功(走 ON DUPLICATE)
- [ ] 单测 `TestReview_Idempotency_100Concurrent` PASS
- [ ] 集成测试:故意重发同一消息,第二次无副作用

## 🔗 依赖
- Blocked by: T-REV-1
- Blocks: 无
- Master: #21
```

---

## §4 T-REV-4 — `ai_cost_logs` 落账(同事务)

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `ai-worker`, `aicost`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
review 结果与 `ai_cost_logs` INSERT 同一事务;失败一起回滚。

## 📋 实施步骤
1. 修改 `internal/aiworker/review.go`:
   - 调 LLM 后,用 `internal/aicost.RecordCostTx(ctx, tx, costLog)` 同事务写
   - costLog 字段:`task_type='review'` / `model=ARK_EP_REVIEW_REFINE` / `tokens_in` / `tokens_out` / `cost_fen`
2. 修改 `internal/aicost/recorder.go`:支持事务版本 `RecordCostTx`
3. 失败回滚验证:故意 mock LLM 成功但 cost log 失败 → review 结果也回滚

## ✅ 验收
- [ ] 成功路径:`review_jobs.status='done'` + `ai_cost_logs` 有对应记录
- [ ] 失败路径:`cost_log` 失败 → review 回滚,DB 无脏数据
- [ ] 单测 `TestReview_CostLog_Success` / `TestReview_CostLog_Rollback` PASS

## 🔗 依赖
- Blocked by: T-REV-1
- Blocks: 无
- Master: #21
```

---

## §5 T-REV-5 — `session.status=reviewed` 驱动 + Prompt 版本字段

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `ai-worker`, `session`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
review 完成后驱动 `session.status='reviewed'` + `reviewed_at` 时间戳;Prompt 加版本字段。

## 📋 实施步骤
1. 修改 `internal/aiworker/review.go`:
   - review 成功后 `UPDATE sessions SET status='reviewed', reviewed_at=NOW() WHERE id=? AND user_id=?`
2. `prompt_configs` 表加 `version` 字段(已存在,确认 schema):
   - 插入时 `version=YYYYMMDD-N`,e.g. `20260906-1`
   - 读时 `ORDER BY version DESC LIMIT 1`
3. 写 `docs/40_研发流程与协作/49_…` 迁移文件模板说明(若需新迁移)

## ✅ 验收
- [ ] review 成功后 sessions 表 `status='reviewed'`
- [ ] Prompt 版本字段非空,支持历史回溯
- [ ] 单测 `TestReview_SessionStatus_Updated` / `TestPrompt_Version_LatestFirst` PASS

## 🔗 依赖
- Blocked by: T-REV-1
- Blocks: 无
- Master: #21
```

---

## §6 执行顺序与总工时

```
T-EVAL-1 (前置) ──▶ T-REV-1 (1.0d) ──▶ T-REV-2..5 (并行)
                                     ├─ T-REV-2 (0.4d)
                                     ├─ T-REV-3 (0.3d)
                                     ├─ T-REV-4 (0.4d)
                                     └─ T-REV-5 (0.3d)
```

| 人员 | 任务 | 时长 |
|---|---|---|
| 后端 AI 组(B 同学) | T-REV-1 | 1.0d (W3 Day 2-3) |
| 后端 AI 组(B 同学) | T-REV-2..5 并行 | 0.4d (W3 Day 4) |

**关键路径**:T-REV-1 不能与 T-REV-2..5 并行(它们依赖 T-REV-1)
**优化**:T-REV-1 完成后,B 同学可串行 T-REV-2..5(共 1.4d,跨 3 天)
