# Skill B19 — B7 命中信号注入 LLM 上下文(新建 master)

> **Master**: 待新建 `B19: B7 命中信号 → LLM 上下文注入`
> **Sub-tickets**: 4 (T-HIT-1..4)
> **总工时**: 0.9 dev-day(原 B19 估 1d)
> **阻塞**: 迁移 0009 + 0010 已就绪 ✅
> **关联**: B16 AIOrchestrator(下游消费 RecentHits)

---

## §0 Master Issue Body(整段可粘贴)

**Title**: `B19: B7 命中信号 → LLM 上下文注入`  
**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `corpus`, `b7`  
**Milestone**: `V2.0 W3`

```markdown
## 🎯 目标
落地 B7 完整数据通路:voicegateway 上报命中 → app-server 写 phrase_block_uses ledger(迁移 0010)→ 同步聚合 phrase_blocks.total_uses / last_used_at(迁移 0009)→ 提供 recent-hits 接口给 LLM 注入器构造 Prompt。

本 Issue **只实现数据通路**,不实现 LLM 注入逻辑(LLM Prompt 构造在 B16 AIOrchestrator)。

## 🚧 阻塞条件
- 0009 迁移已就绪 ✅(49_ §2.2)
- 0010 迁移已就绪 ✅(49_ §2.3)
- staging MySQL 已 apply 0009 + 0010

## 📐 Sub-tickets

| # | Ticket | 工时 | 阻塞 |
|---|---|---|---|
| T-HIT-1 | 路由 + 鉴权中间件 + 路由挂载 | 0.2d | 无 |
| T-HIT-2 | HitsService.RecordHits UPSERT 事务 | 0.3d | T-HIT-1 |
| T-HIT-3 | HitsService.RecentHits 联表 + ttl_at_ms | 0.2d | T-HIT-2 |
| T-HIT-4 | 100 QPS 压测脚本 + OpenAPI 同步 | 0.2d | T-HIT-3 |

## ✅ Master 验收(57_ DoD)
- [ ] 4 个 sub-ticket 全部完成并 merge
- [ ] `go test ./internal/corpus/...` 10 个测试通过(T-B7-1..10)
- [ ] 端到端冒烟:voicegateway → app-server → MySQL 写 1 条成功
- [ ] `48_` §2.6/§2.7 字段对齐(hits 结构 + ttl_at_ms)
- [ ] 性能压测:100 QPS sustained 30s, p99 < 50ms
- [ ] OpenAPI 同步:手动编辑 `api/openapi-v1.yaml` 加 `/voicegateway/hits` + `/sessions/{id}/recent-hits`

## 🔗 关联
- 上游:无(独立启动)
- 下游:B16 AIOrchestrator(消费 RecentHits 构造 LLM Prompt,本 Issue 仅留 TODO 占位)
- 启动包:`docs/40_研发流程与协作/57_B19_B7_命中注入_Issue_Draft_2026-09-06.md`
```

---

## §1 T-HIT-1 — 路由 + 鉴权中间件 + 路由挂载

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `corpus`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
新建 `internal/corpus/internal_http_hits.go`,挂载 B7 内部路由 + 鉴权中间件。

## 📋 实施步骤
1. 新建 `internal/corpus/internal_http_hits.go`:
   ```go
   func RegisterHitsInternalRoutes(rg gin.IRouter, h *Handler, expectedToken string) {
       mw := requireInternalToken(expectedToken)
       rg.POST("/voicegateway/hits", mw, h.PostVoiceGatewayHits)
       rg.GET("/sessions/:session_id/recent-hits", mw, h.GetRecentHits)
   }
   ```
2. 在 `internal/corpus/http.go` 的 `RegisterRoutes` 旁加 `RegisterHitsInternalRoutes`
3. 在 `cmd/app-server/main.go` 调用挂载
4. 鉴权中间件 `requireInternalToken`:校验 `X-Internal-Token` header

## ✅ 验收
- [ ] 路由注册成功,启动 app-server 后 `/voicegateway/hits` 可访问
- [ ] 无 token → 401,错误 token → 401,正确 token → 200
- [ ] 单测 `TestRequireInternalToken_Valid / Invalid / Missing` PASS

## 🔗 依赖
- Blocked by: 无(0009+0010 已就绪)
- Blocks: T-HIT-2
- Master: B19
```

---

## §2 T-HIT-2 — `HitsService.RecordHits` UPSERT 事务

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `corpus`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `HitsService.RecordHits`,单事务 INSERT ledger + UPDATE total_uses(使用 UPSERT 避免 100 并发偏差)。

## 📋 实施步骤
1. 新建 `internal/corpus/hits_service.go`:
   - struct `Hit{BlockID, TurnID, DetectedAtMs}`
   - `RecordHits(ctx, userID, sessionID, turnID, hits) (int, error)`
   - SQL(单事务):
     ```sql
     INSERT INTO phrase_block_uses (id, user_id, session_id, turn_id, block_id, used_at_ms)
     VALUES (?, ?, ?, ?, ?, ?)
     ON DUPLICATE KEY UPDATE used_at_ms = VALUES(used_at_ms);
     
     UPDATE phrase_blocks
     SET total_uses = total_uses + 1, last_used_at = ?
     WHERE id = ? AND user_id = ? AND deleted_at IS NULL;
     ```
2. service 层单元测试 + 100 并发压测(T-B7-8 偏差 ≤ 1%)
3. Handler `PostVoiceGatewayHits`:接收请求 + 调 service + 返回 `{recorded_count: int}`

## ✅ 验收
- [ ] T-B7-1 单 turn 命中转发 PASS
- [ ] T-B7-2 跨 turn 累计 PASS
- [ ] T-B7-8 100 并发上报偏差 ≤ 1% PASS
- [ ] UPSERT 验证:故意重复发同一 hit,total_uses 只 +1

## 🔗 依赖
- Blocked by: T-HIT-1
- Blocks: T-HIT-3
- Master: B19
```

---

## §3 T-HIT-3 — `HitsService.RecentHits` 联表 + `ttl_at_ms`

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `corpus`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `HitsService.RecentHits`,拉最近 N 个 turn 的命中 + 联表取 intent_zh / chunk_en + 计算 ttl_at_ms。

## 📋 实施步骤
1. `RecentHits(ctx, sessionID, lookbackTurns=8, minScore=0.7)`:
   ```sql
   SELECT pbu.block_id, pbu.turn_id, pbu.used_at_ms,
          pb.intent_zh, pb.chunk_en
   FROM phrase_block_uses pbu
   LEFT JOIN phrase_blocks pb ON pb.id = pbu.block_id
   WHERE pbu.session_id = ?
     AND pb.score >= ?
   ORDER BY pbu.used_at_ms DESC
   LIMIT ?;
   ```
2. `ttl_at_ms = 最新 hit.used_at_ms + 60000`
3. Handler `GetRecentHits`:接收 query 参数 + 调 service + 返回 `{hits, ttl_at_ms}`

## ✅ 验收
- [ ] T-B7-3 recent-hits 拉取 PASS
- [ ] T-B7-4 ttl_at_ms 计算 PASS
- [ ] T-B7-9 lookback_turns 边界(0/1)PASS
- [ ] `deleted_at IS NOT NULL` 的 block 不出现在结果(避免 tombstone)

## 🔗 依赖
- Blocked by: T-HIT-2
- Blocks: T-HIT-4
- Master: B19
```

---

## §4 T-HIT-4 — 100 QPS 压测脚本 + OpenAPI 同步

**Labels**: `backend`, `v2.0-blocker`, `priority: P0`, `corpus`, `observability`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
100 QPS sustained 30s 压测脚本 + OpenAPI 同步。

## 📋 实施步骤
1. 新建 `scripts/loadtest-b7-hits.sh`:
   - 用 `hey` 或 `vegeta` 跑 100 QPS × 30s
   - 断言 p99 < 50ms
2. OpenAPI 同步:手动编辑 `api/openapi-v1.yaml`:
   - 加 `/voicegateway/hits` 路径(POST)
   - 加 `/sessions/{session_id}/recent-hits` 路径(GET)
   - schema 对齐 48_ §2.6/§2.7
3. T-B7-5/6 token 鉴权集成测试(补到 T-HIT-1 的单测或这里)

## ✅ 验收
- [ ] 100 QPS × 30s 压测通过,p99 < 50ms
- [ ] OpenAPI commit + `make api-lint` PASS
- [ ] token 鉴权 401 测试 PASS

## 🔗 依赖
- Blocked by: T-HIT-3
- Blocks: 无(Master 收口)
- Master: B19
```

---

## §5 执行顺序与总工时

```
T-HIT-1 (0.2d) ──▶ T-HIT-2 (0.3d) ──▶ T-HIT-3 (0.2d) ──▶ T-HIT-4 (0.2d)
                                                        │
                                                        ▼
                                                    B19 CLOSED
```

**总工时**:0.9 dev-day
**推荐 Owner**:后端语料组(C 同学)
**启动建议**:W3 Day 1 启动,可与 B25 并行(无交叉)
