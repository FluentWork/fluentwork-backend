# B11 daily reads 实现说明

**对应 issue**：`B11`  
**上游**：`meta` 51 号跨仓任务 · W2-D1 / W2-D2 / W2-D3  
**依赖**：`B10`（生成内容衍生自语料）

---

## 目标

落地 `content` 模块与每日一读最小闭环：

1. migration：`daily_reads` + `uk_user_date`
2. `GET /daily-reads/today`：不存在则同步生成并返回 `ready`
3. `POST /daily-reads/:id/follow-read`：占位，不出分
4. 语料不足时使用 preset 兜底
5. 02:00 批处理骨架（`RunScheduledGeneration` 日志入口）

---

## 本批边界

**已做**：

1. `internal/content` 模块（memory + mysql store、service、http）
2. 生成策略：
   - 有语料块 → `corpus-stub-v1`
   - 无语料块 / 生成失败 → `preset-v1`
3. `account.ChainReassigner` 增加 `content.Reassigner`
4. OpenAPI、`go test ./internal/content/...`、`./scripts/smoke-daily-read.sh`

**明确不做**（后续 ticket）：

1. Ark `fw-daily-read` 真模型调用与 `ai_cost_logs`
2. TTS `audio_url` 生成
3. 02:00 批处理真实活跃用户扫描
4. 跟读评分

---

## 响应语义

| 场景 | status | daily_read |
|---|---|---|
| 首次请求（同步生成成功） | `ready` | 有 |
| 同日重复请求 | `ready` | 同一 `id`（`uk_user_date` 幂等） |
| follow-read | — | 返回 `recorded=true`，`read_score=null` |

---

## 验收入口

```bash
go test ./internal/content/...
./scripts/smoke-daily-read.sh
```

---

## 下一步

1. iOS `I10` 消费 daily read 页面
2. 可选：接入 Ark daily-read 生成 + 成本账
3. `B12` 命中检测（需 B10 + B14 门禁）
