# B15 离线评估集与 Prompt 回归基线

**对应 issue**：`B15`（`B8` 提测门禁）  
**上游**：`33` 号 Prompt 文档 §3.2/§3.3，技术方案 3.4  
**状态**：合成基线 v1 可跑；后续随真实标注样本扩到 50–100 条

---

## 一、检查什么

对每条样本自动判定：

1. **Schema 合法性**：评价 / 炼化 JSON 可解析且满足数量纪律
2. **评价引用原句**：`issues[].original_quote` 必须是转录子串；`type` 在枚举内
3. **炼化三元组完整**：`intent_zh` / `expression_en` / `anchor_user_said` 齐全；锚点在转录中；场景/功能标签在封闭枚举内

---

## 二、一键执行

```bash
./scripts/eval-prompt-regression.sh
# equivalent:
go run ./cmd/eval-prompt-regression
go test ./internal/eval/...
```

默认样本：`eval/offline/samples/wave2-synth-v1.json`（合成 / 演示素材）。

---

## 三、扩样本纪律

1. 内测前用合成样本即可启动回归；标注集到位后追加文件，不改判定规则默契
2. 每次改评价 / 炼化 Prompt 后必须跑本回归且全绿，才允许 `B8` 提测
3. 失败输出带 `sample_id` + `rule`，禁止只报总数

---

## 四、与 B8 的边界

| B15 | B8 |
|---|---|
| 离线样本 + 规则判定 | 真实 LLM 调用 + cost log + worker 接入 |
| 不调用供应商 | 必须落 `ai_cost_logs` |
| 提测前门禁 | 实现评价/炼化生成 |
