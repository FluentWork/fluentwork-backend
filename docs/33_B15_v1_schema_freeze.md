# B15 v1 schema 冻结纠正

**对应**：网关 B15 契约收口的后续修正。  
**代码**：`schemas/transport/wss-control-frames-v1.json` 撤回 `outcome` / `log_id`；`TestSchemaAITurnEndIncludesOutcomeAndLogID` 只钉 v2；新增 `TestSchemaV1AITurnEndStaysFrozenWithoutOutcome`  
**门禁**：`go test ./internal/voicegateway/... ./internal/voiceproto/... ./internal/voicepoc/...`

## 原理与背景

V1.0 控制帧清单在协议冻结会议上锁死。B15 的 `ai.turn.end.outcome` / `log_id` 是现行 speaking room（WSS V2）的增量字段，不应回写 v1。

`834729d` 为了 `additionalProperties: false` 把两份 schema 绑在一起改。handler 并不按 JSON Schema 校验出站帧，线上超时路径靠 `json.Marshal(voiceproto.AITurnEnd)` 写 `"outcome":"timeout"`。改 v1 既不能让 iOS 解到字段，也会把冻结快照改成「永远能吞下后来的字段」。

## 方案

- v2 `$defs.aiTurnEnd` 保留可选 `outcome` enum `ok|partial|timeout|error` 与 `log_id`
- v1 `$defs.aiTurnEnd` 回到 `type` + `turn_id`
- 线测 `TestHandler_AITurnEndCarriesTimeoutOutcomeOnWire` 不变：证明 handler 仍把 timeout 写到 WSS

## 缺口根因

把「现行流量带新字段」和「v1 快照必须描述现行流量」当成同一件事。speaking room 走 V2；v1 校验器若拒绝带 `outcome` 的帧，说明调用方还在用旧合同，而不是 v1 该升级。

## 新方案理由

- 不把 v1 改成能通过现行帧：冻结快照的意义就是停在当时的 13 帧形状
- 不把 Go 结构体上的 `Outcome` 拿掉：线帧和 iOS Codable 仍需要它
- 不在 handler 里按 v1 schema 拦出站帧：那会把已发布的 timeout 合同打掉

## 明确不做

- 不改 `turnToOutbound` / collectTurn 的 timeout 出口
- 不在本仓改 `fluentwork-ios`（iOS 已只对齐 v2）
- 不在本仓改 `fluentwork-infra` 真源（infra v1/v2 的 `aiTurnEnd` 仍是 `type`+`turn_id`；v2 真源同步另票）
