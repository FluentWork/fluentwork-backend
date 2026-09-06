# Skill B24 — 历史回顾 API(新建 master)

> **Master**: 待新建 `B24: 历史回顾 API`
> **Sub-tickets**: 4 (T-B24-1..4)
> **总工时**: 0.5 dev-day(工作量最小,4 个 ticket 都是 atomic)
> **阻塞**: B22 A4 CLOSED(过滤 soft delete), B18 CLOSED(review 联动)
> **关联**: I19 iOS 历史回顾列表(下游)

---

## §0 Master Issue Body(整段可粘贴)

**Title**: `B24: 历史回顾 API(含会话列表)`  
**Labels**: `backend`, `v2.0-blocker`, `priority: P2`, `sessions`, `api`  
**Milestone**: `V2.0 W3-W4`

```markdown
## 🎯 目标
实施 PRD §C4 历史回顾 API:会话列表 + 单会话详情(基础版):
- `GET /api/v1/sessions` 列表(cursor 分页,默认 size=20,按 started_at DESC)
- `GET /api/v1/sessions/:session_id` 单会话详情(含 utterances + materials 关联)

> **简化范围**:本期不含筛选(since / until / material_id);V1.5 加 query params

## 🚧 阻塞条件
- practice_sessions + utterances + materials 3 张表 schema 已就绪(含 soft delete 字段)
- B22 A4 软删除已 CLOSED(list API 过滤 deleted_at IS NULL)
- B18 review eval 已 CLOSED(GetDetail 嵌入 review 字段)

## 📐 Sub-tickets

| # | Ticket | 工时 | 阻塞 |
|---|---|---|---|
| T-B24-1 | 数据模型 + Cursor 编解码 | 0.1d | 无 |
| T-B24-2 | Service.List + Cursor 分页 | 0.2d | T-B24-1 |
| T-B24-3 | Service.GetDetail + B18/B22 联动 | 0.1d | T-B24-2, B18 CLOSED, B22 CLOSED |
| T-B24-4 | HTTP 路由 + OpenAPI 同步 | 0.1d | T-B24-3 |

## ✅ Master 验收(72_ DoD)
- [ ] 4 个 sub-ticket 全部完成并 merge
- [ ] `internal/session_history/` 整个包实施
- [ ] `go test ./internal/session_history/...` 16 个测试通过
- [ ] 端到端冒烟:本地真实 session 走通 list + detail
- [ ] 性能:50 句 session P95 ≤ 200ms
- [ ] OpenAPI 同步:`GET /sessions` + `GET /sessions/{id}`
- [ ] A4 软删除 + 撤销删除兼容
- [ ] B18 review 联动

## 🔗 关联
- 关联:B18 review eval、B22 A4 软删除
- 下游:I19 iOS 历史回顾列表
- 启动包:`docs/40_研发流程与协作/72_B24_历史回顾API_Issue_Draft_2026-09-06.md`
```

---

## §1 T-B24-1 — 数据模型 + Cursor 编解码

**Labels**: `backend`, `v2.0-blocker`, `priority: P2`, `sessions`, `api`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
定义 SessionListItem / SessionListPage / SessionDetail / Cursor 数据模型 + Cursor base64 编解码。

## 📋 实施步骤
1. 新建 `internal/session_history/model.go`:
   - 5 个结构体(参考 72_ §2)
2. 新建 `internal/session_history/cursor.go`:
   - `Cursor{StartedAt, ID}` JSON struct
   - `encodeCursor(c) string`:base64.URLEncoding.EncodeToString(json.Marshal(c))
   - `decodeCursor(s) (*Cursor, error)`:反向
3. 单测 `TestCursor_EncodeDecode` + `TestCursor_InvalidInput` PASS

## ✅ 验收
- [ ] 5 个结构体 + 2 个函数编译通过
- [ ] cursor 编解码 round-trip 一致
- [ ] 非法 cursor → error

## 🔗 依赖
- Blocked by: 无
- Blocks: T-B24-2
- Master: B24
```

---

## §2 T-B24-2 — `Service.List` + Cursor 分页

**Labels**: `backend`, `v2.0-blocker`, `priority: P2`, `sessions`, `api`, `db`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `Service.List` 会话列表 + cursor 分页逻辑。

## 📋 实施步骤
1. 新建 `internal/session_history/service.go`:
   - `Service{store, logger}`
   - `List(ctx, userID, cursor, size)`:
     - size 校验(默认 20,最大 100)
     - 解码 cursor
     - `store.ListSessions(ctx, userID, lastStartedAt, lastID, size+1)`
     - 截断到 size + 生成 next_cursor
2. Store 接口:`ListSessions` SQL 按 started_at DESC + ID DESC,过滤 deleted_at IS NULL
3. 单测 T-B24-1..6/16

## ✅ 验收
- [ ] T-B24-1 列出 20 条 + cursor PASS
- [ ] T-B24-2 翻页 PASS
- [ ] T-B24-3 最后一页不足 PASS
- [ ] T-B24-4 空列表 PASS
- [ ] T-B24-5 cursor 篡改 400 PASS
- [ ] T-B24-6 size=200 → 强制 20 PASS
- [ ] T-B24-16 cursor 稳定性(同时间戳按 ID 字典序)PASS

## 🔗 依赖
- Blocked by: T-B24-1
- Blocks: T-B24-3
- Master: B24
```

---

## §3 T-B24-3 — `Service.GetDetail` + B18/B22 联动

**Labels**: `backend`, `v2.0-blocker`, `priority: P2`, `sessions`, `api`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `Service.GetDetail` 单会话详情 + review 联动(B18)+ soft delete 过滤(B22)。

## 📋 实施步骤
1. 扩展 `internal/session_history/service.go`:
   - `GetDetail(ctx, userID, sessionID)`:
     - 校验 session 归属 + 未删除(B22 联动)
     - 拉 materials + utterances
     - 关联 review:`reviewStore.GetReview(ctx, sessionID)`(B18 联动,可能 nil)
     - 返回 SessionDetail
2. 单测 T-B24-7/8/9/10/11/12/13/14

## ✅ 验收
- [ ] T-B24-7 完整详情 PASS
- [ ] T-B24-8 跨用户 403 PASS
- [ ] T-B24-9 A4 删除后 404 PASS
- [ ] T-B24-10 A4 撤销后 200 PASS
- [ ] T-B24-11 空 session 200 + utterances=[] PASS
- [ ] T-B24-12 review pending → review=nil PASS
- [ ] T-B24-13 review failed → review={status:"failed", score:0} PASS
- [ ] T-B24-14 review ready → review={score, dims, suggestions} PASS

## 🔗 依赖
- Blocked by: T-B24-2, B18 CLOSED(review 联动), B22 CLOSED(soft delete)
- Blocks: T-B24-4
- Master: B24
```

---

## §4 T-B24-4 — HTTP 路由 + OpenAPI 同步

**Labels**: `backend`, `v2.0-blocker`, `priority: P2`, `sessions`, `api`, `observability`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 HTTP Handler + OpenAPI 同步 + 性能验证。

## 📋 实施步骤
1. 新建 `internal/session_history/http.go`:
   - `RegisterRoutes(rg, h)`:`GET /sessions` + `GET /sessions/:session_id`
   - `ListSessions(c)`:读 `cursor` + `size` query → 调 service
   - `GetSessionDetail(c)`:调 service → 200 / 403 / 404
2. 手动编辑 `api/openapi-v1.yaml`:
   - `/sessions` (GET, cursor + size query)
   - `/sessions/{session_id}` (GET)
3. handler 注释「Per 48 §1.3.2」
4. T-B24-15 性能测试(50 句 session P95 ≤ 200ms)

## ✅ 验收
- [ ] GET list / detail 路径正确
- [ ] OpenAPI commit + `make api-lint` PASS
- [ ] 50 句 session P95 ≤ 200ms

## 🔗 依赖
- Blocked by: T-B24-3
- Blocks: 无(Master 收口)
- Master: B24
```

---

## §5 执行顺序与总工时

```
T-B24-1 (0.1d) ──▶ T-B24-2 (0.2d) ──▶ T-B24-3 (0.1d, 依赖 B18+B22) ──▶ T-B24-4 (0.1d)
                                                                          │
                                                                          ▼
                                                                       B24 CLOSED
```

**总工时**:0.5 dev-day
**推荐 Owner**:后端 AI 组(G 同学)与 B18 同步实施
**关键路径**:T-B24-3 阻塞 B18 + B22 CLOSED——B24 必须是 B18/B22 完成后的最后一步
