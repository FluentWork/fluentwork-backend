# ai_cost_logs 写入模块启动说明

## 当前目标

把 `ai_cost_logs` 从“只有 migration”推进到“后端具备可调用的同步写入能力”。

本次只完成**账本写入底座**，不伪造尚未接入的真实计费链路。

## 已落内容

- `internal/aicost/types.go`
- `internal/aicost/store.go`
- `internal/aicost/memory_store.go`
- `internal/aicost/mysql_store.go`
- `internal/aicost/service.go`
- `internal/aicost/service_test.go`
- `session.Service` 已接入 `SetCostRecorder(...)`
- `cmd/app-server` / `cmd/worker` / `cmd/smoke-review-ready` 已完成依赖注入
- `review worker` 已加“stub 跳过记账、真实 AI 必须有 recorder”的约束

## 模块边界

当前能力：

1. 校验一条 `ai_cost_logs` 记录
2. 同步写入 memory / MySQL store
3. 按用户读取 recent logs
4. review worker 已具备正式记账挂点

当前**没有**做的事：

1. 不自动挂到任何 AI 调用上
   - 这里的准确边界是：**已经挂到 review worker 的扩展点，但当前 stub generator 不会写假账**
2. 不计算真实供应商价格
3. 不提供 HTTP 查询接口
4. 不提供聚合统计表

## 这么切的原因

先把账本写入模块独立出来，有两个好处：

1. 后续 review / B7 / daily-read / ASR / TTS 接入时，不会再把“调用逻辑”和“记账逻辑”搅在一起
2. 可以先把同步落账纪律做成代码约束，再逐步补真实价格模型

## 下一接入点

推荐按这个顺序挂接：

1. `B8` 已完成第一步：
   - 当前 review/refine 是**一次合并的 Ark 调用**
   - 因此账本按**一次真实 AI 调用写一条**，当前口径固定为 `review.eval`
   - **不再**为了凑“评价/炼化”字面拆成两条假账
   - 若后续真的拆成两个独立模型调用，再在对应 ticket 中新增 `review.refine`
2. `B12` hit detect 接实时旁路时，写 `voice.hit_detect`
3. 语音供应商明确计量口径后，补 `voice.asr` / `voice.tts`
4. 最后再加查询接口和聚合统计

## 2026-08-31 口径补记

为避免后续遗忘，这里冻结两条规则：

1. `ai_cost_logs` 的最小记账单位是**一次真实供应商调用**
2. 在 `B8` 当前实现中，review 与 refine 共用一次 Ark 请求，所以只记一条 `review.eval`
