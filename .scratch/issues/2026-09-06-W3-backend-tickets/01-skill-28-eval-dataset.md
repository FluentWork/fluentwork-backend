# Skill #28 — 评估集与回归基线

> **Master**: `FluentWork/fluentwork-backend#28` (OPEN)
> **Sub-tickets**: 4 (T-EVAL-1..4)
> **总工时**: 1.3 dev-day(原 #28 估 4d,按 skills-to-ticket 切分后降低)
> **SLA**: **9/13 W2 末**(53_ §五之二 F-2)
> **依赖**: 无(无 master 依赖,可立即启动)

---

## §0 Master Issue 评论草稿(贴 #28)

```markdown
## 🎯 Skills-to-Ticket 拆解(2026-09-06)

按 Matt Pocock skills-to-ticket 方法,把 #28 切成 4 个 atomic sub-ticket,每个 ≤ 0.5 dev-day,独立可 shippable。

| # | Ticket | 工时 | 阻塞 |
|---|---|---|---|
| T-EVAL-1 | 评估集样本数据采集脚本 | 0.4d | 无 |
| T-EVAL-2 | 30 条合成样本 + 标注口径 JSONL | 0.3d | T-EVAL-1 |
| T-EVAL-3 | `scripts/eval-prompt-regression.sh` 入口骨架 | 0.3d | T-EVAL-2 |
| T-EVAL-4 | B8/B18 提测门禁挂钩 `check-eval-gate` | 0.3d | T-EVAL-3 |

### 验收口径(Master 级)
- [ ] T-EVAL-1..4 全部完成并 merge
- [ ] 评估集样本 ≥ 30 条(9/13 SLA 内补到 50 条)
- [ ] 回归入口**一条命令可跑**,输出通过/失败 + 失败明细
- [ ] B8/B18 提测门禁:评估集全绿才允许提测

### 关联
- 设计文档:`docs/50_测试与验收/57_FluentWork评估集100条样本设计_2026-09-03.md`
- #28 是 #21 review worker 的前置
- 详见:`.scratch/issues/2026-09-06-W3-backend-tickets/01-skill-28-eval-dataset.md`
```

---

## §1 T-EVAL-1 — 评估集样本数据采集脚本

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `evaluation`  
**Milestone**: `V2.0 W2`

### Body

```markdown
## 🎯 目标
编写 `scripts/collect-eval-samples.go`,从 corpus-seed + 真实会话脱敏样本采集评估集原料。

## 📋 实施步骤
1. 新建 `scripts/collect-eval-samples.go`,读 `corpus-seed` + `sessions` 表(脱敏)
2. 过滤规则:`session.status = reviewed` + `created_at > 2026-08-01`
3. 输出 JSONL:每行 `{session_id, turn_id, asr_text, ref_text, scene_type, expected_intent}`
4. 加入 Makefile target:`make collect-eval-samples`

## ✅ 验收
- [ ] 脚本可独立运行,产物 `eval/raw-samples.jsonl`
- [ ] 至少 30 条样本(9/13 SLA 内),脱敏无 user_id/原始 audio
- [ ] 单测 `TestCollectEvalSamples_FilterReviewed` PASS

## 🔗 依赖
- Blocked by: 无
- Blocks: T-EVAL-2
- Master: #28
```

### GitHub 建仓命令

```bash
gh issue create \
  --title "T-EVAL-1: 评估集样本数据采集脚本" \
  --label "backend,v2.0-blocker,priority: P1,evaluation" \
  --milestone "V2.0 W2" \
  --body-file <(cat <<'EOF'
## 🎯 目标
编写 `scripts/collect-eval-samples.go`,从 corpus-seed + 真实会话脱敏样本采集评估集原料。

## 📋 实施步骤
1. 新建 `scripts/collect-eval-samples.go`,读 `corpus-seed` + `sessions` 表(脱敏)
2. 过滤规则:`session.status = reviewed` + `created_at > 2026-08-01`
3. 输出 JSONL:每行 `{session_id, turn_id, asr_text, ref_text, scene_type, expected_intent}`
4. 加入 Makefile target:`make collect-eval-samples`

## ✅ 验收
- [ ] 脚本可独立运行,产物 `eval/raw-samples.jsonl`
- [ ] 至少 30 条样本(9/13 SLA 内),脱敏无 user_id/原始 audio
- [ ] 单测 `TestCollectEvalSamples_FilterReviewed` PASS

## 🔗 依赖
- Blocked by: 无
- Blocks: T-EVAL-2
- Master: #28
EOF
)
```

---

## §2 T-EVAL-2 — 30 条合成样本 + 标注口径 JSONL

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `evaluation`  
**Milestone**: `V2.0 W2`

### Body

```markdown
## 🎯 目标
手工合成 30 条 + 标注 `eval/eval-set-v1.jsonl`,覆盖 PRD 5 大场景。

## 📋 实施步骤
1. 复制 `eval/raw-samples.jsonl` 中 30 条到 `eval/eval-set-v1.jsonl`
2. 手工标注每条:
   - `expected_intent`(FluentWork 用户意图标签)
   - `expected_phrase_block_match`(若有)
   - `tolerance`(语义等价判定容差:strict/loose)
3. 覆盖场景:free_talk / drill / daily_read / corpus_hit / interrupt_recovery

## ✅ 验收
- [ ] 30 条样本,5 大场景各 ≥ 5 条
- [ ] 标注口径与 `57_` 设计文档一致
- [ ] 单测 `TestEvalSetV1_Schema` PASS

## 🔗 依赖
- Blocked by: T-EVAL-1
- Blocks: T-EVAL-3
- Master: #28
```

---

## §3 T-EVAL-3 — `scripts/eval-prompt-regression.sh` 入口骨架

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `evaluation`  
**Milestone**: `V2.0 W2`

### Body

```markdown
## 🎯 目标
编写回归入口脚本 + 输出 schema + 集成到 CI(可选)。

## 📋 实施步骤
1. 新建 `scripts/eval-prompt-regression.sh`:
   - 读 `eval/eval-set-v1.jsonl`
   - 调 `internal/reviewgen.RunEval(ctx, sample)`(stub 即可,T-REV-1 替换)
   - 输出 PASS/FAIL + 失败明细到 `eval/results/<timestamp>.json`
2. 输出 schema:
   ```json
   {
     "version": "v1",
     "total": 30,
     "passed": 28,
     "failed": 2,
     "failures": [{"sample_id": "...", "expected": "...", "actual": "...", "diff_score": 0.42}]
   }
   ```
3. Makefile target:`make eval-prompt-regression`

## ✅ 验收
- [ ] 脚本可独立运行,exit code 0=PASS,1=FAIL
- [ ] 输出 schema 与 B8/B18 提测门禁可读
- [ ] 单测 `TestEvalRegression_OutputSchema` PASS

## 🔗 依赖
- Blocked by: T-EVAL-2
- Blocks: T-EVAL-4
- Master: #28
```

---

## §4 T-EVAL-4 — B8/B18 提测门禁挂钩 `check-eval-gate`

**Labels**: `backend`, `v2.0-blocker`, `priority: P1`, `evaluation`  
**Milestone**: `V2.0 W2`

### Body

```markdown
## 🎯 目标
`check-eval-gate` 钩子,B8/B18 提测前必跑,失败阻断 PR merge。

## 📋 实施步骤
1. 新建 `scripts/check-eval-gate.sh`:调 `eval-prompt-regression.sh` 并断言 PASS
2. 接入 `.github/workflows/backend-ci.yml`(若存在)或 OpenCodeReview 配置:
   - 触发条件:PR 修改 `internal/reviewgen/**` 或 `internal/aicost/**`
3. 文档化:`docs/40_研发流程与协作/75_eval_gate_runbook.md`(待建)

## ✅ 验收
- [ ] CI 工作流包含 `check-eval-gate` step
- [ ] 故意改坏 T-REV-1 输出,验证 CI 红灯
- [ ] PR description 模板新增「eval-gate ✅」勾选项

## 🔗 依赖
- Blocked by: T-EVAL-3
- Blocks: 无(Master 收口)
- Master: #28
```

---

## §5 执行顺序与总工时

```
T-EVAL-1 (0.4d) ──▶ T-EVAL-2 (0.3d) ──▶ T-EVAL-3 (0.3d) ──▶ T-EVAL-4 (0.3d)
                                                              │
                                                              ▼
                                                          #28 CLOSED
```

| 人员 | 任务 | 时长 |
|---|---|---|
| 后端 AI 组(A 同学) | T-EVAL-1 → T-EVAL-2 | 0.7d (W2 末前完成) |
| 后端 AI 组(A 同学) | T-EVAL-3 → T-EVAL-4 | 0.6d (W3 Day 1-2) |

## §6 GitHub 落库命令

> 见 `INDEX.md` §5 的批量执行,此处仅列单 ticket 命令模板

```bash
# T-EVAL-1..4 4 个 ticket 顺序建仓(参考 §1 命令模板)
# 注意:每个 ticket 必须带 --body-file <(cat <<'EOF' ... EOF)
#       Master 引用:#28
```
