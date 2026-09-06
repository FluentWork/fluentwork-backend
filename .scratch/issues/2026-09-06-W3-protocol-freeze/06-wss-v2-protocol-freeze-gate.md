# P0-PROTO-06 — WSS V2.0 协议封板 checklist + 跨仓空跑验证(唯一硬死线)

> **GitHub Title**: `P0-PROTO-06: WSS V2.0 协议封板 checklist + 跨仓空跑验证(9/15 唯一硬死线)`
> **Labels**: `v2.0-blocker`, `priority/P0`, `protocol-freeze`, `gate`, `milestone-freeze`
> **Milestone**: `V2.0 W3`
> **截止**: **2026-09-15(周一)23:59(唯一硬死线)**
> **Owner**: 后端 TL (主持 gate) + iOS Lead (联合签字)
> **关联**: 79_ V2.0 §九 + §十一 评审通过条件

---

## §0 Issue Body(整段可粘贴)

```markdown
## 🎯 目标
9/15(周一)完成 WSS V2.0 协议封板 gate, 完成所有 voiceproto 代码与 JSON Schema 落库, 跨仓空跑验证通过, 后端 TL + iOS Lead 联合签字后关闭本 Issue。**此为 V2.0 协议冻结唯一硬死线, 9/15 之后任何 WSS V2.0 帧字段变更需走 CCB(架构变更委员会)**。

## 🚧 阻塞条件
- P0-PROTO-03 全部 sub-ticket PR merged
- P0-PROTO-04 ADR merged
- P0-PROTO-05 iOS mock decoder merged + 跨仓空跑通过

## 📋 Gate Checklist(11 项, 全 PASS 才算封板)

### A. 文档归档(3 项)

- [ ] **A1**: `docs/30_技术方案/51_WSS_V2_帧协议设计_2026-09-08.md` 已 commit 到 main, 包含 ai.tts.start/audio/end 三帧 + server_ts_ms 决策
- [ ] **A2**: `docs/40_研发流程与协作/73_seed_tts_SDK_Opus_Decision_2026-09-09.md` ADR-0073 已 merge
- [ ] **A3**: 79_ V2.0 评审闭环(后端 TL + iOS Lead 评论签字, 见 §十一 采纳记录)

### B. backend 仓代码落库(3 项)

- [ ] **B1**: `internal/voiceproto/frames.go` 新增 3 个结构体(`AITTSStart` / `AITTSAudio` / `AITTSEnd`) + 3 个类型常量(`TypeAITTSStart` / `TypeAITTSAudio` / `TypeAITTSEnd`)
- [ ] **B2**: `internal/voiceproto/frames_test.go` 新增单元测试, 覆盖 3 个新帧的 JSON 编解码 + 类型常量唯一性
- [ ] **B3**: `schemas/transport/wss-control-frames-v2.json` 新增 3 个 `$defs` + `oneOf` 引用, 同步到 `fluentwork-infra/schemas/`(用 `scripts/sync-shared-schemas.sh`)

### C. iOS 仓验证(2 项)

- [ ] **C1**: ios 仓 P0-PROTO-05 已 merged, MockTTSDecoder 单元测试 6 个 PASS
- [ ] **C2**: 跨仓空跑验证 SOP(9/13 已跑过)产物保留在 `docs/40_研发流程与协作/74_WSS_V2_跨仓空跑验证_2026-09-13.md`

### D. schema & 迁移一致性(2 项)

- [ ] **D1**: backend staging 与 production schema 一致(`SHOW TABLES` + 列对比, 见 79_ §七.3 风险)
- [ ] **D2**: 50_ §3.4 server_ts_ms 决策段落已更新, git commit hash 引用

### E. 签字(1 项)

- [ ] **E1**: 后端 TL + iOS Lead 在本 Issue 评论中各留一句"封板确认"评论, 含 git commit hash

## ✅ 验收
- [ ] 11 项 checklist 全部 PASS
- [ ] E1 联合签字到位
- [ ] 本 Issue 关闭后, GitHub Project 看板更新"协议冻结"列为"已封板"
- [ ] 通知 PM / 商务: WSS V2.0 已冻结, 后续 B17/B19/B22/B25 等 Issue 可正常推进
- [ ] 9/15 之后任何 WSS V2.0 帧字段变更走 CCB(本 Issue 评论留 CCB 入口)

## 🔗 依赖
- Blocked by: P0-PROTO-03 + P0-PROTO-04 + P0-PROTO-05 全部 CLOSED
- Blocks: B17 TTS Provider T-TTS-2(流式 chunk 实现依赖 codec 字段值确定)
- 关联: 79_ V2.0 §九 行动清单 #9(协议封板)
```

---

## §1 跨仓空跑验证产物要求(9/13 必须完成)

### 1.1 文件: `docs/40_研发流程与协作/74_WSS_V2_跨仓空跑验证_2026-09-13.md`

必须包含:
1. 验证日期 + 验证人(iOS Lead + 后端 TL)
2. backend dev-echo provider TTS mock 输出 10 个 ai.tts.audio 帧的截图/日志
3. iOS 端 MockTTSDecoder 记录的 prepare/feed/finish 调用次数断言:
   - prepares.count == 1
   - feeds.count == 10
   - finishes.count == 1
4. 任何意外行为的备注
5. 联合签字

### 1.2 自动化脚本: `scripts/wss-v2-empty-run.sh`(backend 仓)

```bash
#!/bin/bash
# WSS V2.0 跨仓空跑验证 SOP
# 用法: ./scripts/wss-v2-empty-run.sh
# 退出码 0 = 通过, 1 = 失败

set -e

echo "[1/4] 启动 backend (DEV_ECHO_TTS_MOCK=true)..."
cd "$(dirname "$0")/.."
DEV_ECHO_TTS_MOCK=true ./scripts/dev-up.sh
sleep 5

echo "[2/4] 验证 backend dev-echo 端口..."
nc -z localhost 8080 || { echo "backend 未起来"; exit 1; }

echo "[3/4] 触发 WSS 客户端连入并发送音频..."
# 调用 cmd/integration-voice-gateway 或类似工具
go run ./cmd/integration-voice-gateway --mock-tts

echo "[4/4] 检查 backend 日志输出 ai.tts.* 帧..."
LOG=$(tail -100 logs/voice-gateway.log)
echo "$LOG" | grep -q "ai.tts.start" || { echo "未输出 ai.tts.start"; exit 1; }
echo "$LOG" | grep -q "ai.tts.audio" || { echo "未输出 ai.tts.audio"; exit 1; }
echo "$LOG" | grep -q "ai.tts.end" || { echo "未输出 ai.tts.end"; exit 1; }

echo "✅ 跨仓空跑验证通过"
```

---

## §2 9/15 后变更管控(CCB 流程)

### 2.1 CCB 触发条件

9/15 之后, 以下任何变更需走 CCB(架构变更委员会):

1. 新增 WSS V2.0 帧
2. 修改已有帧的 required 字段
3. 修改 codec 字段可选值("opus" / "pcm" 之外的第三个)
4. 修改 sample_rate 字段可选值
5. 修改 ai.text.delta 的 server_ts_ms 字段类型

### 2.2 CCB 流程

```
变更申请人提 CCB 单
   ↓
CCB 评审会(后端 TL + iOS Lead + PM + Tech Lead)
   ↓
评估影响范围(iOS 端回归成本 / backend 端回归成本)
   ↓
同意 → 走标准 PR 流程(额外加 CCB 标签 + 引用 CCB 单号)
不同意 → 驳回, 留档, 走 V3.0 规划
```

预估: CCB 流程耗时 2-3 天, 单次变更成本 1-2 人天。

---

## §3 gh CLI 落库命令

```bash
cd /Users/apple/Developments/FluentWork\ App/fluentwork-backend

gh issue create \
  --title "P0-PROTO-06: WSS V2.0 协议封板 checklist + 跨仓空跑验证(9/15 唯一硬死线)" \
  --label "v2.0-blocker,priority/P0,protocol-freeze,gate,milestone-freeze" \
  --milestone "V2.0 W3" \
  --body-file .scratch/issues/2026-09-06-W3-protocol-freeze/06-wss-v2-protocol-freeze-gate.md
```

---

## §4 风险与回滚

| 风险 | 应对 |
|---|---|
| 9/13 跨仓空跑验证失败 | Issue 05 不关闭, 9/14 紧急修复, 9/15 gate 顺延到 9/16(触发 PM 警报) |
| B1/B2/B3 backend 代码落仓冲突 | 强制 rebase 到最新 main, 优先合 P0-PROTO-06 阻断 B17 启动 |
| iOS 仓 Mock decoder 与 B17 真实解码冲突 | iOS 端用编译条件 `#if DEBUG_TTS_MOCK` 隔离, 真实代码用 ADR-0073 决策的库 |
| 后端 TL / iOS Lead 任一缺席封板 gate | 提前 3 天预约, 缺席则顺延, 不允许代理签字 |
| 9/15 当天发现新需求字段 | 立即开 CCB 单, 走 V3.0 规划, 不在 V2.0 范围夹带 |

---

## §5 reviewer checklist(后端 TL + iOS Lead 联合 review 时用)

- [ ] 11 项 checklist 是否覆盖所有 79_ V2.0 评审通过条件?
- [ ] B1/B2/B3 是否与 Issue 03 字段定义**逐字一致**?(防漂移)
- [ ] D1 schema 一致性是否包含 0008-0011 已 commit 的 V2.0 迁移?
- [ ] E1 联合签字是否引用具体 commit hash 而非模糊"已 review"?
- [ ] CCB 流程是否包含预估耗时(2-3 天)以便 PM 排期?
- [ ] 跨仓空跑 SOP 是否可在 30 分钟内完整跑通?(若超时需简化)

---

## §6 不在本 Issue 范围内

- ❌ 不实施 B17 TTS Provider 代码(完全独立, Issue 06 通过后才能启动 T-TTS-2)
- ❌ 不实施 §七-B.3 WSS 写入缓冲池(独立 ADR-0072, W7 启动)
- ❌ 不修改 iOS 仓真实 Opus 解码逻辑(Issue 05 已用 MockTTSDecoder 占位)
- ❌ 不动 50_ §3.4 之外的全链路架构段落(留给 V3.0 规划)
- ❌ 不在 9/15 之后开新字段(走 CCB)

---

## §7 时间窗口复盘

```
9/6 (周日) 今晚   ────  Issue 01 + 02 关闭
9/7-9/8 (周一二)  ────  Issue 03 全部 sub-ticket PR merged
9/9 (周三)        ────  Issue 04 ADR merged
9/10 (周四) W3 Day 1 ────  B17/B19/B25/B22 建仓
9/13 (周六)       ────  Issue 05 iOS mock decoder merged + 跨仓空跑通过
9/14 (周日)       ────  本 Issue 06 准备 checklist, 邀请 iOS Lead + 后端 TL
9/15 (周一) 23:59 ────  本 Issue 06 关闭 = V2.0 协议封板 🎉
```