# B9 review endpoint full model 实现说明

## 目标

把 `GET /sessions/:id/review` 从“只返回 review 部分”升级为“返回完整回顾模型”，作为 iOS `I7` 的服务端前置。

## 本批边界

本批**已做**：

1. `review_json` 统一升级为完整文档：
   - `review`
   - `refine`
   - `generator`
   - `status`
   - `duration_sec`
2. `GET /sessions/:id/review` 对历史 review-only 数据做兼容包装
3. worker 生成新 review 时直接落完整文档

本批**明确不做**：

1. 不写 `phrase_blocks`
2. 不新增 `corpus` 表与 CRUD
3. 不改变 `ai_cost_logs` 口径
4. 不扩 daily reads / content 模块

## 当前存储口径

`practice_sessions.review_json` 现在承载完整回顾文档。

新格式：

```json
{
  "review": {...},
  "refine": {"blocks":[...]},
  "generator": "ark-review-refine-v1",
  "status": "ready",
  "duration_sec": 18
}
```

兼容口径：

1. 若历史数据还是旧格式 review-only：
   - `GetReview` 会在读路径包成 full model
2. 因此不需要 migration

## 设计原因

`B9` 的目标是回顾页 full model 返回，不是语料入库。

所以最小正确实现是：

1. 先把 full model 放进现有 `review_json`
2. 对外统一返回 full model
3. 把 `B10` 的 `phrase_blocks` 入库继续留在下一票

## 验证

1. 单测：

```bash
go test ./internal/session ./internal/reviewgen ./internal/config
```

2. 端到端：

```bash
zsh -ic 'proxy_on && ./scripts/smoke-review-ready.sh'
```

当前结果：

- PASS
- `generator=ark-review-refine-v1`
- `ai_cost_logs` 有行
- `ready_wait_ms` 落在 15s 以内
- 返回体中的 `review` 已是 full model
