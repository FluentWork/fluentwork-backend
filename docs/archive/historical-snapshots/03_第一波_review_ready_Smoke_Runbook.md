# FluentWork Backend 第一波 review ready Live Smoke Runbook

**版本**：V1.0  
**日期**：2026-08  
**定位**：固定第一波 backend 活体验证入口，证明 `session.end -> worker -> review ready` 可本地重复跑通  
**对应门禁**：`W1-BE-3` / `W1-START-2`（见 `fluentwork-meta` 51/52 号文档）

---

## 一、这条 runbook 证明什么

最小链路：

1. guest auth
2. create session
3. activate session（内部接口，模拟网关）
4. `session.end` 落库并投递 `session.finished`
5. worker 处理 job，写入 stub `review_json`
6. `GET /sessions/:id/review` 返回 `status=ready`，含 `generator`

默认模式不依赖 Docker：HTTP handlers 与 worker loop 同进程共址，使用内存存储。  
可选 `--mysql`：走 Docker MySQL + 共享 DSN，更接近跨进程部署形态。

---

## 二、前置条件

1. Go 1.26+
2. 默认可直接跑，无需 Docker
3. `--mysql` 需要本机 Docker daemon 可用

---

## 三、一键执行

```bash
./scripts/smoke-review-ready.sh
```

可选 MySQL：

```bash
./scripts/smoke-review-ready.sh --mysql
```

成功时 stdout 会打印：

1. `wave1 review-ready smoke PASS`
2. JSON evidence：`session_id` / `review_status` / `generator` / `utterance_count` / `steps`
3. 当前 mode（memory 或 MySQL）

若已配置 `.env.volc.local`（`ARK_API_KEY`/`ARK_API_KEY_DEV` + `ARK_EP_REVIEW_REFINE`）且代理可达方舟：

1. `ark_review_enabled=true`
2. evidence 断言 `generator=ark-review-refine-v1`
3. 断言内存/`ai_cost_logs` 有 `task_type=review.eval` 行
4. 断言 `ready_wait_ms ≤ 15000`（端到端就绪等待；日志另有 `review.generate.done.duration_ms`）

2026-08-31 本地 live（thinking disabled，3 次）：

| 次 | `review.generate.done` | `ready_wait_ms` | cost |
|---|---|---|---|
| 1 | 7269ms | 7393 | tokens 834/452 |
| 2 | 8982ms | 9128 | tokens 831/396 |
| 3 | 8053ms | 8248 | tokens 830/534 |

均 `generator=ark-review-refine-v1`，稳定落在 P90 ≤ 15s 红线内。

失败退出码非 0。排障顺序：

1. app-server HTTP 路径（guest / sessions / review）
2. worker `ProcessNextJob`（job claim / stub review 写入）
3. store 层（memory 同进程；MySQL 则查 compose / migration / DSN）

---

## 四、手动等价步骤（理解用）

默认推荐直接跑脚本。若要理解链路：

1. 游客鉴权：`POST /api/v1/auth/guest`
2. 创建会话：`POST /api/v1/sessions`
3. 激活：`POST /internal/v1/sessions/activate`
4. 结束：`POST /internal/v1/sessions/end`（带 utterances）
5. 轮询：`GET /api/v1/sessions/:id/review` 直到 `ready`

当前第一波无 Ark 凭证时 generator 为 `stub-v1`；配置齐后走第二波 `B8` 真实路径（见上表）。

---

## 五、通过标准

1. 脚本可重复执行，不依赖手工改库
2. 输出 `review_status=ready` 且 `generator` 非空（无 Ark：`stub-v1`；有 Ark：`ark-review-refine-v1` + cost 行）
3. `steps` 覆盖 guest → create → activate → end → ready
4. 失败时日志能指向 HTTP / worker / store 之一
5. Ark 路径：`ready_wait_ms` 与 `review.generate.done.duration_ms` 应 ≤ 15s（告警红线）

---

## 六、与 `dev-up.sh` 的关系

| 入口 | 覆盖范围 |
|---|---|
| `./scripts/dev-up.sh` | 常驻本地开发；smoke 到 guest auth |
| `./scripts/smoke-review-ready.sh` | 第一波放行证据：跑到 `review ready` 后退出 |

开发联调继续用 `dev-up.sh`；波次放行证据用本 runbook。
