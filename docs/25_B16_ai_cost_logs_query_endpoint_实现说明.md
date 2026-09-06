# B16 ai_cost_logs 查询接口实现说明

## 目标

把 `ai_cost_logs` 从"只有内部 `ListRecent(...)`"推进到"HTTP 可读"，给运维、smoke 校验、未来 admin 页面一个干净的只读出口。

承接 `docs/17_第二波残留项登记.md` 的推荐落位：

> 3. `ai_cost_logs` 查询接口
>    * 当前只有内部 `ListRecent(...)`
>    * 当前第二波 issue 清单中**没有**单独 issue

## 本批边界

本批**已做**：

1. `internal/aicost/http.go` —— 新增 `Handler` + `RegisterInternalRoutes`
   * `GET /internal/v1/ai-cost-logs?user_id=&limit=`
   * 复用现有 `aicost.Service.ListRecent(...)`，不引入新的 Service 方法
   * 鉴权复用 `corpus` / `session` 已有的 `X-Internal-Token` 常量时间比较模式
   * `limit` 默认 50、上限 500（与 MemoryStore / MySQLStore 内部一致）
2. `cmd/app-server` 现在打开 `aicost.OpenStore` 并挂上 `costHandler`
3. `cmd/smoke-review-ready` 已经开 aicost.Store，现在补上 handler 让它在 MySQL 模式下也能直接 `curl /internal/v1/ai-cost-logs` 验证落库
4. 7 个 handler 单测覆盖：未带 token / token 不匹配 / 未配置 token / 跨用户列表 / `user_id` 过滤 / `limit` 上限 clamp / `limit` 默认值与负数 / 空结果非 null

本批**明确不做**：

1. 不暴露 HTTP POST 写入（cost ledger 行必须由 review worker 在事务里写，HTTP 写会破坏与 review_json 的原子性 —— 见 #21 followup）
2. 不做时间范围 / cursor 分页（先满足 smoke + 运维排查用例）
3. 不进 OpenAPI（internal 路由按现有约定不进 `api/openapi-v1.yaml`，与 `corpus` / `session` 内部路由一致）
4. 不做 admin RBAC（只用 `INTERNAL_API_TOKEN`，符合当前 internal 路由模型）
5. 不动聚合/统计（残留清单第 4 条留待 B17+ 单独 ticket）

## 当前实现口径

### 路由表

| Method | Path | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/internal/v1/ai-cost-logs` | `X-Internal-Token` | 列最近 `ai_cost_logs` 行，按 `created_at ASC` 返回 |

### 查询参数

| 参数 | 必填 | 默认 | 上限 | 说明 |
| --- | --- | --- | --- | --- |
| `user_id` | 否 | （空） | — | 空 = 跨用户列出；非空 = 仅该用户 |
| `limit` | 否 | 50 | 500 | 与 store 内部 clamp 对齐；0 / 负数 / 非数字都用默认 |

### 响应

```json
{
  "logs": [
    {
      "id": "uuid",
      "user_id": "user-7",
      "task_type": "review.eval",
      "model": "ep-review",
      "tokens_in": 1000,
      "tokens_out": 2000,
      "audio_sec": 0,
      "cost_fen": 0,
      "created_at": "2026-09-06T12:00:00Z"
    }
  ]
}
```

包装在 `logs` 键下，方便将来加 `next_cursor` / `total` / `cost_summary` 等字段而不破坏现有 JSON 形态。

### 错误形态

| 情形 | HTTP code | apierr code |
| --- | --- | --- |
| 未带 `X-Internal-Token` | 401 | UNAUTHENTICATED |
| Token 与配置不符 | 401 | UNAUTHENTICATED |
| `INTERNAL_API_TOKEN` 未配置 | 500 | INTERNAL |
| store 故障（DB 不可达） | 500 | INTERNAL（由 `aicost.Service.ListRecent` 透传） |

### 写路径仍由 #21 followup 守护

* HTTP 入口**只能读**，永远不能写
* 所有写入由 `session.MySQLStore.MarkSessionReviewedWithCost` 通过 `costTx` 回调到 `aicost.MySQLStore.RecordCostTx` 完成
* review + cost 在同一 `*sql.Tx` 内 commit（B8 followup #21 的不变量）

## 验收入口

1. 单元：
   ```bash
   go test ./internal/aicost/... -run "TestListRecentInternal" -v
   ```
2. 回归：
   ```bash
   go test ./internal/{aicost,session,httpserver,corpus,content,account}/...
   ```
3. Live（memory 模式）：
   ```bash
   APP_RUN_REVIEW_WORKER=1 ./bin/app-server &
   curl -H "X-Internal-Token: $INTERNAL_API_TOKEN" \
        "http://localhost:8080/internal/v1/ai-cost-logs?user_id=u-1&limit=10"
   ```
4. Live（MySQL 模式）：同 3，配合 `./scripts/smoke-review-ready.sh` 触发 review 落账后查询

## 已覆盖路径

| 测试 | 覆盖 |
| --- | --- |
| `TestListRecentInternal_RejectsMissingToken` | 401 UNAUTHENTICATED |
| `TestListRecentInternal_RejectsBadToken` | 401 UNAUTHENTICATED |
| `TestListRecentInternal_RejectsEmptyConfiguredToken` | 500 INTERNAL（运维配错 token 时 fail-closed） |
| `TestListRecentInternal_ReturnsAllLogsWhenUserIDEmpty` | 跨用户列出 |
| `TestListRecentInternal_FiltersByUserID` | `user_id` 过滤生效 |
| `TestListRecentInternal_ClampsLimitToMax` | `limit=9999` 不报错，返回实际行数 |
| `TestListRecentInternal_DefaultsLimitWhenZeroOrNegative` | `limit=0 / -5 / abc` 都走默认 |
| `TestListRecentInternal_EmptyResultReturnsEmptyArray` | `logs` 字段为空数组（不是 null） |
| `TestHandler_DoesNotPanicWithRealConfig` | API 漂移哨兵 |

## 未覆盖路径

1. MySQL 模式下 `ListRecent` 真查询（store 内部已覆盖：MySQLStore.ListRecent 自己的测试）
2. token 走 `crypto/subtle.ConstantTimeCompare` 的时序安全 —— 实现已对齐 corpus/session 现有模式
3. limit 超过 500 的真实 HTTP 行为（clamp 由 store 也兜底一次，handler clamp 优先；目前只验到"不报错"）

## 这么切的原因

1. **HTTP 只读、写仍走事务**：cost ledger 与 review_json 的原子性（#21 followup）是硬约束，HTTP 写入口一旦暴露就有人会去用，破坏不变量
2. **internal 路由而非 `/api/v1/`**：本接口不面对终端用户，挂在 `/internal/v1/` + token 鉴权，符合 voice-gateway 调 corpus 的现有约定
3. **handler clamp + store clamp 双保险**：handler 层给出稳定 HTTP 契约，store 层守住 DB 友好上限
4. **包装在 `logs` 键下**：未来加 `next_cursor` / `cost_summary` 字段不影响现有 iOS / smoke 解析

## 下一步

1. ~~新增 `aicost.Handler` + `GET /internal/v1/ai-cost-logs`~~ — 2026-09-06 落
2. 把 `requireInternalToken` + `internalTokenHeader` 从 corpus / session / aicost 三处抽到 `internal/httpjson` 或新建 `internal/internalhttp` 包（DRY；非阻塞）
3. 等 voice.asr / voice.tts 计量口径明确（B12 已 CLOSED 但 cost 还在 log-only），单独 ticket 接入
4. 聚合统计（B17+）：先定义口径（按用户/天/任务类型？汇总表 vs 每次 query 算？）再动手
