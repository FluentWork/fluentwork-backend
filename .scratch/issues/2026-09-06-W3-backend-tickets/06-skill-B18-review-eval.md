# Skill B18 — review eval 真实 LLM 接入(新建 master)

> **Master**: 待新建 `B18: review eval 真实 LLM 接入`
> **Sub-tickets**: 6 (T-B18-1..6)
> **总工时**: 1.0 dev-day(与 69_ 启动包一致)
> **阻塞**: D-1 ✅, 0008 ✅, B16 AIOrchestrator CLOSED
> **关联**: I16 iOS 完整转录浮层(下游)

---

## §0 Master Issue Body(整段可粘贴)

**Title**: `B18: review eval 真实 LLM 接入`  
**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `review`, `llm`  
**Milestone**: `V2.0 W3-W5`

```markdown
## 🎯 目标
实施会话结束后 review eval 真实 LLM 接入(D-1 拍板:Ark Mini 异步批处理):
- `GET /api/v1/sessions/:session_id/review` 返回 `eval` 字段(score / dims / suggestions)
- 会话结束 → 后台异步触发 eval job → 落 `reviews` 表 → 客户端轮询 /review 直到 `status=ready`
- 落地 3 维评分(grammar / fluency / vocabulary)+ 个性化建议

> **范围**:仅 review eval;闪测判定(B22)是另一条独立链路(D-3 Ark Mini 同步调用)

## 🚧 阻塞条件
- D-1 已 ✅ 拍板(Ark Mini + 深度思考异步)
- 0008 迁移已就绪 ✅(49_ § 0008:alter_utterances_add_llm_eval)
- C-1 凭证:Ark Mini API Key + endpoint(W3 前到位)
- B16 AIOrchestrator 已 CLOSED(提供 `LLMClient` 接口)
- staging 已 apply 0008 迁移

## 📐 Sub-tickets

| # | Ticket | 工时 | 阻塞 |
|---|---|---|---|
| T-B18-1 | 数据模型 + Store 接口 | 0.2d | 无(0008 ✅) |
| T-B18-2 | Evaluator + LLM Prompt + JSON 解析 | 0.3d | T-B18-1, B16 CLOSED |
| T-B18-3 | Service.RunEvalJob + 限速 1 QPS | 0.2d | T-B18-2 |
| T-B18-4 | EnqueueEval 触发点 + session.EndSession 联动 | 0.1d | T-B18-3 |
| T-B18-5 | HTTP 路由 + Handler + 200/202/404 | 0.1d | T-B18-3 |
| T-B18-6 | OpenAPI 同步 + metric + 文档 | 0.1d | T-B18-5 |

## ✅ Master 验收(69_ DoD)
- [ ] 6 个 sub-ticket 全部完成并 merge
- [ ] `internal/review/` 整个包实施
- [ ] `go test ./internal/review/...` 14 个测试通过
- [ ] 端到端冒烟:本地 mock LLM + 真实 session 走通完整 review
- [ ] 性能:session 50 句 eval 任务 P95 ≤ 60s
- [ ] OpenAPI 同步:`/sessions/{session_id}/review` 含 200/202/401/404
- [ ] metric:`review_eval_timeout_total` + `review_eval_parse_error_total`

## 🔗 关联
- 上游依赖:B16 AIOrchestrator(提供 `LLMClient` 接口)
- 下游:I16 iOS 完整转录浮层(消费 review.eval 字段)
- 启动包:`docs/40_研发流程与协作/69_B18_review_eval_Issue_Draft_2026-09-06.md`
```

---

## §1 T-B18-1 — 数据模型 + Store 接口

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `review`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
定义 `Review` / `UtteranceWithEval` / `EvalSummary` / `EvalDims` 数据模型 + Store CRUD 接口。

## 📋 实施步骤
1. 新建 `internal/review/model.go`:
   - `Review` / `UtteranceWithEval` / `EvalSummary` / `EvalDims` 结构体(参考 69_ §4)
   - JSON tag 完整
2. 新建 `internal/review/store.go`:
   - `GetUtterancesForEval(ctx, sessionID) ([]UtteranceForEval, error)`
   - `SaveReviews(ctx, sessionID, utts, results) error`
   - `GetReview(ctx, sessionID) (*Review, error)`
   - `MarkReviewStatus(ctx, sessionID, status) error`
3. SQL 表已就绪(0008 迁移已 apply)

## ✅ 验收
- [ ] 4 个结构体编译通过
- [ ] Store 接口可用(sqlmock 覆盖)
- [ ] 单测 `TestStore_GetUtterancesForEval` / `TestStore_SaveReviews` / `TestStore_GetReview` PASS

## 🔗 依赖
- Blocked by: 无(0008 已 apply)
- Blocks: T-B18-2
- Master: B18
```

---

## §2 T-B18-2 — Evaluator + LLM Prompt + JSON 解析

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `review`, `llm`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `Evaluator` + Prompt 模板 + JSON 解析 + fallback。

## 📋 实施步骤
1. 新建 `internal/review/eval_prompt.go`:Prompt 模板(D-1 拍板 Ark Mini)
2. 新建 `internal/review/evaluator.go`:
   - `Evaluator{llm LLMClient, logger}` 结构
   - `Evaluate(ctx, utt, hits) (*EvalResult, error)`:
     - 调 llm.Complete(3s timeout)
     - JSON 解析失败 → fallback EvalResult(score=0.5, suggestions=["系统繁忙，请稍后重试"])
     - 写 metric:`review_eval_timeout_total` + `review_eval_parse_error_total`
3. 单测 T-B18-4..8

## ✅ 验收
- [ ] T-B18-4 评分维度落库 PASS
- [ ] T-B18-5 suggestions ≤ 30 字符 PASS
- [ ] T-B18-6 异常 ASR(score=0.0)PASS
- [ ] T-B18-7 LLM 超时 fallback PASS
- [ ] T-B18-8 非 JSON fallback PASS

## 🔗 依赖
- Blocked by: T-B18-1, B16 AIOrchestrator CLOSED(LLMClient 接口)
- Blocks: T-B18-3
- Master: B18
```

---

## §3 T-B18-3 — Service.RunEvalJob + 限速 1 QPS

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `review`, `llm`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `Service.RunEvalJob` 异步执行 review eval,逐 utterance 限速 1 QPS。

## 📋 实施步骤
1. 新建 `internal/review/service.go`:
   - `Service{store, eval, hitsService, job, logger}`
   - `RunEvalJob(ctx, sessionID)`:
     - 拉 utts + 拉 hits(B19 联动)
     - 1 QPS 限速(`sem := make(chan struct{}, 1); time.Sleep(1s)`)
     - 落 reviews + 更新 review_status
2. 单测 T-B18-11 限速验证(20 句并发总耗时 ≥ 20s)
3. 性能测试 T-B18-14(session 50 句 P95 ≤ 60s)

## ✅ 验收
- [ ] T-B18-1 session 结束 → 20 reviews 落库 PASS
- [ ] T-B18-11 限速生效 PASS
- [ ] T-B18-14 性能 P95 ≤ 60s PASS

## 🔗 依赖
- Blocked by: T-B18-2
- Blocks: T-B18-4, T-B18-5
- Master: B18
```

---

## §4 T-B18-4 — EnqueueEval 触发点 + session.EndSession 联动

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `review`, `session`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
在 `internal/session/service.go` 的 `EndSession` 末尾调用 `reviewSvc.EnqueueEval`。

## 📋 实施步骤
1. 修改 `internal/session/service.go` 的 `EndSession` 末尾:
   ```go
   if err := s.reviewSvc.EnqueueEval(ctx, sessionID); err != nil {
       s.logger.Warn("enqueue eval failed", "session_id", sessionID, "err", err)
   }
   ```
2. `EnqueueEval` 写 JobQueue(B16 worker 范畴)
3. 集成测试:EndSession 后检查 job 队列非空

## ✅ 验收
- [ ] EndSession 触发 review 任务入队
- [ ] 入队失败不影响 session 结束主流程
- [ ] 单测 `TestSession_EndSession_EnqueuesEval` PASS

## 🔗 依赖
- Blocked by: T-B18-3, B16 worker JobQueue 接口就绪
- Blocks: 无
- Master: B18
```

---

## §5 T-B18-5 — HTTP 路由 + Handler + 200/202/404

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `review`, `api`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `GET /api/v1/sessions/:session_id/review` Handler,根据 review_status 返回 200 或 202。

## 📋 实施步骤
1. 新建 `internal/review/http.go`:
   - `RegisterRoutes(rg, h)`:GET /sessions/:session_id/review
   - `GetReview(c)`:
     - review_status=ready → 200 + Review 全字段
     - review_status=pending/queued → 202 + `{status, estimated_ready_at}`
     - 不存在 → 404
2. 单测 T-B18-2/3/9

## ✅ 验收
- [ ] T-B18-2 GET ready 后 PASS
- [ ] T-B18-3 GET pending 中 PASS
- [ ] T-B18-9 跨用户 403 PASS
- [ ] T-B18-10 空 session(score=0)PASS

## 🔗 依赖
- Blocked by: T-B18-3
- Blocks: T-B18-6
- Master: B18
```

---

## §6 T-B18-6 — OpenAPI 同步 + metric + 文档

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `review`, `api`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
OpenAPI 字段对齐 + metric 暴露 + 文档注释。

## 📋 实施步骤
1. 手动编辑 `api/openapi-v1.yaml` 加 `/sessions/{session_id}/review` 路径(参考 69_ §7)
2. metric:`review_eval_timeout_total` + `review_eval_parse_error_total` 暴露到 `/metrics`
3. handler 顶部注释加「Per 48 §1.3.3 + §1.3.7」
4. T-B18-12 A4 撤销后 review 复活测试(需要 B22 A4 已 CLOSED)
5. T-B18-13 OpenAPI 字段对齐

## ✅ 验收
- [ ] OpenAPI commit + `make api-lint` PASS
- [ ] metric 在 `/metrics` 出现
- [ ] 跨 B22 A4 软删除兼容验证

## 🔗 依赖
- Blocked by: T-B18-5, B22 A4 CLOSED(T-B18-12 验证)
- Blocks: 无(Master 收口)
- Master: B18
```

---

## §7 执行顺序与总工时

```
T-B18-1 (0.2d) ──▶ T-B18-2 (0.3d, 依赖 B16) ──▶ T-B18-3 (0.2d) ──┬──▶ T-B18-4 (0.1d)
                                                              └──▶ T-B18-5 (0.1d) ──▶ T-B18-6 (0.1d)
```

**总工时**:1.0 dev-day
**推荐 Owner**:后端 AI 组(G 同学)
**关键路径**:T-B18-2 阻塞 B16 AIOrchestrator CLOSED;建议 B16 启动包 W3 中期(9/12)补齐后立即启动 B18
