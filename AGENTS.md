# FluentWork Backend - Agent 协作指南

## 仓库信息

- **仓库名**: `fluentwork-backend`
- **语言**: Go
- **角色**: FluentWork 核心服务 — 语音网关、Session 管理、语料库、Badge 检测

## 架构概览

```
┌─────────────────────────────────────────────────────────────────┐
│                    fluentwork-backend (Go)                       │
├─────────────────────────────────────────────────────────────────┤
│  cmd/voice-gateway/     — WSS 语音网关服务 (Port 8081)          │
│  cmd/app-server/       — 主业务服务 (Port 8080)                │
│  internal/voicegateway/— 语音网关核心 (Handler, Provider)        │
│  internal/voiceproto/  — WSS 帧协议定义                        │
│  internal/voicepoc/    — 火山引擎双工会话                      │
│  internal/session/      — Session 生命周期管理                   │
│  internal/corpuss/     — 语料库服务                            │
│  pkg/logx/             — 结构化日志                            │
└─────────────────────────────────────────────────────────────────┘
```

## 核心模块

### Voice Gateway (WSS)

**关键文件**:
- `internal/voicegateway/handler.go` — WSS 连接处理、帧分发、`sessionRuntime` 状态机
- `internal/voicegateway/provider.go` — `VoiceProvider` / `VoiceProviderSession` 接口
- `internal/voicegateway/provider_volc_duplex.go` — 火山引擎生产 Provider
- `internal/voicegateway/provider_dev_echo.go` — 本地开发 Provider (含 PCM fixture)

**Provider 类型**:
1. `MockVoiceProvider` — 测试用
2. `DevEchoVoiceProvider` — 本地开发，含 EchoText + 可选 PCM fixture
3. `VolcDuplexProvider` — 生产环境

**关键特性**:
- `rt.broken` — 音频转发失败后静默丢弃后续帧
- `rt.reopenAttempted` — #43 reopen-once 行为
- `logWarn` — Warn 去重（5s 窗口 + 每 10 次汇总）

### 帧协议 (voiceproto)

**关键帧类型**:
| 类型 | 方向 | 描述 |
|------|------|------|
| `auth` | C→S | WSS 认证 |
| `session.ready` | S→C | Session 就绪 |
| `session.start` | C→S | 启动语音会话 |
| `user.speech.start` | C→S | 用户开始说话 |
| `user.speech.end` | C→S | 用户结束说话（含 ASR text） |
| `ai.text.delta` | S→C | AI 文本增量 |
| `ai.turn.end` | S→C | AI Turn 结束（含 outcome） |
| `client.asr.transcription` | S→C | 服务器 ASR 中继 |
| `error` | S→C | 错误帧 |
| `feedback.badge` | S→C | Badge 命中通知 |

## 开发约定

### 1. 测试运行

```bash
# 运行所有测试
cd fluentwork-backend && go test ./...

# 运行 voicegateway 测试
go test ./internal/voicegateway/... -v

# 运行特定测试
go test ./internal/voicegateway/... -run "TestHandler_AudioMarksSessionBroken"

# 并行测试（部分测试可能受并行影响）
go test ./... -p 1
```

### 2. 本地启动

```bash
# 轻量级本地启动（无 Docker）
./scripts/dev-up.sh

# 完整本地栈（MySQL + Redis + 后端服务）
./scripts/dev-stack.zsh

# 开发检查（必须运行）
./scripts/dev-check.sh
```

### 3. 代码规范

- **错误处理**: 使用 sentinel errors 或自定义 error types
- **日志**: 使用 `pkg/logx` 进行结构化日志
- **测试**: 每个 Package 至少要有 `_test.go` 文件
- **并发**: 注意 `atomic` 包的正确使用
- **任务串行**: 任一时刻只实施、测试和推进一个 ticket；完成当前 ticket 的验证后才能开始下一个
- **跨仓串行**: 涉及 backend 与 iOS 的工作不得并行推进；必须先完成并验证当前仓任务，再切换到另一仓库

### 4. Commit 规范

```
<type>(<scope>): <description>

Types:
  - feat: 新功能
  - fix: Bug 修复
  - docs: 文档更新
  - test: 测试更新
  - refactor: 重构
  - perf: 性能优化

Examples:
  feat(voicegateway): add reopen-once behavior for audio failures
  fix(handler): return error after provider write failure (B15 Item 1.2)
  test(provider_dev_echo): add PCM fixture E2E tests
```

### 5. Git 工作流

1. **直接在 `main` 上开发并 push**，默认不创建 feature branch / PR / MR
2. **门禁**：`go test ./...` + `go build ./...`（本地 `./scripts/dev-check.sh`）。GitHub 在 push 到 `main` 后跑 `go-build-and-test`，不要求 PR
3. 只有用户明确要求时才开 PR
4. 不把 gstack review 当作提交或合并门禁
5. **实现说明与代码一并提交**：门禁通过后必须新增 `docs/NN_<ticket>_实现说明.md`（原理、方案、若修 bug 则写根因、新方案理由、门禁证据），并在下方「最近实现说明」登记。不要把长文堆进本文件正文。

## 最近实现说明

| 文档 | 任务 | 提交 |
|------|------|------|
| `docs/26_B24_历史回顾API_实现说明.md` | B24 会话列表/详情 | `d90d4e4` |
| `docs/27_B21_素材模块_实现说明.md` | B21 素材创建与提炼 | `37d3614` |
| `docs/28_B23_话题卡_实现说明.md` | B23 每日话题卡与打卡 | `ffd585f` |
| `docs/29_B17_TTS_Provider_实现说明.md` | B17 TTS Provider 与 fallback | `8d28c9b` |
| `docs/30_B8_评价炼化Worker_实现说明.md` | B8 review worker 评价与炼化 | `fb779e3` |
| `docs/31_B15_离线评估集_实现说明.md` | 第二波 B15 Prompt 回归（#28） | (本提交) |

## 关键 Issue 追踪

| Issue | 描述 | 状态 |
|-------|------|------|
| B12 | Badge Emit 修复 | ✅ 完成 |
| B13 | Client ASR Relay | ✅ 完成 |
| B14 | T3/T4 注入生效 | ✅ 完成 |
| B15 | Turn Timeout & Session Exit | 🔄 实施中 |
| I20 | 全链路收口 | 🔄 实施中 |

## 高风险路径

以下代码修改需要格外小心：

1. **Voice Gateway Handler** — `sessionRuntime` 状态机逻辑
2. **Provider 接口** — 改动可能影响所有 Provider 实现
3. **voiceproto 帧定义** — 协议变更需要 iOS 同步
4. **数据库迁移** — 不可逆操作

## 相关仓库

- **iOS**: `fluentwork-ios` — SwiftUI 应用，语音会话客户端
- **Meta**: `fluentwork-meta` — 项目元数据、文档、治理规则

## 文档索引

| 文档 | 描述 |
|------|------|
| `docs/00_开发入口与第一波范围.md` | 项目启动和第一波范围 |
| `docs/02_第二波开发范围与任务清单.md` | 第二波开发任务 |
| `docs/23_B15_Turn_Timeout_And_Session_Exit.md` | B15 Issue 详情 |
| `docs/i20-fix-plan.md` | I20 全链路收口计划 |
| `docs/26_B24_历史回顾API_实现说明.md` | B24 历史回顾 API 实现说明 |
| `docs/27_B21_素材模块_实现说明.md` | B21 素材模块实现说明 |
| `docs/28_B23_话题卡_实现说明.md` | B23 话题卡实现说明 |
| `docs/29_B17_TTS_Provider_实现说明.md` | B17 TTS Provider 实现说明 |
| `docs/30_B8_评价炼化Worker_实现说明.md` | B8 评价炼化 Worker 实现说明 |
| `docs/31_B15_离线评估集_实现说明.md` | 第二波 B15 离线评估集（#28） |
| `AGENTS.md` | Agent 协作策略 |
| `CLAUDE.md` | 本文件，Agent 上下文指南 |
