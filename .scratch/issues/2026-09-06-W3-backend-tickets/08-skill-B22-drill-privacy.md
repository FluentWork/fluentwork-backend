# Skill B22 — 闪测模块 + A4 隐私删除(新建 master)

> **Master**: 待新建 `B22: 闪测模块 + A4 隐私删除`
> **Sub-tickets**: 10 (T-B22-0..9)
> **总工时**: 2.5 dev-day(工作量最大的 skill)
> **阻塞**: 0011 ✅, D-3 ✅, D-4 ✅, D-API-2 ✅
> **关联**: I17 闪测 UI(下游), I22 F3 UI(下游), B24 历史回顾(下游消费 A4)

---

## §0 Master Issue Body(整段可粘贴)

**Title**: `B22: 闪测模块 + A4 隐私删除`  
**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `drill`, `privacy`, `migration`  
**Milestone**: `V2.0 W3-W4`

```markdown
## 🎯 目标
合并实施 3 个子任务(共享迁移 0012 + 软删除事务基础设施):
1. **闪测**(E1/E2/E3):`GET /api/v1/drill/round` + `POST /api/v1/drill/judge`
2. **隐私删除**(A4):`DELETE /api/v1/account/data` + `POST /api/v1/account/export` + 软删除矩阵
3. **撤销接口**:`POST /internal/v1/support/undelete-user`

合并理由:3 个子任务共享事务回滚模式、级联查询、Outbox tombstone 写入,分 3 个 Issue 反而增加集成成本。

## 🚧 阻塞条件
- 0011 迁移已就绪 ✅(49_ §2.4)
- 0012 迁移待本 Issue 创建(49_ §五 模板)
- D-3 已 ✅ 拍板(Ark Mini 闪测判定)
- D-4 已 ✅ 拍板(简化 SM-2 调度)
- D-API-2 已 ✅ 拍板(`/api/v1/account/data`)

## 📐 Sub-tickets

| # | Ticket | 工时 | 阻塞 |
|---|---|---|---|
| T-B22-0 | 0012 迁移 + 回滚 SQL | 0.2d | 无 |
| T-B22-1 | 闪测 SM-2 selectBlocksForRound | 0.3d | 无 |
| T-B22-2 | 闪测 Judge LLM Prompt + LLMJudge | 0.3d | B16 CLOSED |
| T-B22-3 | 闪测 Service + Handler + 路由 | 0.3d | T-B22-1, T-B22-2 |
| T-B22-4 | 闪测 drill_records 写入 + state 更新 | 0.2d | T-B22-3 |
| T-B22-5 | 隐私 PrivacyService 单事务 | 0.4d | T-B22-0 |
| T-B22-6 | 隐私 DELETE /account/data Handler | 0.2d | T-B22-5 |
| T-B22-7 | 隐私 POST /account/export Handler | 0.1d | T-B22-6 |
| T-B22-8 | 撤销 POST /support/undelete-user | 0.3d | T-B22-5 |
| T-B22-9 | OpenAPI 同步 + metric + 文档 | 0.2d | T-B22-4, T-B22-7, T-B22-8 |

## ✅ Master 验收(58_ DoD)
- [ ] 10 个 sub-ticket 全部完成并 merge
- [ ] 0011 + 0012 迁移在 staging apply 成功(含 0012.down.sql 回滚验证)
- [ ] `internal/drill` 包 9 个测试通过
- [ ] `internal/account/privacy_service` + `undelete_http` 14 个测试通过
- [ ] 端到端冒烟:完整 A4 流程走通(创建素材 → 闪测 → 删除 → 撤销 → 再查询)
- [ ] OpenAPI 同步:加 `/drill/{round,judge}` + `/account/{data,export}` + `/support/undelete-user`
- [ ] 性能:DELETE /account/data P95 ≤ 15s
- [ ] 审计日志:support undelete 写入 audit_logs 表

## 🔗 关联
- 上游依赖:B16 AIOrchestrator(LLMJudge 接口给闪测)
- 下游:iOS I17 闪测 UI(E1/E4/E5)、iOS I22 F3 UI、Backend B24 历史回顾
- 启动包:`docs/40_研发流程与协作/58_B22_闪测与A4隐私_Issue_Draft_2026-09-06.md`
```

---

## §1 T-B22-0 — 0012 迁移 + 回滚 SQL

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `migration`, `privacy`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
创建 0012 迁移 + 回滚 SQL,涉及 users / materials / practice_sessions / reviews / ai_cost_logs + tombstones 表。

## 📋 实施步骤
1. 新建 `migrations/0012_alter_privacy_soft_delete.sql`(参考 58_ §2.1)
2. 新建 `migrations/0012.down.sql`(回滚模板)
3. staging apply 验证 + 回滚验证
4. 更新 `docs/30_技术方案/49_FluentWork_V2_数据库迁移脚本总览_2026-09-06.md` §五

## ✅ 验收
- [ ] 迁移 apply 成功(staging)
- [ ] down.sql 回滚成功
- [ ] 5 张表 + tombstones 表 schema 正确
- [ ] 49_ §五 文档同步

## 🔗 依赖
- Blocked by: 无
- Blocks: T-B22-5
- Master: B22
```

---

## §2 T-B22-1 — 闪测 SM-2 `selectBlocksForRound`

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `drill`, `scheduler`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现简化版 SM-2 调度器(D-4 拍板)+ selectBlocksForRound SQL。

## 📋 实施步骤
1. 新建 `internal/drill/scheduler.go`:
   - `selectBlocksForRound(ctx, userID, size)`:
     ```sql
     SELECT * FROM phrase_blocks
     WHERE user_id = ? AND deleted_at IS NULL
       AND state IN ('new', 'training')
       AND next_due_at <= NOW()
     ORDER BY next_due_at ASC LIMIT ?;
     ```
   - 不足时补充 `state='automated'` 且 due
2. SM-2 参数:`easiness_factor=2.5`,`interval=24h/7d/30d`
3. 单测 T-D-1/2

## ✅ 验收
- [ ] T-D-1 SM-2 参数 PASS
- [ ] T-D-2 automated 块到期进入下轮 PASS
- [ ] T-E1-1 无 due 块返回 0 题 PASS
- [ ] T-E1-2 12 due 返回 10 题 PASS

## 🔗 依赖
- Blocked by: 无(0011 已 apply,phrase_blocks 表字段就绪)
- Blocks: T-B22-3
- Master: B22
```

---

## §3 T-B22-2 — 闪测 Judge LLM Prompt + LLMJudge

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `drill`, `llm`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现语义判定 LLM 调用 + Prompt 模板 + JSON 解析(D-3 Ark Mini)。

## 📋 实施步骤
1. 新建 `internal/drill/judge_prompt.go`:判定 Prompt 模板
2. 新建 `internal/drill/judge.go`:
   - `LLMJudge{llm}` 结构
   - `Judge(ctx, target, userSaid) (*JudgeResult, error)`:
     - 调 llm.Complete(1.5s timeout,参考 D-3)
     - JSON 解析失败 → `{pass: false, judge_reason: "judge_parse_error"}`
3. 单测 T-D-3 解析失败 metric

## ✅ 验收
- [ ] LLM 调用可 mock + JSON 解析 PASS
- [ ] 解析失败 fallback PASS

## 🔗 依赖
- Blocked by: B16 AIOrchestrator CLOSED(LLMJudge 接口)
- Blocks: T-B22-3
- Master: B22
```

---

## §4 T-B22-3 — 闪测 Service + Handler + 路由

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `drill`, `api`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `Service.Round` + `Service.Judge` + HTTP Handler。

## 📋 实施步骤
1. 新建 `internal/drill/service.go`:
   - `Service{store, llmJudge, logger}`
   - `Round(ctx, userID, size)` → *Round
   - `Judge(ctx, req JudgeRequest)` → *JudgeResponse
2. 新建 `internal/drill/http.go`:
   - `GET /api/v1/drill/round`
   - `POST /api/v1/drill/judge`
3. 单测 T-E1-1/2 + T-E2-1/4

## ✅ 验收
- [ ] T-E1-1/2 Round PASS
- [ ] T-E2-1/2 Judge semantic Pass + streak PASS
- [ ] T-E2-4 跨用户 403 PASS

## 🔗 依赖
- Blocked by: T-B22-1, T-B22-2
- Blocks: T-B22-4
- Master: B22
```

---

## §5 T-B22-4 — 闪测 drill_records 写入 + state 更新

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `drill`, `db`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
Judge 后落 `drill_records` 表 + 更新 `phrase_blocks.state` (SM-2 状态转换)。

## 📋 实施步骤
1. 扩展 `internal/drill/service.go` 的 `Judge`:
   - `INSERT INTO drill_records(...)` 单事务
   - 更新 `phrase_blocks`:
     - Fail → `next_due_at = now + 1h`
     - Pass + streak<3 → `success_streak+=1`, `next_due_at=now+24h`
     - Pass + streak>=3 → `state='automated'`, `next_due_at=now+7d`
     - automated Pass → `next_due_at=now+30d`
2. 单测 T-E2-1/2/3 完整 state 转换

## ✅ 验收
- [ ] T-E2-1 语义 Pass + streak=1 PASS
- [ ] T-E2-2 连续 3 次 Pass → automated PASS
- [ ] T-E2-3 Fail → next_due_at=now+1h PASS

## 🔗 依赖
- Blocked by: T-B22-3
- Blocks: T-B22-9
- Master: B22
```

---

## §6 T-B22-5 — 隐私 PrivacyService 单事务

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `privacy`, `db`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `PrivacyService.DeleteAllData` 单事务删除逻辑。

## 📋 实施步骤
1. 新建 `internal/account/privacy_service.go`:
   - `PrivacyService{store, outbox, logger}`
   - `DeleteAllData(ctx, userID, confirmationCode)`:
     - 校验 `confirmationCode == "DELETE-MY-DATA"`
     - 单事务:
       - users:UPDATE deleted_at + tombstone_at
       - materials / sessions / reviews / phrase_blocks:UPDATE deleted_at
       - drill_records:物理 DELETE
       - ai_cost_logs:UPDATE user_id_anonymized
       - tombstones:INSERT 多条
     - 返回 DeleteResult{cascaded map, backup_purge_at}
2. 单测 T-A4-1..2 + T-A4-9

## ✅ 验收
- [ ] T-A4-1 幂等 PASS
- [ ] T-A4-2 物理 vs 软删除矩阵 PASS
- [ ] T-A4-9 ai_cost_logs 匿名化 PASS
- [ ] 单事务回滚验证

## 🔗 依赖
- Blocked by: T-B22-0
- Blocks: T-B22-6, T-B22-8
- Master: B22
```

---

## §7 T-B22-6 — 隐私 DELETE /account/data Handler

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `privacy`, `api`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `DELETE /api/v1/account/data` HTTP Handler。

## 📋 实施步骤
1. 新建 `internal/account/privacy_http.go`:
   - `RegisterRoutes(rg, h)`:DELETE /account/data
   - `DeleteData(c)`:解析 `{confirmation_code}` → 调 service → 返回 DeleteResult
2. 单测 T-A4-1/4 + T-A4-10

## ✅ 验收
- [ ] T-A4-1 同确认码 2 次幂等 PASS
- [ ] T-A4-4 错误确认码 422 PASS
- [ ] T-A4-10 DELETE P95 ≤ 15s PASS

## 🔗 依赖
- Blocked by: T-B22-5
- Blocks: T-B22-7
- Master: B22
```

---

## §8 T-B22-7 — 隐私 POST /account/export Handler

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `privacy`, `api`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `POST /api/v1/account/export` 异步导出任务入口。

## 📋 实施步骤
1. 扩展 `internal/account/privacy_http.go`:
   - `RegisterRoutes`:POST /account/export
   - `PostExport(c)`:入队 ExportJob(B16 worker 范畴)→ 返回 `{export_id, email_to, estimated_ready_at}`
2. 单测 T-A4-6 导出任务入队 PASS
3. 占位:`actual email send` 由 V1.5 实施

## ✅ 验收
- [ ] T-A4-6 导出 7 天内收到邮件 PASS(端到端 mock)
- [ ] export_id 唯一性

## 🔗 依赖
- Blocked by: T-B22-6, B16 worker ExportJob 接口
- Blocks: T-B22-9
- Master: B22
```

---

## §9 T-B22-8 — 撤销 POST /support/undelete-user

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `privacy`, `api`, `internal`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 support 内部工具 `POST /internal/v1/support/undelete-user`。

## 📋 实施步骤
1. 新建 `internal/account/undelete_http.go`:
   - 校验 X-Internal-Token
   - 校验 30 天撤销窗口
   - 单事务:
     - users:deleted_at = NULL
     - materials/.../phrase_blocks/reviews:deleted_at = NULL
     - ai_cost_logs:user_id_anonymized = NULL
     - tombstones:DELETE WHERE user_id = ?
   - 写 audit_logs(who/when/reason)
2. 单测 T-A4-11..14

## ✅ 验收
- [ ] T-A4-11 30 天内撤销 PASS
- [ ] T-A4-12 30 天外 422 PASS
- [ ] T-A4-13 无 deleted_at 422 PASS
- [ ] T-A4-14 X-Internal-Token 错 401 PASS

## 🔗 依赖
- Blocked by: T-B22-5
- Blocks: T-B22-9
- Master: B22
```

---

## §10 T-B22-9 — OpenAPI 同步 + metric + 文档

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `drill`, `privacy`, `api`, `observability`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
OpenAPI 字段对齐 + metric 暴露 + 文档注释。

## 📋 实施步骤
1. 手动编辑 `api/openapi-v1.yaml`:
   - `/drill/round` + `/drill/judge`
   - `/account/data`(DELETE)+ `/account/export`(POST)
   - `/internal/v1/support/undelete-user`
2. metric 暴露:
   - `refine_timeout_total` / `refine_parse_error_total`
   - `drill_state_transition_total{from, to}`
   - `privacy_delete_total` / `privacy_undelete_total`
   - `tombstone_inserted_total{entity_type}`
3. handler 注释「Per 48 §1.5 / §1.1.6 / §1.1.7 / §2.8」
4. 集成测试 E2E:创建素材 → 闪测 → 删除 → 撤销 → 再查询

## ✅ 验收
- [ ] OpenAPI commit + `make api-lint` PASS
- [ ] 4 类 metric 在 `/metrics` 出现
- [ ] E2E 集成测试 PASS

## 🔗 依赖
- Blocked by: T-B22-4, T-B22-7, T-B22-8
- Blocks: 无(Master 收口)
- Master: B22
```

---

## §11 执行顺序与总工时

```
0012 迁移就绪后:
T-B22-0 (0.2d) ──▶ T-B22-5 (0.4d) ──┬──▶ T-B22-6 (0.2d) ──▶ T-B22-7 (0.1d) ──┐
                                     └──▶ T-B22-8 (0.3d) ─────────────────────┤
                                                                              │
(并行) T-B22-1 (0.3d) ──┐                                                  │
(并行) T-B22-2 (0.3d) ──┴──▶ T-B22-3 (0.3d) ──▶ T-B22-4 (0.2d) ──────────┼──▶ T-B22-9 (0.2d)
                                                                              │
                                                                              ▼
                                                                          B22 CLOSED
```

**总工时**:2.5 dev-day
**推荐 Owner**:后端隐私组(E 同学,需 1 人全力)
**关键路径**:T-B22-2 阻塞 B16 CLOSED
