# Skill B25 — F3 收藏置顶后端(PATCH 拆分)(新建 master)

> **Master**: 待新建 `B25: F3 收藏置顶后端(PATCH 拆分)`
> **Sub-tickets**: 4 (T-PIN-1..4)
> **总工时**: 0.8 dev-day(原 B25 估 1d)
> **阻塞**: D-API-1 已拍板 ✅, phrase_blocks 表已有 `is_favorite` + `pinned_at`(迁移 0006)
> **关联**: iOS I22 F3 UI 实施(下游)

---

## §0 Master Issue Body(整段可粘贴)

**Title**: `B25: F3 收藏置顶后端(PATCH 拆分)`  
**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `corpus`, `api-change`  
**Milestone**: `V2.0 W3`

```markdown
## 🎯 目标
按 D-API-1 拍板方案 A 拆分 live `POST /api/v1/corpus/blocks/:id/favorite` 为两个 PATCH 端点:
- `PATCH /api/v1/corpus/blocks/:id/pin`(body: `{pinned: bool}`)
- `PATCH /api/v1/corpus/blocks/:id/favorite`(body: `{favorite: bool}`)

旧 POST 路由**保留为 deprecated 1 个版本**(V1.4),写 metrics 跟踪调用量;V1.5 移除。

## 🚧 阻塞条件
- D-API-1 已 ✅ 拍板(47_ §五 2026-09-06)
- live `phrase_blocks` 表已有 `is_favorite` + `pinned_at` 字段(迁移 0006)—— **无需新迁移**

## 📐 Sub-tickets

| # | Ticket | 工时 | 阻塞 |
|---|---|---|---|
| T-PIN-1 | 路由注册 + handler 骨架 | 0.2d | 无 |
| T-PIN-2 | PatchPin + PatchFavorite handler + service | 0.3d | T-PIN-1 |
| T-PIN-3 | PostFavorite deprecated + metrics | 0.1d | T-PIN-1 |
| T-PIN-4 | GetBlocks 服务端排序 + OpenAPI | 0.2d | T-PIN-2 |

## ✅ Master 验收(59_ DoD)
- [ ] 4 个 sub-ticket 全部完成并 merge
- [ ] 3 个 handler:PatchPin / PatchFavorite / PostFavorite(deprecated)
- [ ] `go test ./internal/corpus/...` 10 个测试通过(T-F3-1..10)
- [ ] metric `corpus_deprecated_post_favorite_total` 上线
- [ ] OpenAPI 同步:PATCH 路径 + 排序规则说明 + POST deprecated 标注

## 🔗 关联
- 上游:无(D-API-1 已 ✅ 拍板)
- 下游:iOS I22 F3 UI 实施(46 F3.5 估时 0.5 dev-day;等本 Issue 落地后启动)
- 启动包:`docs/40_研发流程与协作/59_B25_F3_收藏置顶_Issue_Draft_2026-09-06.md`
```

---

## §1 T-PIN-1 — 路由注册 + handler 骨架

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `corpus`, `api-change`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
在 `internal/corpus/http.go` 的 `RegisterRoutes` 加 PATCH + 保留 deprecated POST。

## 📋 实施步骤
1. 修改 `internal/corpus/http.go`:
   ```go
   rg.PATCH("/corpus/blocks/:id/pin", h.accounts.RequireAuth(), h.PatchPin)
   rg.PATCH("/corpus/blocks/:id/favorite", h.accounts.RequireAuth(), h.PatchFavorite)
   rg.POST("/corpus/blocks/:id/favorite", h.accounts.RequireAuth(), h.PostFavorite)
   ```
2. 在 `internal/corpus/http.go` 加 3 个 handler 函数签名(stub 实现,具体逻辑下个 ticket)
3. 编译通过

## ✅ 验收
- [ ] `make build` 通过
- [ ] 路由列表包含 PATCH /pin + /favorite + POST /favorite
- [ ] 单测 `TestCorpus_Routes_Registered` PASS

## 🔗 依赖
- Blocked by: 无
- Blocks: T-PIN-2, T-PIN-3
- Master: B25
```

---

## §2 T-PIN-2 — `PatchPin` + `PatchFavorite` handler + service

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `corpus`, `api-change`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 PatchPin / PatchFavorite handler + service 方法(UpdatePin / UpdateFavorite)。

## 📋 实施步骤
1. 在 `internal/corpus/service.go` 加:
   ```go
   func (s *Service) UpdatePin(ctx context.Context, blockID, userID string, pinnedAt sql.NullTime) error
   func (s *Service) UpdateFavorite(ctx context.Context, blockID, userID string, favorite bool) error
   ```
2. `PatchPin` handler(参考 59_ §2.2):JSON 解析 + pinnedAt 转换 + 调 service + 返回
3. `PatchFavorite` handler 类似
4. 单测 T-F3-1..6 + T-F3-9

## ✅ 验收
- [ ] T-F3-1 PATCH /pin 3 张卡 PASS
- [ ] T-F3-3 取消置顶 PASS
- [ ] T-F3-6 跨用户权限 403 PASS
- [ ] T-F3-9 body 缺 pinned 字段 → 400 PASS
- [ ] Service 单测 `TestService_UpdatePin_Owner / CrossUser` PASS

## 🔗 依赖
- Blocked by: T-PIN-1
- Blocks: T-PIN-4
- Master: B25
```

---

## §3 T-PIN-3 — `PostFavorite` deprecated + metrics

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `corpus`, `observability`, `deprecation`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
PostFavorite handler 加 deprecation warning + metric 跟踪。

## 📋 实施步骤
1. 在 `internal/corpus/http.go` 的 `PostFavorite` 顶部加:
   ```go
   metrics.IncDeprecatedCall("corpus.PostFavorite", c.GetHeader("User-Agent"))
   c.Header("Deprecation", "true")
   c.Header("Sunset", "Wed, 01 Oct 2025 00:00:00 GMT") // V1.5 移除日
   c.Header("Link", `</api/v1/corpus/blocks/:id/pin>; rel="successor-version"`)
   ```
2. Prometheus metric:`corpus_deprecated_post_favorite_total{user_agent="..."}`
3. 单测 `TestPostFavorite_DeprecationHeaders` + `TestPostFavorite_Metric` PASS

## ✅ 验收
- [ ] T-F3-7 POST deprecated 仍可用 + 写 metric PASS
- [ ] Deprecation / Sunset / Link headers 出现
- [ ] metric `corpus_deprecated_post_favorite_total` 在 `/metrics` 出现

## 🔗 依赖
- Blocked by: T-PIN-1
- Blocks: 无(可与 T-PIN-4 并行)
- Master: B25
```

---

## §4 T-PIN-4 — `GetBlocks` 服务端排序 + OpenAPI

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `corpus`, `api-change`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
修改 `GetBlocks` 排序:pinned > favorite > updated_at DESC;OpenAPI 同步。

## 📋 实施步骤
1. 修改 `internal/corpus/service.go` 的 `GetBlocks` SQL:
   ```sql
   SELECT * FROM phrase_blocks
   WHERE user_id = ? AND deleted_at IS NULL
     [AND pinned_at IS NOT NULL] [AND is_favorite = ?]
   ORDER BY
     CASE WHEN pinned_at IS NOT NULL THEN 0 ELSE 1 END ASC,
     CASE WHEN is_favorite = 1 THEN 0 ELSE 1 END ASC,
     updated_at DESC
   LIMIT ? OFFSET ?
   ```
2. OpenAPI 同步:手动编辑 `api/openapi-v1.yaml`:
   - 加 PATCH /pin + /favorite 完整 schema
   - POST /favorite 加 `deprecated: true` + `description: ⚠️ Deprecated, V1.5 移除`
   - 在 `description` 字段加排序规则说明
3. 单测 T-F3-2 + T-F3-4 + T-F3-5 + T-F3-8

## ✅ 验收
- [ ] T-F3-2 置顶 + 收藏叠加 PASS
- [ ] T-F3-4 筛选 pinned PASS
- [ ] T-F3-5 cursor 分页不打断置顶 PASS
- [ ] T-F3-8 PATCH 与 POST 互斥幂等 PASS
- [ ] OpenAPI commit + `make api-lint` PASS

## 🔗 依赖
- Blocked by: T-PIN-2
- Blocks: 无(Master 收口)
- Master: B25
```

---

## §5 执行顺序与总工时

```
T-PIN-1 (0.2d) ──┬──▶ T-PIN-2 (0.3d) ──▶ T-PIN-4 (0.2d)
                  │                                │
                  └──▶ T-PIN-3 (0.1d) ────────────┤
                                                   ▼
                                               B25 CLOSED
```

**总工时**:0.8 dev-day
**推荐 Owner**:后端语料组(D 同学)
**启动建议**:W3 Day 1 启动,可与 B19 并行(无交叉)
