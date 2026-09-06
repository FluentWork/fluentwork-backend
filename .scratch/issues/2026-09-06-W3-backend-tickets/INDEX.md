# Skills → Tickets 索引(2026-09-06)

## 1. 全量 Skill 总览(10 个)

| Skill | Master | 状态 | Sub-tickets | 总工时 | SLA | 备注 |
|---|---|---|---|---|---|---|
| **#28 评估集** | #28 OPEN | 🟡 草稿 | 4 (T-EVAL-1..4) | 1.3d | **9/13** | 评论拆解 |
| **#21 review worker** | #21 OPEN | 🟡 草稿 | 5 (T-REV-1..5) | 2.4d | **9/20** | 评论拆解 |
| **B17 TTS Provider** | 新建 | 🟡 草稿 | 5 (T-TTS-1..5) | 1.5d | W3 内 | C-2 音色查表 |
| **B19 B7 注入** | 新建 | 🟡 草稿 | 4 (T-HIT-1..4) | 0.9d | W3 内 | 0009+0010 ✅ |
| **B25 F3 收藏置顶** | 新建 | 🟡 草稿 | 4 (T-PIN-1..4) | 0.8d | W3 内 | D-API-1 ✅ |
| **B18 review eval** | 新建 | 🟡 草稿 | 6 (T-B18-1..6) | 1.0d | W3 中期 | 依赖 B16 |
| **B21 素材模块** | 新建 | 🟡 草稿 | 7 (T-B21-1..7) | 1.5d | W3 中期 | 依赖 B16 |
| **B22 闪测 + A4 隐私** | 新建 | 🟡 草稿 | 10 (T-B22-0..9) | 2.5d | W3 内 | 0012 迁移 + 撤销 |
| **B23 话题卡** | 新建 | 🟡 草稿 | 6 (T-B23-1..6) | 1.0d | W3 中期 | 依赖 B16 + D-5 |
| **B24 历史回顾** | 新建 | 🟡 草稿 | 4 (T-B24-1..4) | 0.5d | W3 中期 | 依赖 B22 A4 |
| **合计** | **10** | — | **55** | **13.4d** | — | — |

## 2. 文件 → Ticket 映射

| 文件 | Skill | 包含 |
|---|---|---|
| `00-close-plans.md` | close plan | #43 close, #42 close |
| `01-skill-28-eval-dataset.md` | #28 | #28 评论草稿 + T-EVAL-1..4 |
| `02-skill-21-review-worker.md` | #21 | #21 评论草稿 + T-REV-1..5 |
| `03-skill-B17-TTS-Provider.md` | B17 | B17 master + T-TTS-1..5 |
| `04-skill-B19-B7-hit-injection.md` | B19 | B19 master + T-HIT-1..4 |
| `05-skill-B25-F3-pin-favorite.md` | B25 | B25 master + T-PIN-1..4 |
| `06-skill-B18-review-eval.md` | B18 | B18 master + T-B18-1..6 |
| `07-skill-B21-materials.md` | B21 | B21 master + T-B21-1..7 |
| `08-skill-B22-drill-privacy.md` | B22 | B22 master + T-B22-0..9 |
| `09-skill-B23-topic-cards.md` | B23 | B23 master + T-B23-1..6 |
| `10-skill-B24-session-history.md` | B24 | B24 master + T-B24-1..4 |

## 3. 全量依赖图

```
#28 (T-EVAL-1) ──▶ #21 (T-REV-1) ──▶ (完成)

B17 (T-TTS-1..5) ──▶ 独立
B19 (T-HIT-1..4) ──▶ 独立 (0009/0010 已就绪)
B25 (T-PIN-1..4) ──▶ 独立 (D-API-1 已拍板)

B22-0 (0012 迁移) ──┬──▶ B22-1..4 (闪测) ──┐
                    ├──▶ B22-5..7 (隐私删除) ──┼──▶ B22-9 (OpenAPI)
                    └──▶ B22-8 (撤销) ──────────┘

B16 AIOrchestrator (待建启动包)
    │
    ├──▶ B18 (review eval) ──▶ B24-3 (联动 review)
    ├──▶ B21 (素材模块) ──▶ (无下游)
    └──▶ B23 (话题卡) ──▶ (无下游)
    
B22 (T-B22-5..8 隐私删除) ──▶ B24 (T-B24-3 soft delete 过滤)
```

## 4. 推荐分工(扩展版)

| 人员 | 本周(W2 末)+ W3 任务 |
|---|---|
| **后端 AI 组(A 同学)** | T-EVAL-1..4 + T-REV-1..5 |
| **后端语音组** | B17 + T-TTS-1..5 |
| **后端语料组(C 同学)** | B19 + T-HIT-1..4 |
| **后端语料组(D 同学)** | B25 + T-PIN-1..4 |
| **后端隐私组(E 同学)** | B22 + T-B22-0..9 (工作量最大,需 1 人全力) |
| **后端 AI 组(F 同学)** | B21 + T-B21-1..7 |
| **后端 AI 组(G 同学)** | B18 + T-B18-1..6 + B24 + T-B24-1..4 |
| **后端调度组(H 同学)** | B23 + T-B23-1..6 |

> **B16 AIOrchestrator 建议**:由后端 Tech Lead 单独带 1-2 人,作为 EPIC-A 的核心骨架。其他下游(B18/B21/B23)必须 B16 CLOSED 后才能完整体测。

## 5. GitHub 落库命令(review 通过后批量执行)

```bash
# Phase 1: 关闭已修的 #43 / #42
gh issue close 43 --comment-file .scratch/issues/2026-09-06-W3-backend-tickets/00-close-plans.md
gh issue close 42 --comment-file .scratch/issues/2026-09-06-W3-backend-tickets/00-close-plans.md

# Phase 2: 在 #28 / #21 贴拆解评论(不建 master)
gh issue comment 28 --body-file .scratch/issues/2026-09-06-W3-backend-tickets/01-skill-28-eval-dataset.md
gh issue comment 21 --body-file .scratch/issues/2026-09-06-W3-backend-tickets/02-skill-21-review-worker.md

# Phase 3: 建 B19 / B25 / B17 / B18 / B21 / B22 / B23 / B24 8 个 master
gh issue create --title "B19: ..." --label "..." --body-file ...
# (见 create-w3-issues.sh)

# Phase 4: 建 55 个 sub-ticket
# (见 create-w3-issues.sh 的 extract_tickets 函数)
```

## 6. 关键约束

- ✅ 每个 ticket 工时 ≤ 0.5 dev-day
- ✅ 每个 ticket 独立可 PR / 单独 merge / 单独回滚
- ✅ acceptance criteria 量化(Go test name + 性能数字)
- ✅ ticket body 必须显式写 `Blocked by #X` 或 `depends on T-XXX-Y`
- ❌ 不允许把 ticket 写成「做 B17 的某部分」(过粗)
- ❌ 不允许跨 skill 拆 ticket(如把 B22 闪测拆到 B18 那边)

## 7. 状态机

```
🟡 草稿 (本地 scratch)
   ↓ review 通过
🟢 已建仓 + #XXX (GitHub)
   ↓ 开始实施
🔵 进行中 (assignee + milestone)
   ↓ PR merged
✅ Closed
```
