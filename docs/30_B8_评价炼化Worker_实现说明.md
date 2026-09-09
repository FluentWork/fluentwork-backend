# B8 评价炼化 Worker 实现说明

**对应 issue**：#21  
**代码**：已在 `main`（`internal/reviewgen`、`session.processSessionFinished`、`MarkSessionReviewedWithCost`）；本提交只补关单说明  
**门禁**：`go test ./internal/reviewgen/... ./internal/session/... ./internal/aicost/... ./internal/eval/...`；B15 提测基线 `./scripts/eval-prompt-regression.sh`（103/103 PASS）

## 原理与背景

第一波 worker 只消费 `session.finished` 并写入 stub `review_json`。回顾页要真实评价 + 炼化块，且每次模型调用必须同时落 `ai_cost_logs`（技术方案 3.2：不允许后补账）。

质量面由第二波 **B15 离线回归**（GitHub #28，与语音网关那个 B15 不是同一张票）把门：生成 JSON 必须过「引用原句 + 炼化三元组 + 封闭标签」。

## 方案

一次 Ark chat-completions 同时产出 `review` 与 `refine`（`generator=ark-review-refine-v1`），用 `eval.ValidateSample` 校验。通过后：

1. `review_json` 写入 `practice_sessions`，`status=reviewed`
2. 同一 `*sql.Tx` 插入一行 `ai_cost_logs`（`task_type=review.eval`）
3. 已是 `reviewed` 的二次消费只 commit、不插账（幂等键实际是 session 状态，不是另建 `session_id+task_type` 表）

失败：生成器最多 2 次（首试 + 1 次重试）。仍失败则 **stub-v1、不写账**，转录仍可回看。凭证缺失同样走 stub。

计价：Ark Mini 公开价 0.3 / 0.6 元每百万 token，记入 `cost_fen`。Prompt 以 `reviewgen.systemPrompt` / `userPrompt` 代码常量承载（不建 `prompt_configs` 表和管理后台）。thinking 强制 `disabled`，避免 json_object 下 45–120s 空响应。

Worker / 本地 in-process worker / `smoke-review-ark` 共用 `SetReviewGenerator`。B9 已把 full review 模型对外；B10 炼化入库是另一张票；B18 在同一会话上另挂 per-utterance eval job。

细节见已有分批说明：`docs/14_B8_review_generator_第一批实现说明.md`、`docs/24_B8_followup_#21_review_cost_atomic_write_实现说明.md`、`docs/15_B8_Ark_live_诊断记录.md`。

## 缺口根因

#21 长期 OPEN，是因为评价炼化、原子写账、B9/B10 分多批合进 `main` 后没有关主票。功能不缺。

票面「失败后状态可重试」若做成 job failed 挂起，回顾页会空壳。实际选择 stub 保底：用户总能看到转录和一份可解释的 review，账只在真实 LLM 成功时出现。

## 新方案理由

- **一次调用而不是 eval/refine 拆两次**：省时延和双倍账单；B15 规则一次校验两份 JSON。
- **Store 层事务而不是 Service 记两笔**：所有 caller 都得到「结果在、账必在」。
- **Prompt 放代码**：票面允许「最小配置、不建管理后台」；改 Prompt 后跑 `./scripts/eval-prompt-regression.sh`。
- **B15 合成 103 条作提测门禁**：#28 允许内测前用合成样本；不把用户转录送进评估集。

## 明确不做

- 发音评测、APNs、Prompt 灰度中心、额度扣减
- 把 `review.refine` 再记一笔假账
- 本环境未再跑 live `smoke-review-ark`（需 Ark 凭证；2026-08-31 记录 7–9s，低于 P90 15s）
