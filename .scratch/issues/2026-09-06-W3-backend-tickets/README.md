# W3 Backend Tickets — Skills to Ticket (Scratch)

**日期**: 2026-09-06  
**作者**: backend planning  
**目的**: 按 Matt Pocock skills-to-ticket 方法论,把 V2.0 W3 backend 启动包切成 atomic sub-tickets,落库到本地 `.scratch/issues/2026-09-06-W3-backend-tickets/`,**待人工 review 后再 gh CLI 建仓**。

## Why Scratch

- ✅ 避免 gh CLI 误建后回滚麻烦
- ✅ review 完整 issue 内容(尤其 dependency 关系)后再批量创建
- ✅ 一份文件既是 plan, 又是 issue body 模板
- ✅ 每次 sprint 可复用 `.scratch/issues/<date>-<topic>/` 模式归档

## 目录约定

```
.scratch/issues/2026-09-06-W3-backend-tickets/
├── README.md                      # 本文件
├── INDEX.md                       # skills → tickets 总览表
├── 00-close-plans.md              # #43 / #42 close rationale
├── 01-skill-28-eval-dataset.md    # #28 + 4 sub-tickets
├── 02-skill-21-review-worker.md   # #21 + 5 sub-tickets
├── 03-skill-B17-TTS-Provider.md   # B17 + 5 sub-tickets (master + sub)
├── 04-skill-B19-B7-hit-injection.md   # B19 + 4 sub-tickets
└── 05-skill-B25-F3-pin-favorite.md    # B25 + 4 sub-tickets
```

## Skills-to-Ticket 切分原则

1. **每个 skill 切成 atomic ticket** —— 1 master + N sub
2. **每个 ticket ≤ 0.5 dev-day** —— 原启动包估的 1-2 dev-day 是 skill 级别,必须再切
3. **每个 ticket 独立可 shippable** —— 单独 PR,单独 merge,单独回滚
4. **acceptance criteria 量化** —— 用 Go test name + 性能数字,不写"做得不错"
5. **依赖显式映射** —— ticket body 写清 `Blocked by #X` 或 `depends on T-XXX-Y`

## Sub-ticket 命名

- **已存在 issue 的拆解**(`#28` / `#21`):`T-<SKILL>-<N>` 格式
- **新建 master issue 的拆解**(`B17` / `B19` / `B25`):`T-<SKILL>-<N>` 格式

## 使用流程

1. 本次 review 全部 6 个文件
2. 确认 ticket 数 / 依赖 / 工时
3. 一键 `gh` 批量建仓(参考 INDEX.md §「GitHub 落库命令」)
4. 建仓完成后回到本目录,把每个文件的「GitHub 状态」从 `🟡 草稿` 改为 `🟢 #XXX`
5. W3 结束归档 `.scratch/issues/2026-09-06-W3-backend-tickets/` 到 `docs/40_研发流程与协作/75_W3_启动日_Issue_落库清单_2026-09-10.md`(待建)

## 修订记录

| 版本 | 日期 | 修改 |
|---|---|---|
| V1.0 | 2026-09-06 | 初版;5 个 skill + 2 个 close plan 共 22 sub-ticket |
