# W3 Protocol Freeze — 协议冻结 6 P0 Issue 拆解(Scratch)

**日期**: 2026-09-06(晚场评审 V2.0 产出)
**作者**: backend planning
**目的**: 基于 `docs/30_技术方案/79_Backend架构Review_辩证分析与执行校准_2026-09-06.md` §九 行动清单,把协议冻结相关 P0 项切成 6 个 atomic GitHub Issue,落库到本地 `.scratch/issues/2026-09-06-W3-protocol-freeze/`,**待人工 review 后再 gh CLI 建仓**。

## Why Scratch(沿用 backend 仓现有约定)

- ✅ 避免 gh CLI 误建后回滚麻烦
- ✅ review 完整 issue 内容(尤其 dependency 关系)后再批量创建
- ✅ 一份文件既是 plan, 又是 issue body 模板
- ✅ 与 W3-backend-tickets 拆解模式对齐(scratch → gh CLI → close)

## 6 个 P0 Issue 总览

| # | Issue | 类型 | 截止 | 仓 |
|---|---|---|---|---|
| 01 | 协议冻结会议召集 + WSS V2.0 帧字段草稿输出 | 会议任务 | 9/6 今晚 | backend |
| 02 | sessionRuntime 结构体加一行注释(非代码改动) | doc-only | 9/6 今晚 | backend |
| 03 | WSS V2.0 `ai.tts.start/audio/end` 帧协议 Markdown 草稿 | master + 3 sub | 9/8 | backend |
| 04 | seed-tts-2.0 SDK 决策评审(Opus 解码方案) | 评审任务 | 9/9 | backend |
| 05 | iOS 端 `ai.tts.*` mock decoder 接入 | iOS feat | 9/13 | **ios**(backend 同步跟踪) |
| 06 | WSS V2.0 协议封板 checklist + 跨仓空跑验证 | gate 验收 | **9/15** | backend+ios 联动 |

## 目录约定

```
.scratch/issues/2026-09-06-W3-protocol-freeze/
├── README.md                # 本文件
├── INDEX.md                 # 6 issue 索引
├── 01-protocol-freeze-meeting.md
├── 02-sessionruntime-comment.md
├── 03-wss-v2-frames-markdown.md   # master + 3 sub-ticket
├── 04-seed-tts-sdk-decision.md
├── 05-ios-mock-decoder.md         # 跨仓,主仓 ios
└── 06-wss-v2-protocol-freeze-gate.md
```

## 优先级与依赖关系

```
9/6 今晚:
  Issue 01 (会议) ─┬─▶ Issue 02 (注释) ───────────────────────┐
                    └─▶ Issue 03 master 启动                 │
                                                                │
9/7-9/9:                                                   │
  Issue 03 sub-1 (ai.tts.start) ──┐                       │
  Issue 03 sub-2 (ai.tts.audio) ──┼──▶ Issue 04 (SDK 决策) ◀┘
  Issue 03 sub-3 (ai.tts.end)   ──┘                       │
                                                           │
9/10-9/13:                                                  │
  Issue 04 评审产出 ─▶ Issue 05 (iOS mock decoder 接入)  ───┤
                                                           │
9/14-9/15:                                                  │
  Issue 05 验证 ─▶ Issue 06 (协议封板 gate, 唯一硬死线) ◀──┘
```

**关键依赖**:
- Issue 02 必须在评审通过的同一个 PR 内合入(注释 + 评审 doc 同步)
- Issue 03 的 3 个 sub-ticket 必须在 Issue 04 评审前完成(否则评审无依据)
- Issue 06 是唯一硬死线,9/15 后任何 WSS V2.0 帧字段改动走 CCB

## Skills-to-Ticket 切分原则(沿用)

1. **每个 ticket 独立可 shippable** —— 单独 PR, 单独 merge, 单独回滚
2. **acceptance criteria 量化** —— 用 Go test name + 帧字段值, 不写"做得不错"
3. **依赖显式映射** —— ticket body 写清 `Blocked by #X` 或 `depends on #Y`
4. **跨仓 Issue** —— 在主仓建仓, 关联仓加 reference(Issue 05: ios 主仓, backend 加引用)

## 后续批量建仓(评审通过后)

```bash
# Phase 1: 在 backend 仓建仓 01, 02, 03, 04, 06
gh issue create --title "P0-PROTO-01: 协议冻结会议召集..." \
  --label "v2.0-blocker,priority/P0,protocol-freeze" \
  --milestone "V2.0 W3" \
  --body-file .scratch/issues/2026-09-06-W3-protocol-freeze/01-protocol-freeze-meeting.md
# (其余见 create-protocol-freeze-issues.sh, 待评审通过后生成)

# Phase 2: Issue 03 master 建仓后, 立即建 3 个 sub-ticket
gh issue create --title "P0-PROTO-03.1: ai.tts.start 帧字段定义..." ...

# Phase 3: Issue 05 在 ios 仓建仓, backend 仓加交叉引用评论
cd ../fluentwork-ios && gh issue create ...
```

## 与现有 W3 tickets 的关系

| 范畴 | Scratch 目录 | 关系 |
|---|---|---|
| B17 / B25 / B19 / B22 等业务 Issue | `.scratch/issues/2026-09-06-W3-backend-tickets/` | 已有, 不重叠 |
| 协议冻结 P0 准备 | `.scratch/issues/2026-09-06-W3-protocol-freeze/` (本目录) | 新增, 是 B17 落地的**前置依赖** |

**关键不重叠**: B17 的 T-TTS-1..5 是「TTS Provider 代码实现」,本目录的 Issue 03 是「WSS V2.0 ai.tts.* 帧协议文档」, 两者是文档 vs 代码的上下游关系, 不重复。

## 状态机(已落 GitHub)

```
✅ P0-PROTO-01 (backend #101) ──── 会议决议已产出 → CLOSED
✅ P0-PROTO-02 (backend #100) ──── 注释极简 → CLOSED
🔵 P0-PROTO-03 master (backend #99)
   ├─ 🔵 03.1 (backend #104) ──── 无依赖,可立即启动
   ├─ 🔵 03.2 (backend #105) ──── Blocked by #104
   ├─ 🔵 03.3 (backend #108) ──── Blocked by #105
   └─ 🔵 03.4 (backend #103) ──── 无依赖,可与 #104 并行
🔵 P0-PROTO-04 (backend #98) ──── 9/9 ADR, codec=opus 已预决议
🔵 P0-PROTO-05 主 (ios #47) + 跟踪 (backend #109) ──── 9/13 跨仓空跑
🔵 P0-PROTO-06 (backend #102) ──── 9/15 唯一硬死线
```

## 修订记录

| 版本 | 日期 | 修改 |
|---|---|---|
| V1.0 | 2026-09-06 晚场 | 初版; 基于 79_ V2.0 §九 拆出 6 P0 Issue |