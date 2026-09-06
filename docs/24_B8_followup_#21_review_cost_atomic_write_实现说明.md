# B8 followup #21 review+cost 原子写实现说明

## 目标

在 `#21` 之前，`processSessionFinished` 走的是 **review 写 + cost 写两个独立调用** 的路径，
存在三类风险：

1. review 写成功、cost 写失败 → 账本漏记，事后对不上
2. review 写失败、cost 写成功 → 用户看到没有回顾但被计费
3. retry 风暴下 cost 重复写 → 同一会话被计两次

`#21` 的目标是把这两件事压成 **一次事务**，并在 writer 层兜住幂等。

## 本批边界

本批**已做**：

1. `aicost.Store.RecordCostTx(ctx, tx, log)` — 让调用方在自有事务里写一行 `ai_cost_logs`
   * memory 版 no-op（无外部事务语义，调用方需自行容错 nil）
   * MySQL 版断言 `tx` 是 `*sql.Tx`，并 `ExecContext` 同样的 INSERT
2. `session.Store.MarkSessionReviewedWithCost(...)` — review 与 cost 在同一事务内 commit
   * memory 版用 mutex 把两写锁在一起；幂等命中（status 已是 reviewed）只 commit 不重复记
   * MySQL 版 `BeginTx + SELECT ... FOR UPDATE + UPDATE sessions + 委托 costTx 写 ai_cost_logs + Commit`；
     任一步出错 `Rollback` 整笔；幂等命中也走 Commit 但跳过 cost insert
   * `MySQLStore.costTx` 由 `session.OpenStore` 在 MySQL 分支里 wire 到
     `aicost.MySQLStore.RecordCostTx`，成本 INSERT 的 SQL 只在 aicost 包维护一份
3. `processSessionFinished` 编排层：
   * `artifacts.Cost == nil` → 走 `MarkSessionReviewed`（stub 不写账本）
   * `artifacts.Cost != nil` → 走 `MarkSessionReviewedWithCost`（真实 AI 用量）
4. `buildReviewArtifacts` 加 retry：`reviewRetryAttempts=2`（首试 + 1 次重试）后回退到 stub
5. `computeCostFen` 用 Ark Mini 2026-09 list price 计价（0.3 / 0.6 元每 M token → 0.03 / 0.06 分每 token）
6. 干掉迁移遗留：`Service.costRecorder`、`CostRecorder` 接口、`SetCostRecorder`、`recordReviewCost`，
   以及 `cmd/app-server` / `cmd/worker` 的 aicost 注入（`#21` 之前已经被原子写取代）
7. `internal/session/mysql_store_test.go` 用 `DATA-DOG/go-sqlmock` 覆盖 MySQL 真实事务路径（5 个
   `MarkSessionReviewedWithCost` 行为用例 + 1 个列数哨兵），不再依赖 docker；`session.OpenStore`
   测试 helper 在 MySQL 模式下额外 wire `aicost.MySQLStore.RecordCostTx` 以保持生产路径一致

本批**明确不做**：

1. 不动 Ark provider 实现本身（`internal/reviewgen/ark.go` 不在范围）
2. 不改 `ai_cost_logs` 表结构（仅写入路径换事务）
3. 不补 docker-compose / 真实 MySQL 集成测试；sqlmock 已覆盖 SQL 字符串、事务边界、幂等语义

## 当前实现口径

### 事务边界

```
processSessionFinished
├── buildReviewArtifacts
│   ├── attempt 1: reviewGen.Generate(...)
│   ├── attempt 2: reviewGen.Generate(...)   // 失败时
│   └── 仍失败: buildStubReviewArtifacts(...)
├── artifacts.Cost == nil
│   └── store.MarkSessionReviewed(...)
└── artifacts.Cost != nil
    └── store.MarkSessionReviewedWithCost(...)
        ├── BeginTx
        ├── SELECT ... FROM practice_sessions WHERE id=? FOR UPDATE
        ├── switch session.Status
        │   ├── reviewed → Commit (no cost insert; 幂等)
        │   ├── ended    → UPDATE sessions
        │   │             + costTx(ctx, tx, log)  // 委托 aicost.MySQLStore.RecordCostTx
        │   │             + Commit
        │   └── other    → Rollback + ErrConflict
        └── 任一错误 → Rollback + 返回错误

session.OpenStore (MySQL 分支)
├── sql.Open(mysql, dsn)
├── aicost.OpenStore(cfg)         // 共享同一个 *sql.DB
├── NewMySQLStore(db)
└── store.SetCostTx(costStore.RecordCostTx)
```

### 计价口径

| 字段 | 值 |
| --- | --- |
| 任务类型 | `review.eval` |
| 输入价 | 0.3 元 / 1M token = 0.03 分 / token |
| 输出价 | 0.6 元 / 1M token = 0.06 分 / token |
| 取整 | round-half-up 到分（`int(fen + 0.5)`） |
| 切换点 | 后续若 Ark 出 model-specific pricing，在 `computeCostFen` 内按 `result.Model` 分支 |

### Retry 口径

| 行为 | 次数 |
| --- | --- |
| 总尝试数 | `reviewRetryAttempts=2`（首试 + 1 重试） |
| 失败 1 次后 | 记 `attempt=1/max=2` warn，继续重试 |
| 两次都失败 | 记 `attempts=2` warn，回退 stub；不写 cost row |

### 幂等口径

* `MarkSessionReviewedWithCost` 在 status 已是 `reviewed` 时：
  * memory 版：返回现有 session，不写 cost
  * MySQL 版：`Commit` 一个空事务，返回现有 session，不 INSERT cost
* 触发条件：worker 重启 / 同一 job 被多个 worker 抢到（即使 worker_id 已避免）

## 验收入口

1. 单元（内存版）：
   ```bash
   go test ./internal/session/... -run "MarkSessionReviewedWithCost|BuildReviewArtifacts|ComputeCostFen|BuildCostLog" -v
   ```
2. 回归：
   ```bash
   go test ./internal/session/... ./internal/aicost/... ./internal/reviewgen/... ./internal/corpus/...
   ```
3. Live（MySQL 模式）：
   ```bash
   ./scripts/smoke-review-ready.sh
   # 预期：review_json 落 + ai_cost_logs 出现一行 review.eval + cost_fen ≈ 0.03~0.06 分
   ```

## 已覆盖路径

| 测试 | 覆盖 |
| --- | --- |
| `TestBuildReviewArtifacts_RetriesOnceBeforeStubFallback` | 两次失败 → fallback；`gen.calls == 2` |
| `TestBuildReviewArtifacts_SucceedsOnSecondAttempt` | 第一次失败、第二次成功 → 走成功路径 |
| `TestComputeCostFen` × 6 子用例 | 0 / 1M-only / mixed / 100k+200k / negative clamp |
| `TestBuildCostLog_FieldMapping` | ID/TaskType/Model/tokens/timestamp/UserID fallback |
| `TestBuildCostLog_KeepsExplicitUserID` | 显式 UserID 不被覆盖 |
| `TestNullableUserID` | 空 / 全空白 → nil |
| `TestMarkSessionReviewedWithCost_Memory_BothWritesLand` | review_json + cost log 同 mutex 提交 |
| `TestMarkSessionReviewedWithCost_Memory_IdempotentNoDoubleBill` | 二次 Mark 不重复写 cost |
| `TestMarkSessionReviewedWithCost_Memory_RejectsNonEnded` | 非 Ended 返回 `ErrConflict` |
| `TestMySQLStore_MarkSessionReviewedWithCost_AtomicOnEnded` | sqlmock：BeginTx / SELECT FOR UPDATE / UPDATE / INSERT ai_cost_logs（走 aicost.RecordCostTx）/ Commit 全部命中 |
| `TestMySQLStore_MarkSessionReviewedWithCost_RollsbackOnCostInsertFailure` | sqlmock：cost INSERT 失败 → Rollback（验证事务回滚） |
| `TestMySQLStore_MarkSessionReviewedWithCost_IdempotentNoDoubleBill` | sqlmock：status=reviewed → BeginTx/SELECT/Commit，**不**发 UPDATE/INSERT（幂等） |
| `TestMySQLStore_MarkSessionReviewedWithCost_RejectsNonEnded` | sqlmock：非 Ended → Rollback + ErrConflict |
| `TestMySQLStore_MarkSessionReviewedWithCost_NotFound` | sqlmock：SELECT 返 ErrNoRows → Rollback + ErrNotFound |
| `TestMySQLStore_MarkSessionReviewedWithCost_RejectsUnwiredCostTx` | `SetCostTx` 未调 → 拒绝运行、不发任何 SQL |
| `TestSessionColumnCountMatchesScanSession` | 列数哨兵：mysql_store.scanSession 与测试期望 9 列对齐 |

## 未覆盖路径

1. `computeCostFen` 在 `result.Model` 切换为 model-specific 时的分支
   * 等 Ark 实际提供 model-specific 价格后再补
2. 真实 MySQL 端到端（不走 sqlmock）
   * 当前依赖 `cmd/smoke-review-ready` 走真实 MySQL 路径间接验证

## 这么切的原因

1. **事务边界对齐业务不变量**：账本漏记和 review 缺失必须同生同灭，不能由调用方记日志对账
2. **Store 层做原子性而不是 Service 层**：让所有 caller（包括将来直接调 store 的内部脚本）都享受到事务保护
3. **memory 版保留 + 写 mutex**：让单元测试可以断点验证两写一致性；上线路径走 MySQL 版
4. **幂等放在 Status.Reviewed 判断里**：避免给 worker 增加额外 "is this a retry" 状态机字段

## 下一步

1. ~~atomic review + cost 接入~~ — 2026-09-06 落（commit：`d270708` / `d877eed` / `2488dc8` / `67501d9` / `b99b811` / `0af46b0` / `f4b8dee` / `c78f9b5`）
2. ~~给 `MySQLStore.MarkSessionReviewedWithCost` 加 sqlmock 集成测试（不引入 docker）~~ — 2026-09-06 落（commit `f4b8dee`，7 个用例覆盖原子写 / 回滚 / 幂等 / 状态校验 / NotFound / 列数哨兵 / costTx 未接线守护）
3. ~~把 `MySQLStore.MarkSessionReviewedWithCost` 的 inline INSERT 委托给 `aicost.MySQLStore.RecordCostTx`~~ — 2026-09-06 落（commit `c78f9b5`），让 cost SQL 只在 aicost 包维护一份
4. Ark 出 model-specific pricing 后，把 `computeCostFen` 的 switch 分支补上
5. 观察线上 `cost_fen` 与 Ark billing 的对账偏差，确认口径稳定
