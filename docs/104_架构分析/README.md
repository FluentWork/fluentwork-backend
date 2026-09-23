# fluentwork-backend 架构分析（系列）

**日期**：2026-09-23
**分析基线**：`c23cc38`（`main`）
**范围**：整个 Go 仓 —— 199 个生产文件 / 149 个测试文件、37679 行生产代码、29055 行测试代码、22 个 `internal/` 包、25 个 `cmd/`
**不包含**：产品设计；iOS 客户端（另见 `fluentwork-ios/docs/80_架构分析/`）

---

## 这份系列是什么

一份**读代码得出的**架构分析。它回答三个问题：哪些是好的、哪些是坏的、哪里有真问题。

### 与 `docs/103_tts_wss_architecture_audit/` 的分工

**先说清楚，因为这两份文档会打架。**

| | `103_tts_wss_architecture_audit/` | `104_架构分析/` |
|---|---|---|
| 对象 | **`internal/voicegateway` 的 TTS/WSS 一条链路** | **整个仓**：22 个包、数据层、配置、测试与工程纪律 |
| 结论 | 架构评级 **A-（90/100）优秀，可生产**；M1–M6 全部落地，9/9 结构问题关闭 | 见下面的摘要 —— **其中若干条 `103_` 没有覆盖，因为不在它的范围内** |
| 形式 | 里程碑验收报告（对照 `94_`–`99_` 设计） | 按架构轴分章的分析 |

**它们不矛盾，但读的时候要知道边界**：`103_` 的「A-」是对**语音链路内部**的评价，它审的是「`94_`–`99_` 那六个里程碑做到没有」。本系列审的是「这个仓作为整体，结构上哪里会出事」。同一个仓可以「语音链路 A-」而「迁移机制没有版本表」——两句话都对。

**本系列不重复 `103_` 已定案的结论**（切帧层统一、`AITTSAudio.Encode` 唯一编码位置、`SeqAllocator`、救援路径与 AI 路径为何不合并流控制），只引用。

### 与 iOS 系列的对照

`fluentwork-ios/docs/80_架构分析/` 是同一次工作的客户端一半。两侧**故意用了同一套方法**，所以跨仓的结论可以直接并排读。几处值得对照的：

| 形状 | iOS | backend |
|---|---|---|
| 「未知帧要宽容」 | `URLSessionSocketTransport.swift:316-332` | `handler_control.go:465-486` —— **两侧独立选了同一策略，且都写了理由** |
| 「路由表要能断言」 | `TransportRoutingEquivalenceTests.swift:69-98` 字典 + 生产工厂驱动 | `ProviderErrorPolicies()` 导出失败策略表供测试断言 |
| 「判据必须真的能红」 | 8 份 `waitUntil` 已漂移（`80_/04` §3.1） | `check-defect-discipline.sh` 主动修掉了「永远通过」的写法 |
| 「同一处定义被抄成两份」 | `pcmBuffer` 不变量无测试保护 | `materials` 的 refine 没有 lease，而**同仓的 `session_jobs` 有** |

## 方法

1. **先读代码，再下判断。** 每一条结论后面跟 `文件:行`，可当场复核。
2. **标确定性。** 分三档：
   - **【实测】** —— 有命令输出或测试输出
   - **【读码】** —— 从源码直接读出，未运行
   - **【推断】** —— 从读到的推出来，可能有别的解释
3. **拆开再判。** 同一个现象常捆着几件成立程度不同的事，逐条给结论。
4. **不评价风格。** 「我不喜欢」不是发现；「这会让 X 发生而没有人会知道」是。

---

## 结论摘要

### 好的

| # | 结论 | 证据 | 确定性 |
|---|---|---|---|
| G1 | **依赖图无环，分层成立。** 22 个 `internal/` 包、85 条内部依赖边、**0 个环** | `104_/01` §2 | 【实测】 |
| G2 | **架构纪律是机器执行的，不是口号。** `depguard` 规则**带理由**（`desc:` 写清了为什么），违反即 `dev-check.sh` 变红 | `.golangci.yml` | 【实测】 |
| G3 | **失败策略是一张表，且「沉默」必须给理由。** `silentBecause` 对沉默是**必填**；表外的帧落到 provider 时**默认播报**（fail-loud） | `handler_control.go:37-98`、`:171-178` | 【读码】 |
| G4 | **缺陷纪律检查知道自己查不了什么。** 脚本明说「检查不了内容是否诚实」，并且修掉了一个会让它**永远通过**的写法（`-z`） | `check-defect-discipline.sh:8,18-23` | 【读码】 |
| G5 | **`session_jobs` 是一个带租约的持久化工作队列。** `FOR UPDATE` 抢占 + `locked_at`/`locked_by`/`attempts` + 过期租约回收，且 memory 替身**镜像同一语义** | `session/mysql_store.go:430-447`、`types.go:40` | 【读码】 |
| G6 | **refine 的幂等是状态守卫 + 数据库 CAS。** `!= queued` 即 no-op；`MarkProcessing` 冲突视为「别人赢了」；所有错误路径都归到带 code 的 `failed` | `materials/refiner.go:42-51,90-96` | 【读码】 |
| G7 | **注释记录的是「为什么」，包括被推翻的选项和事故日期。** 例：`RescueEnabled` 为什么默认关、`defaultWriteTimeout` 为什么关整条连接 | `config.go:16-49,57-65` | 【读码】 |
| G8 | **未知控制帧忽略 + 计数 + 日志，且写明了为什么不能回错误** | `handler_control.go:465-486` | 【读码】 |

### 不好的

| # | 结论 | 证据 | 确定性 |
|---|---|---|---|
| B1 | **`voicegateway` 是 71 个文件的扁平包**，无子目录；7133 行生产 + 11118 行测试 | `find`/`wc` | 【实测】 |
| B2 | **控制帧分派是线性扫描，且同一个帧的 JSON 被解码最多 8 次**（1 次分派 + 7 个 handler 各 1 次） | `handler_control.go:108,200,223,308,361,397,416,458` | 【实测】 |
| B3 | **迁移没有版本表。** 26 个 up / 4 个 down，**全仓 0 处提及 `schema_migrations`**；应用靠 **5 份** shell 循环副本 | `migrations/`、`scripts/*.sh` | 【实测】 |
| B4 | **`migrations` 包 embed 了 SQL，但没有任何包 import 它** —— 它只喂自己的测试 | `grep` 全仓 0 命中 | 【实测】 |
| B5 | **`/metrics` 是 7 个包手写文本的字符串拼接**，无 registry、无重名检测 | `httpserver/server.go:179-184` | 【实测】 |
| B6 | **`httpserver.New` 有 13 个位置参数**，且路由表不是静态可知的（nil 决定挂不挂） | `httpserver/server.go:56-129` | 【读码】 |
| B7 | **0 个 benchmark、0 个 fuzz。** 而仓里有明确的每帧热路径 | `grep` | 【实测】 |
| B8 | **`httpjson` / `apierr` 零测试**，而 `httpjson.Error` 是**每个 API 错误的唯一出口**且有 4 个分支 | `httpjson/httpjson.go:31-57` | 【实测】 |
| B9 | **`test/` 是空目录**（只有 `.gitkeep`） | `ls` | 【实测】 |
| B10 | **`materials` 的 refine 是无人 join 的进程内 goroutine**，而 `materials` 表**没有 lease 需要的列** —— 同仓的 `session_jobs` 有 | `materials/service.go:81-88`、`migrations/0014` vs `0005` | 【实测】 |
| B11 | **`topic_cards` 的幂等是「先读后跳」，没有唯一键**；而调度器的「今天跑过没有」只在内存里 | `topic/generator.go:61-76`、`0015` | 【读码】 |

### 有问题的

| # | 问题 | 影响 | 证据 |
|---|---|---|---|
| P1 | **`ai.audio.chunk` 声明了常量，全仓 0 处使用**（无生产者） | 与 iOS D11 是同一件事的两侧 | `voiceproto/frames.go:21`；`grep` 0 命中 |
| P2 | **二进制帧不带 `turn_id`**，`[4B seq][payload]` 冻结 | 这是 iOS D7 的根因；**唯一需要跨仓决策的改动** | `voiceproto/frames.go:195-215` |
| P3 | **`UplinkChunkBytes` 在两个包各有一份 640**，`voicegateway` 那份的注释承认这是分层妥协 | 改一处不会让另一处红 | `uplink_constants.go:12` vs `voiceduplex/volc_duplex.go:958` |
| P4 | **音频热路径上有一句逐帧 `Debug` 日志** | 每帧一次调用 + 变参切片分配 | `voicegateway/handler.go:566` |
| P5 | **`AGENTS.md` 的架构图把 `internal/corpus/` 写成 `internal/corpuss/`** | 照图找目录会找不到 | `AGENTS.md:22` |
| P6 | **`golangci-lint` 本机未安装**，`depguard` 那条纪律在本机跑不起来 | 纪律只在装了工具的地方生效 | `dev-check.sh:18`；`which` 无输出 |

## 章节

| # | 文档 | 回答什么 |
|---|---|---|
| 01 | [模块划分与依赖方向](./01_模块划分与依赖方向.md) | 22 个包的分层成立吗？纪律靠什么维持？ |
| 02 | [语音网关](./02_语音网关.md) | 帧怎么进来、怎么分派、失败怎么处理？与 iOS 两侧是否对称？ |
| 03 | [数据与配置](./03_数据与配置.md) | 迁移、工作队列、状态机、配置面 |
| 04 | [测试与工程纪律](./04_测试与工程纪律.md) | 746 条测试测的是什么高度？纪律有多少是机器执行的？ |
| 05 | [问题清单与建议](./05_问题清单与建议.md) | 按严重度排序，每条给动作与是否需要决策 |

## 阅读前提

默认读者已了解：Go 1.26（`log/slog`、`embed`、`context`）、`gin`、`coder/websocket`、MySQL 8。

若只关心 TTS/WSS 一条链路，先读 [`docs/103_tts_wss_architecture_audit/07_refactoring_status_summary.md`](../103_tts_wss_architecture_audit/07_refactoring_status_summary.md)。
