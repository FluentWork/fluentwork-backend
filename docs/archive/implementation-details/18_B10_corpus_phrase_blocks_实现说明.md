# B10 corpus phrase_blocks 实现说明

**对应 issue**：`B10`  
**上游**：`meta` 51 号跨仓任务 · W2-C3  
**依赖**：`B9`（炼化卡数据来自 review full model）

---

## 目标

提供语料库基础能力，支撑 iOS `I8`（炼化入库）与后续 `B12`（命中检测）：

1. `phrase_blocks` 表 + migration
2. `GET/PUT/DELETE /corpus/blocks`
3. `POST /corpus/blocks/:id/favorite`
4. `POST /corpus/blocks/batch-accept`（幂等入库）
5. 游客归并时语料块随 `account/merge` 迁移

---

## 当前状态（main + 本分支 close-out）

**已在 main 落地**（`ff4ed85` / `e274f5b`）：

- `internal/corpus` 全模块（memory + mysql store、service、http、schedule）
- migration `0006_create_phrase_blocks.sql`
- OpenAPI 五路由
- `account.ChainReassigner` 串联 session + corpus

**本分支 close-out**：

1. `kw` 搜索纳入 `anchor_user_said`（memory + mysql LIKE）
2. 补 `new → training` 调度单测
3. 补 guest merge HTTP 契约测试
4. 新增 `./scripts/smoke-corpus.sh` + `cmd/smoke-corpus`

---

## 关键口径

### batch-accept 幂等键

`(user_id, source_session_id, expression_en, anchor_user_said, scene_tag, function_tag)`

重复提交返回同一 block；软删后 re-accept 会 revival（`deleted_at = NULL`）。

### 软删除

`DELETE /corpus/blocks/:id` 写 `deleted_at`；列表默认过滤已删块。

### 游客归并

`corpus.Reassigner` 已挂到 `account.ChainReassigner`；merge 后 registered token 可 list 到 guest 期入库块。

---

## 验收入口

```bash
go test ./internal/corpus/...
./scripts/smoke-corpus.sh
```

---

## 下一步

1. 合并本 close-out PR，关闭 B10 ticket
2. 启动 `B11`（daily reads）或 iOS `I8` batch-accept 接线
