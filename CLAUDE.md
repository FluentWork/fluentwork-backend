# FluentWork Backend — CLAUDE.md

> 本文件是 Agent 的主要上下文文档。阅读顺序：先 CLAUDE.md → 再 AGENTS.md → 最后具体模块文档。

## 仓库角色

`fluentwork-backend` 是 FluentWork 的 Go 服务仓库，负责：
- **语音网关** (Voice Gateway) — WSS 服务，处理 iOS 语音会话
- **Session 管理** — 用户语音会话生命周期
- **语料库** (Corpus) — 短语块管理和 Badge 检测
- **后台 Worker** — 异步任务处理

## 核心架构

### 语音网关 (Voice Gateway)

```
iOS App ──WSS──► voice-gateway:8081 ──► VolcEngine (生产)
                         │
                         └──► DevEchoProvider (本地开发)
```

**关键组件**:
- `handler.go` — WSS 连接处理、`sessionRuntime` 状态机
- `provider.go` — Provider 接口定义
- `provider_volc_duplex.go` — 火山引擎双工 Provider
- `provider_dev_echo.go` — 本地开发 Provider

**状态机关键字段**:
- `rt.broken` — 音频转发失败后静默丢弃
- `rt.reopenAttempted` — reopen-once 标志
- `rt.warnDedup` — Warn 去重状态

### 帧协议

所有 WSS 帧定义在 `internal/voiceproto/frames.go`：

| 帧类型 | 方向 | 说明 |
|--------|------|------|
| `auth` | C→S | 认证 |
| `session.start` | C→S | 启动会话 |
| `user.speech.end` | C→S | 用户结束说话 |
| `ai.turn.end` | S→C | AI Turn 结束（含 outcome） |
| `client.asr.transcription` | S→C | ASR 中继 |
| `feedback.badge` | S→C | Badge 命中 |
| `error` | S→C | 错误 |

## 开发入门

### 1. 环境准备

```bash
# 克隆仓库
git clone https://github.com/FluentWork/fluentwork-backend.git
cd fluentwork-backend

# 安装依赖
go mod download

# 启动本地服务
./scripts/dev-up.sh
```

### 2. 运行测试

```bash
# 所有测试
go test ./...

# voicegateway 模块
go test ./internal/voicegateway/...

# 带日志输出
go test ./... -v -count=1
```

### 3. 本地服务

```bash
# 轻量启动（推荐）
./scripts/dev-up.sh

# 完整栈（需 Docker）
./scripts/dev-stack.zsh

# 开发质量检查
./scripts/dev-check.sh
```

## 关键规范

### 测试规范

1. **每个 Package 都有 `_test.go`**
2. **测试命名**: `Test<Subject>_<Scenario>`
3. **集成测试**: `Test<Subject>_E2E`
4. **并行安全**: 注意 `t.Parallel()` 的使用

### 代码规范

1. **错误处理**: 使用 sentinel errors
2. **日志**: 使用 `pkg/logx` 结构化日志
3. **Context**: 传递 `context.Context`
4. **并发**: 使用 `atomic` 包或 `sync.Mutex`

### 提交规范

```
<type>(<scope>): <description>

feat(voicegateway): add warn deduplication
fix(handler): return error on provider write failure
test(provider): add B15 regression tests
```

## 高风险区域

修改以下代码前需要额外审查：

1. `internal/voicegateway/handler.go` — 状态机逻辑
2. `internal/voiceproto/frames.go` — 协议定义（影响 iOS）
3. 数据库迁移文件
4. 生产配置

## 相关资源

### 内部文档

- `AGENTS.md` — Agent 协作策略（必读）
- `docs/00_开发入口与第一波范围.md` — 项目启动
- `docs/02_第二波开发范围与任务清单.md` — 开发任务
- `docs/23_B15_Turn_Timeout_And_Session_Exit.md` — B15 Issue
- `docs/i20-fix-plan.md` — I20 收口计划

### 外部依赖

- **iOS**: `fluentwork-ios` — SwiftUI 客户端
- **Meta**: `fluentwork-meta` — 项目治理和文档

## 环境变量

### Voice Gateway

| 变量 | 描述 | 默认值 |
|------|------|--------|
| `VOICE_GATEWAY_PROVIDER` | Provider 类型 (`mock`/`dev-echo`/`volc-duplex`) | `mock` |
| `VOICE_DEV_ECHO_TEXT` | DevEcho Provider 的回显文本 | `""` |
| `VOICE_DEV_ECHO_FIXTURE` | DevEcho PCM fixture 文件路径 | `""` |
| `VOLC_SPEECH_API_KEY` | 火山引擎 API Key | `""` |
| `VOLC_SPEECH_APP_ID` | 火山引擎 App ID | `""` |

### App Server

| 变量 | 描述 |
|------|------|
| `DATABASE_URL` | MySQL 连接字符串 |
| `APP_ENV` | 环境 (`development`/`production`) |

## 问题排查

### 测试失败

```bash
# 查看详细日志
go test -v ./...

# 只运行失败的测试
go test -run "TestName" -v

# 查看覆盖率
go test -cover ./...
```

### 本地服务启动失败

```bash
# 检查端口占用
lsof -i :8081

# 查看服务日志
./scripts/dev-check.sh
```

## 贡献指南

1. **Fork** 并创建 feature branch
2. **编写测试** 覆盖新功能
3. **运行** `./scripts/dev-check.sh` 确保质量
4. **提交** 前运行 gstack review (`/review`)
5. **创建 PR** 并等待 Code Review
