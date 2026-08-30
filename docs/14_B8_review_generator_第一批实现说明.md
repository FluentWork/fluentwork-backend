# B8 review generator 第一批实现说明

## 目标

在不越过 `B9` / `B10` 边界的前提下，启动 `B8`：

1. review worker 可以接入真实 Ark review/refine 生成器
2. 当前 `review_json` 存储契约保持不变
3. 生成失败时回退到 stub，避免把第一波 `review ready` 链路打坏

## 本批边界

本批**已做**：

1. 新增 `internal/reviewgen`，封装 Ark chat-completions 调用
2. 用 `B15` 规则校验生成出的 `review` 与 `refine`
3. worker / app-server / smoke-review-ready 支持注入 `ReviewGenerator`
4. 生成器失败时，自动回退到 `stub-v1`
5. 真实生成成功时写入 `ai_cost_logs`（`task_type=review.eval`，`cost_fen=0` 占位）
6. live 请求强制 `thinking.disabled`，避免深度思考模型拖垮时延（产品/场景讨论见 meta `docs/30_技术方案/35_FluentWork深度思考模型场景与落地讨论.md`，暂不改主路径）

本批**明确不做**：

1. 不修改 `GET /sessions/:id/review` 返回模型
2. 不把 `refine.blocks` 对外返回
3. 不写入 `phrase_blocks`
4. 不决定最终 `cost_fen` 计价规则

## 当前实现口径

1. 若 `ARK_API_KEY(+DEV)` 和 `ARK_EP_REVIEW_REFINE` 都配置齐：
   - worker 启动日志会打 `ark_review_enabled=true`
   - 会尝试真实 Ark 生成
2. 若未配置或生成失败（含 B15 校验失败）：
   - 回退到 `stub-v1`，且不写假账
3. 当前落库到 `review_json` 的仍只有 `review` 部分（附带 `generator` / `status` 元数据）
4. `refine` 当前只做校验和后续接入准备，不对外暴露
5. 时延：`review.generate` / `session.review.pipeline` 日志自带 `duration_ms`；live smoke 约 7–9s，目标 P90 ≤ 15s

## 验收入口

1. 单元：`go test ./internal/reviewgen/... ./internal/session/...`
2. Live：`zsh -ic 'proxy_on && ./scripts/smoke-review-ark.sh'`
3. 诊断细节见 `docs/15_B8_Ark_live_诊断记录.md`

## 这么切的原因

这样可以把 `B8` 真正启动，同时不把三个 ticket 混在一起：

- `B8`：真实生成与 worker 接入
- `B9`：回顾页 full model 返回
- `B10`：炼化入库与语料库

## 下一步

1. ~~本地带 Ark 凭证跑完整 `smoke-review-ready`~~ — 2026-08-31 PASS（3/3，`generator=ark-review-refine-v1`，cost 有行，`duration_ms` ~7–9s）
2. 继续观察线上/联调 `duration_ms` 是否稳定 ≤ 15s
3. 再决定是否进入 `B9`（full model 返回）或 `B10`（炼化入库）
