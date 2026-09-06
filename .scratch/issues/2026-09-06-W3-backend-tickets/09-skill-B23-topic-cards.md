# Skill B23 — 话题卡生成(每日调度)(新建 master)

> **Master**: 待新建 `B23: 话题卡生成(每日调度)`
> **Sub-tickets**: 6 (T-B23-1..6)
> **总工时**: 1.0 dev-day(与 71_ 启动包一致)
> **阻塞**: D-5 ✅, topic_cards 表 ✅, B16 AIOrchestrator CLOSED
> **关联**: I18 iOS 话题卡 UI(下游)

---

## §0 Master Issue Body(整段可粘贴)

**Title**: `B23: 话题卡生成(每日调度)`  
**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `topic`, `llm`, `scheduler`  
**Milestone**: `V2.0 W3-W4`

```markdown
## 🎯 目标
实施 PRD §H1/H2/H3 话题卡每日生成 + 列表 API:
- **每日 04:00 UTC 批处理**:为活跃用户(最近 30 天有练习记录)生成 3 张话题卡
- **API**:`GET /api/v1/topic-cards` 返回今日话题卡列表;`POST /api/v1/topic-cards/:id/checkin` 打卡
- **话题卡内容**:基于用户最近练习的 scene_tag / function_tag 分布 + 整体学习水平

> **参考 D-5 每日一读批处理**:复用相同的 cron worker 模式(D-5 已拍板 04:00 UTC 触发)

## 🚧 阻塞条件
- D-5 已 ✅ 拍板(每日一读批处理 04:00 UTC)—— 复用 cron worker 模式
- topic_cards 表待创建:0007 迁移已包含,需确认 valid_until / card_type / seed_tags 字段
- B16 AIOrchestrator 已 CLOSED(提供 `LLMClient`)
- C-1 凭证:Ark Mini API Key

## 📐 Sub-tickets

| # | Ticket | 工时 | 阻塞 |
|---|---|---|---|
| T-B23-1 | 数据模型 + Store + streak 逻辑 | 0.3d | 无 |
| T-B23-2 | Generator + LLM Prompt | 0.2d | T-B23-1, B16 CLOSED |
| T-B23-3 | Service + Checkin 业务 | 0.2d | T-B23-1 |
| T-B23-4 | Scheduler + cron 集成 | 0.1d | T-B23-2 |
| T-B23-5 | HTTP 路由 + Handler | 0.1d | T-B23-3 |
| T-B23-6 | OpenAPI 同步 + metric | 0.1d | T-B23-5 |

## ✅ Master 验收(71_ DoD)
- [ ] 6 个 sub-ticket 全部完成并 merge
- [ ] `internal/topic/` 整个包实施
- [ ] `topic_cards` + `checkins` + `streaks` 3 张表 schema 已就绪
- [ ] `go test ./internal/topic/...` 17 个测试通过
- [ ] 端到端冒烟:本地 mock LLM + 真实 cron 触发走通
- [ ] 性能:1000 活跃用户批处理 P95 ≤ 10 min
- [ ] OpenAPI 同步:`GET /topic-cards` + `POST /topic-cards/{id}/checkin`
- [ ] A4 删除 + 撤销删除兼容(B22)

## 🔗 关联
- 上游依赖:B16 AIOrchestrator(LLMClient)
- 下游:I18 iOS 话题卡 UI
- 启动包:`docs/40_研发流程与协作/71_B23_话题卡生成_Issue_Draft_2026-09-06.md`
- 借鉴:D-5 每日一读批处理模式
```

---

## §1 T-B23-1 — 数据模型 + Store + streak 逻辑

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `topic`, `db`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
定义 TopicCard / CheckinResult / Streak 数据模型 + Store + streak 更新逻辑。

## 📋 实施步骤
1. 新建 `internal/topic/model.go`:
   - `TopicCard` / `CheckinResult` / `Streak` 结构体(参考 71_ §2)
2. 新建 `internal/topic/store.go`:
   - `ListTodayCards(ctx, userID, today) ([]TopicCard, error)`
   - `GetCard(ctx, cardID) (*TopicCard, error)`
   - `MarkCheckedIn(ctx, cardID, now, reflection) error`
   - `InsertCheckin(ctx, cardID, userID, reflection, now) (string, error)`
   - `InsertCards(ctx, userID, cards, validUntil) ([]TopicCard, error)`
3. 新建 `internal/topic/streak.go`:
   - `UpdateOnCheckin(ctx, userID, now) (*Streak, error)`:日界重置 + longest_streak 更新
4. 单测 T-B23-8/9 streak 重置 + 7 天连续

## ✅ 验收
- [ ] 4 个结构体 + 6 个 Store 方法编译通过
- [ ] T-B23-8 7 天连续 streak=7 PASS
- [ ] T-B23-9 中断 1 天后重置 PASS

## 🔗 依赖
- Blocked by: 无(topic_cards / checkins / streaks 表已就绪)
- Blocks: T-B23-2, T-B23-3
- Master: B23
```

---

## §2 T-B23-2 — Generator + LLM Prompt

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `topic`, `llm`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 Generator + 生成 Prompt + 解析落库。

## 📋 实施步骤
1. 新建 `internal/topic/generator_prompt.go`:生成 Prompt 模板(参考 71_ §5)
2. 新建 `internal/topic/generator.go`:
   - `Generator{llm, analytics, store, logger}`
   - `GenerateForUser(ctx, userID, targetDate)`:
     - 拉 tag distribution(30 天)+ level + recentTitles(14 天)
     - 调 llm.Complete(15s timeout, temperature=0.7)
     - 解析 + InsertCards(validUntil=targetDate+24h)
     - LLM 超时/解析失败 → 跳过该用户 + metric
3. 单测 T-B23-10/11/12/17

## ✅ 验收
- [ ] T-B23-10 LLM 超时重试 3 次后跳过 PASS
- [ ] T-B23-11 解析失败 metric PASS
- [ ] T-B23-12 标签分布空降级 PASS
- [ ] T-B23-17 限速 ≤ 10 QPS PASS

## 🔗 依赖
- Blocked by: T-B23-1, B16 CLOSED
- Blocks: T-B23-4
- Master: B23
```

---

## §3 T-B23-3 — Service + Checkin 业务

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `topic`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 Service.ListToday + Service.Checkin 业务逻辑。

## 📋 实施步骤
1. 新建 `internal/topic/service.go`:
   - `Service{store, streak, logger}`
   - `ListToday(ctx, userID)` → `[]TopicCard`
   - `Checkin(ctx, userID, cardID, reflection)`:
     - 校验 card 归属 + 未打卡
     - MarkCheckedIn + UpdateStreak + InsertCheckin
     - 返回 CheckinResult{checkin_id, streak_days}
2. 单测 T-B23-3/4/5/6/7/13/14/15

## ✅ 验收
- [ ] T-B23-3 今日 3 张卡 valid_until 明天 PASS
- [ ] T-B23-5 首次打卡 PASS
- [ ] T-B23-6 重复打卡 409 PASS
- [ ] T-B23-7 跨用户 403 PASS
- [ ] T-B23-13 reflection > 500 → 400 PASS
- [ ] T-B23-14/15 A4 删除 + 撤销 PASS

## 🔗 依赖
- Blocked by: T-B23-1
- Blocks: T-B23-5
- Master: B23
```

---

## §4 T-B23-4 — Scheduler + cron 集成

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `topic`, `scheduler`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `Scheduler.DailyTopicCardGeneration` 复用 D-5 cron worker。

## 📋 实施步骤
1. 新建 `internal/topic/scheduler.go`:
   - `Scheduler{generator, analytics, logger}`
   - `DailyTopicCardGeneration(ctx, runDate)`:
     - 拉活跃用户(30 天内 practice_session)
     - 并发生成(限速 10 QPS)
     - 跳过非活跃 + LLM 失败用户
2. 在 B16 worker 的 daily-broadcast cron 追加 TaskType=TopicCard
3. 单测 T-B23-1/2 活跃用户筛选

## ✅ 验收
- [ ] T-B23-1 100 活跃用户各生成 3 张 PASS
- [ ] T-B23-2 非活跃跳过 PASS

## 🔗 依赖
- Blocked by: T-B23-2
- Blocks: 无
- Master: B23
```

---

## §5 T-B23-5 — HTTP 路由 + Handler

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `topic`, `api`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
实现 `GET /api/v1/topic-cards` + `POST /api/v1/topic-cards/:id/checkin`。

## 📋 实施步骤
1. 新建 `internal/topic/http.go`:
   - `RegisterRoutes(rg, h)`:`GET /topic-cards` + `POST /topic-cards/:id/checkin`
   - `GetTopicCards(c)`:ListToday → 200 + `{items: [...]}`
   - `PostCheckin(c)`:解析 `{reflection}` → Checkin → 200 / 409 / 403
2. 单测 T-B23-3/5/6/7

## ✅ 验收
- [ ] GET 当日 3 张卡 PASS
- [ ] POST 首次打卡 PASS
- [ ] POST 重复 409 PASS
- [ ] 跨用户 403 PASS

## 🔗 依赖
- Blocked by: T-B23-3
- Blocks: T-B23-6
- Master: B23
```

---

## §6 T-B23-6 — OpenAPI 同步 + metric + 文档

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `topic`, `api`, `observability`  
**Milestone**: `V2.0 W3`

### Body

```markdown
## 🎯 目标
OpenAPI 字段对齐 + metric 暴露 + 文档注释。

## 📋 实施步骤
1. 手动编辑 `api/openapi-v1.yaml` 加 `GET /topic-cards` + `POST /topic-cards/{id}/checkin` 完整 schema
2. metric 暴露:
   - `topic_card_generated_total{success, fail}`
   - `topic_card_parse_error_total`
   - `topic_card_gen_skipped_total{reason}`
   - `topic_card_checkin_total`
3. handler 注释「Per 48 §1.7」
4. T-B23-16 性能测试(1000 活跃用户 P95 ≤ 10 min)

## ✅ 验收
- [ ] OpenAPI commit + `make api-lint` PASS
- [ ] 4 类 metric 在 `/metrics` 出现
- [ ] 1000 活跃用户批处理 P95 ≤ 10 min

## 🔗 依赖
- Blocked by: T-B23-5, B22 A4 CLOSED(联动测试)
- Blocks: 无(Master 收口)
- Master: B23
```

---

## §7 执行顺序与总工时

```
T-B23-1 (0.3d) ──┬──▶ T-B23-2 (0.2d, 依赖 B16) ──▶ T-B23-4 (0.1d)
                  └──▶ T-B23-3 (0.2d) ──────────────────▶ T-B23-5 (0.1d) ──▶ T-B23-6 (0.1d)
```

**总工时**:1.0 dev-day
**推荐 Owner**:后端调度组(H 同学)
**关键路径**:T-B23-2 阻塞 B16 CLOSED;T-B23-6 阻塞 B22 A4 CLOSED
