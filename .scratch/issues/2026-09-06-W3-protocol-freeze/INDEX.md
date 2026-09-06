# W3 Protocol Freeze Issue 索引(2026-09-06)

## 1. 6 Issue 总览

| # | Issue | GitHub | 优先级 | 截止 | 工时估 | 仓 | 关联依赖 |
|---|---|---|---|---|---|---|---|
| **01** | 协议冻结会议召集 + WSS V2.0 帧字段草稿输出 | **backend #101** ✅ CLOSED | P0 | **9/6 今晚** | 0.5h(会议) + 1h(产出) | backend | 无 |
| **02** | sessionRuntime 结构体加并发注释 | **backend #100** ✅ CLOSED | P0 | 9/6 今晚 | 0.1h(注释) + 0.1h(PR) | backend | Issue 01 决议 |
| **03** | WSS V2.0 `ai.tts.*` 帧协议 Markdown 草稿(master) | **backend #99** 🔵 OPEN | P0 | 9/8 | 4h | backend | Issue 01 |
| 03.1 | └ sub: ai.tts.start 帧字段定义 | **backend #104** 🔵 OPEN | P0 | 9/7 | 1h | backend | Issue 03 master |
| 03.2 | └ sub: ai.tts.audio 帧字段定义 | **backend #105** 🔵 OPEN | P0 | 9/7 | 1.5h | backend | Issue 03 master |
| 03.3 | └ sub: ai.tts.end 帧字段定义 | **backend #108** 🔵 OPEN | P0 | 9/7 | 0.5h | backend | Issue 03 master |
| 03.4 | └ sub: 50_ server_ts_ms 决策落地 | **backend #103** 🔵 OPEN | P0 | 9/8 | 1h | meta 仓 | Issue 03 master |
| **04** | seed-tts-2.0 SDK 决策评审(Opus 解码方案) | **backend #98** 🔵 OPEN | P0 | **9/9** | 2h(评审) + 1h(ADR) | backend | Issue 03 全 sub |
| **05** | iOS 端 `ai.tts.*` mock decoder 接入 | **ios #47** 🔵 OPEN | P0 | **9/13** | 1 dev-day | **ios** | Issue 04 决策产出 |
| 05T | └ backend 仓 tracking 副本 | **backend #109** 🔵 OPEN | P0 | 9/13 | (无独立工时) | backend | ios #47 |
| **06** | WSS V2.0 协议封板 checklist + 跨仓空跑验证 | **backend #102** 🔵 OPEN | P0 | **9/15** | 0.5 dev-day | backend+ios | Issue 05 + Issue 03 |

**总工时估**: ~3 dev-day(分散在 9/6 - 9/15, 共 9 天窗口)

## 2. 文件 → Issue 映射

| 文件 | Issue | 内容 |
|---|---|---|
| `01-protocol-freeze-meeting.md` | #01 | 会议议程 + 产出 checklist + gh 落库命令 |
| `02-sessionruntime-comment.md` | #02 | sessionRuntime 加 1 行注释的 Go diff + PR checklist |
| `03-wss-v2-frames-markdown.md` | #03 + 4 sub | master 验收 + 3 个 ai.tts.* 子帧定义 + server_ts_ms 决策 |
| `04-seed-tts-sdk-decision.md` | #04 | Opus 解码方案对比 + ADR-0073 模板 |
| `05-ios-mock-decoder.md` | #05 | iOS 端 Swift 接入设计 + 跨仓引用 |
| `06-wss-v2-protocol-freeze-gate.md` | #06 | 封板 checklist + 跨仓空跑验证 SOP |

## 3. 关键时间窗口(不可滑动)

```
9/6 (今天, 周日) 23:59  ──  Issue 01 + 02 必须今晚关闭
9/8 (周二)              ──  Issue 03 全部 sub-ticket 完成
9/9 (周三) 18:00        ──  Issue 04 评审会召开, ADR 落地
9/13 (周六) 18:00       ──  Issue 05 iOS mock decoder 接入完成, 跨仓空跑通过
9/15 (周一) 23:59       ──  Issue 06 协议封板, CCB 启动
```

**红线**: 9/15 之后任何 WSS V2.0 帧字段变更走 CCB(架构变更委员会),预估 2 人天/次。

## 4. 状态机追踪(已落 GitHub)

| Issue | 本地编号 | GitHub | 状态 | 备注 |
|---|---|---|---|---|
| 01 | 🟢 | **backend #101** | ✅ CLOSED | 会议决议已产出,见 `scratch/wss-v2-frames-2026-09-06.md` |
| 02 | 🟢 | **backend #100** | ✅ CLOSED | 注释极简,无需独立跟踪 |
| 03 master | 🟢 | **backend #99** | 🔵 OPEN | sub-tickets 启动顺序已在评论中明确 |
| 03.1 | 🟢 | **backend #104** | 🔵 OPEN | 无依赖,可立即启动 |
| 03.2 | 🟢 | **backend #105** | 🔵 OPEN | Blocked by #104 |
| 03.3 | 🟢 | **backend #108** | 🔵 OPEN | Blocked by #105 |
| 03.4 | 🟢 | **backend #103** | 🔵 OPEN | 无依赖,可与 #104 并行 |
| 04 | 🟢 | **backend #98** | 🔵 OPEN | 9/9 评审, codec=opus 已在评论中预决议 |
| 05 (主) | 🟢 | **ios #47** | 🔵 OPEN | 9/13 跨仓空跑,会议决议已评论同步 |
| 05 (跟踪) | 🟢 | **backend #109** | 🔵 OPEN | cross-repo tracking |
| 06 | 🟢 | **backend #102** | 🔵 OPEN | 9/15 唯一硬死线,gate checklist 输入已具备 |

**已知 issue 历史**:
- backend #107 (重复的 03.3): CLOSED (GraphQL 重试导致重复创建)
- backend #98 (P0-PROTO-04): 9/6 晚误关后已 reopen

## 5. 与 W3 业务 tickets 的依赖

```
本目录(P0 准备):
  Issue 03 (帧协议) ──▶ Issue 04 (SDK 决策) ──▶ Issue 05 (iOS decoder) ──▶ Issue 06 (封板)
                                                          │
                                                          ▼
W3-backend-tickets(业务开发):
  B17 (TTS Provider) 必须在 Issue 06 之后才能开始 T-TTS-2 流式 chunk 实现
  因为帧字段不冻结,B17 实现的 ai.tts.audio 流式 chunk 与 iOS decoder 不兼容
```

**结论**: 业务 Issue(B17) 的 T-TTS-2 启动条件 = Issue 06 通过。

## 6. 评审 checklist(review 本目录时使用)

- [ ] 6 个 Issue 标题是否清晰(用户能在 GitHub 项目看板一眼识别优先级)?
- [ ] Issue 01 会议议程是否覆盖 iOS Lead + 后端 TL 共同决策项?
- [ ] Issue 03 的 3 个 sub-ticket 字段定义是否与 79_ §四 一致?
- [ ] Issue 04 决策选项是否覆盖 Opus 解码主流方案(libopus / opus-codec / 自实现)?
- [ ] Issue 05 iOS 端代码量评估是否合理(< 1 dev-day)?
- [ ] Issue 06 封板 checklist 是否包含跨仓空跑 SOP?
- [ ] 跨仓依赖(Issue 05 在 ios 仓)是否标注清楚?
- [ ] acceptance criteria 是否量化(每个都有 PASS 标准)?