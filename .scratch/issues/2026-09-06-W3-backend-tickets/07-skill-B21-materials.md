# Skill B21 — 素材模块(A1/A2 后端)(新建 master)

> **Master**: 待新建 `B21: 素材模块(A1/A2 后端)`
> **Sub-tickets**: 7 (T-B21-1..7)
> **总工时**: 1.5 dev-day(与 70_ 启动包一致)
> **阻塞**: phrase_blocks 表 ✅, B16 AIOrchestrator CLOSED
> **关联**: I14 iOS 创建练习弹层(下游)

---

## §0 Master Issue Body(整段可粘贴)

**Title**: `B21: 素材模块(A1/A2 后端)`  
**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `materials`, `llm`  
**Milestone**: `V2.0 W3-W4`

```markdown
## 🎯 目标
实施 PRD §A1/A2 素材模块:
- 用户粘贴英文文本 / 一句话 / URL → 创建 `material`
- 后端异步提炼 → 转 `phrase_blocks`(写入语料库)
- 客户端轮询 GET /materials/:id 直到 `refine_status=ready` 或 `failed`

> **范围**:仅素材创建 + 提炼流水线入口;phrase_blocks 完整 CRUD 走 B25 收藏置顶 / F3 已 live 路径

## 🚧 阻塞条件
- phrase_blocks 表已就绪 ✅(live schema,含 `intent_zh` / `chunk_en` / `scene_tag` / `function_tag`)
- B16 AIOrchestrator 已 CLOSED(提供 `LLMClient`)
- C-1 凭证:Ark Mini API Key(W3 前到位)

## 📐 Sub-tickets

| # | Ticket | 工时 | 阻塞 |
|---|---|---|---|
| T-B21-1 | 数据模型 + Store 接口 | 0.2d | 无 |
| T-B21-2 | Service.Create + URL fetcher stub | 0.2d | T-B21-1 |
| T-B21-3 | Refiner.LLM 调用 + JSON 解析 | 0.3d | T-B21-2, B16 CLOSED |
| T-B21-4 | Refiner.PhraseBlockStore 批量写入 | 0.3d | T-B21-3 |
| T-B21-5 | Refiner 状态机 queued→processing→ready/failed | 0.2d | T-B21-4 |
| T-B21-6 | HTTP 路由 + Handler | 0.1d | T-B21-2 |
| T-B21-7 | OpenAPI 同步 + 软删除 + metric | 0.2d | T-B21-5, T-B21-6 |

## ✅ Master 验收(70_ DoD)
- [ ] 7 个 sub-ticket 全部完成并 merge
- [ ] `internal/materials/` 整个包实施
- [ ] `go test ./internal/materials/...` 15 个测试通过
- [ ] 端到端冒烟:粘贴文本 → 提炼 → 5 个 phrase_blocks 落库
- [ ] 性能:500 字符文本提炼 P95 ≤ 35s
- [ ] OpenAPI 同步:`POST /materials` + `GET /materials/{id}`
- [ ] 软删除 + 撤销删除联动(B22 A4 兼容)

## 🔗 关联
- 上游依赖:B16 AIOrchestrator(LLMClient)
- 下游:I14 iOS 创建练习弹层
- 启动包:`docs/40_研发流程与协作/70_B21_素材模块_Issue_Draft_2026-09-06.md`
```

---

## §1 T-B21-1 — 数据模型 + Store 接口

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `materials`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
定义 Material / RefineResult 数据模型 + Store 接口。

## 📋 实施步骤
1. 新建 `internal/materials/model.go`:
   - `Material` / `RefineResult` / `RefinedBlock` 结构体(参考 70_ §2)
   - 字段:`kind` / `content` / `refine_status` / `deleted_at`
2. 新建 `internal/materials/store.go`:
   - `InsertMaterial(ctx, userID, kind, content) (materialID, error)`
   - `GetMaterial(ctx, userID, materialID) (*Material, error)`
   - `MarkRefined(ctx, materialID, blockCount) error`
   - `MarkRefineFailed(ctx, materialID, errorCode) error`
3. 单测 `TestStore_Insert / Get / MarkRefined / MarkRefineFailed` PASS

## ✅ 验收
- [ ] 3 个结构体编译通过
- [ ] Store 接口 sqlmock 覆盖
- [ ] 4 个单测 PASS

## 🔗 依赖
- Blocked by: 无
- Blocks: T-B21-2
- Master: B21
```

---

## §2 T-B21-2 — `Service.Create` + URL fetcher stub

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `materials`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `Service.Create`,URL 类型 stub,落库 + 入队。

## 📋 实施步骤
1. 新建 `internal/materials/service.go`:
   - `Service{store, refiner, job, logger}`
   - `Create(ctx, userID, kind, content)`:
     - URL 类型:`content = "[URL content placeholder]"`(stub,V1.5 B16 接入)
     - 长度校验:`len(content) > 5000` → BadRequest
     - `InsertMaterial` + `Enqueue RefineJob`
2. 单测 T-B21-1..5 + T-B21-13 幂等性

## ✅ 验收
- [ ] T-B21-1 粘贴 200 词 → 202 PASS
- [ ] T-B21-2 一句话 → 202 PASS
- [ ] T-B21-3 URL → 202 PASS
- [ ] T-B21-4 空内容 → 400 PASS
- [ ] T-B21-5 超长内容 → 400 PASS
- [ ] T-B21-13 同 material_id 二次入队幂等 PASS

## 🔗 依赖
- Blocked by: T-B21-1
- Blocks: T-B21-3, T-B21-6
- Master: B21
```

---

## §3 T-B21-3 — Refiner LLM 调用 + JSON 解析

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `materials`, `llm`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `Refiner.Refine` 第一步:LLM 调用 + Prompt + JSON 解析。

## 📋 实施步骤
1. 新建 `internal/materials/refiner_prompt.go`:提炼 Prompt 模板(参考 70_ §5)
2. 新建 `internal/materials/refiner.go`:
   - `Refiner{llm, blocks, logger}`
   - `Refine(ctx, materialID)`:
     - 拉 material + 构造 Prompt
     - llm.Complete(30s timeout)
     - 超时 → `markFailed(ctx, materialID, "llm_timeout")`
     - JSON 解析失败 → `markFailed(ctx, materialID, "parse_error")`
     - metric:`refine_timeout_total` + `refine_parse_error_total`
3. 单测 T-B21-7/8

## ✅ 验收
- [ ] T-B21-7 LLM 超时 → status=failed PASS
- [ ] T-B21-8 LLM 解析失败 → status=failed PASS
- [ ] metric 暴露

## 🔗 依赖
- Blocked by: T-B21-2, B16 CLOSED
- Blocks: T-B21-4
- Master: B21
```

---

## §4 T-B21-4 — Refiner.PhraseBlockStore 批量写入

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `materials`, `corpus`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `Refiner` 把 LLM 输出写入 `phrase_blocks` 表。

## 📋 实施步骤
1. 扩展 `internal/materials/refiner.go`:
   - 解析后调 `blocks.InsertBulk(ctx, materialID, userID, blocks)`
   - 写入失败 → `markFailed(ctx, materialID, "db_error")`
2. 单测 `TestRefiner_InsertBulk_Success / Fail` PASS

## ✅ 验收
- [ ] 提炼 5 个 block → phrase_blocks 表 +5 行
- [ ] 单 transaction 包裹(失败回滚)
- [ ] 单测 PASS

## 🔗 依赖
- Blocked by: T-B21-3
- Blocks: T-B21-5
- Master: B21
```

---

## §5 T-B21-5 — Refiner 状态机 queued→processing→ready/failed

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `materials`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
完善 Refiner 状态转换 + 边界条件处理。

## 📋 实施步骤
1. Refiner 完整状态机:
   - `processing`:Refiner 入口设置
   - `ready`:InsertBulk 成功后调 MarkRefined
   - `failed`:任何 markFailed 调用
2. T-B21-12 提炼 0 个块:`markReady(blockCount=0, error="no_chunks_extracted")`
3. T-B21-13 二次入队幂等:检查 material.refine_status != queued 时跳过
4. 状态转换 metric:`refine_status_transition_total{from, to}`

## ✅ 验收
- [ ] T-B21-6 提炼成功 → status=ready PASS
- [ ] T-B21-12 0 个块 → status=ready + error PASS
- [ ] 状态机非法转换(如 ready→processing)被拒绝

## 🔗 依赖
- Blocked by: T-B21-4
- Blocks: T-B21-7
- Master: B21
```

---

## §6 T-B21-6 — HTTP 路由 + Handler

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `materials`, `api`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `POST /api/v1/materials` + `GET /api/v1/materials/:id`。

## 📋 实施步骤
1. 新建 `internal/materials/http.go`:
   - `RegisterRoutes(rg, h)`:`POST /materials` + `GET /materials/:id`
   - `PostMaterial(c)`:解析 `{kind, content}` → 调 service.Create → 202 + `{material_id, refine_status: "queued"}`
   - `GetMaterial(c)`:拉 material → 200 / 404
2. 单测 T-B21-9..11(权限 + 软删除)

## ✅ 验收
- [ ] T-B21-9 跨用户 403 PASS
- [ ] T-B21-10 A4 删除后 404 PASS
- [ ] T-B21-11 A4 撤销后 200 PASS

## 🔗 依赖
- Blocked by: T-B21-2
- Blocks: T-B21-7
- Master: B21
```

---

## §7 T-B21-7 — OpenAPI 同步 + 软删除 + metric

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `materials`, `api`, `observability`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
OpenAPI 字段对齐 + service 软删除过滤 + metric 暴露。

## 📋 实施步骤
1. 手动编辑 `api/openapi-v1.yaml` 加 `POST /materials` + `GET /materials/{id}` 完整 schema
2. service 层查询时过滤 `deleted_at IS NULL`
3. metric:`refine_status_transition_total` 暴露
4. T-B21-14 性能测试(500 字符 P95 ≤ 35s)
5. T-B21-15 OpenAPI 字段对齐

## ✅ 验收
- [ ] OpenAPI commit + `make api-lint` PASS
- [ ] 软删除兼容(B22 A4 联动)
- [ ] 性能 P95 ≤ 35s

## 🔗 依赖
- Blocked by: T-B21-5, T-B21-6, B22 A4 CLOSED
- Blocks: 无(Master 收口)
- Master: B21
```

---

## §8 执行顺序与总工时

```
T-B21-1 (0.2d) ──▶ T-B21-2 (0.2d) ──┬──▶ T-B21-3 (0.3d) ──▶ T-B21-4 (0.3d) ──▶ T-B21-5 (0.2d) ──┐
                                     │                                                              ├──▶ T-B21-7 (0.2d)
                                     └──▶ T-B21-6 (0.1d) ─────────────────────────────────────────┘
```

**总工时**:1.5 dev-day
**推荐 Owner**:后端 AI 组(F 同学)
**关键路径**:T-B21-3 阻塞 B16 CLOSED;T-B21-7 阻塞 B22 A4 CLOSED
