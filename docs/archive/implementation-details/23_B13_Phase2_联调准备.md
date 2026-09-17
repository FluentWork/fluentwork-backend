# Client ASR Phase 2 联调准备 - Status Report

**日期**：2026-09-01  
**状态**：✅ iOS 和 Backend 都已完成并合并

---

## 一、已完成工作

### ✅ iOS I13 实现（已合并到 main）

**PR**: [#39](https://github.com/FluentWork/fluentwork-ios/pull/39) - Merged at 2026-09-01 11:51:58

**核心实现**：
- PCM 音频缓冲（`SpeechSessionMiddleware.swift`）
- ClientASR 集成（AppleSpeechClientASRTranscriber）
- 超时机制（800ms）
- 三个遥测事件

**测试覆盖**：
- 7 个单元测试，100% 通过（0.976s）

**文档**：
- 5 份文档，1,454 行

### ✅ Backend B13 实现（已合并到 main）

**Commit**: `f7b637c` - feat(B13): add client ASR gate for user.speech.end

**核心实现**：
- `Handler.clientASRRequired` 字段
- Gate 逻辑：空 text → 返回 `error.client_asr_required`
- 环境变量 `VOICE_CLIENT_ASR_REQUIRED` 绑定

**测试覆盖**：
- 4 个单元测试，100% 通过

**当前配置**：
- `VOICE_CLIENT_ASR_REQUIRED` **未设置**（默认 false）
- 兼容不带 `text` 字段的老版本客户端

---

## 二、接口对接验证

### 2.1 数据结构对齐 ✅

**iOS 发送格式**：
```swift
// SpeechSessionMiddleware.swift
try await speechClient.sendSpeechBoundary(
    started: false,
    turnID: turnID,
    text: clientASRText  // ← String? (可选)
)
```

**Backend 接收格式**：
```go
// internal/voiceproto/frames.go
type UserSpeechEnd struct {
    Type   string `json:"type"`
    Text   string `json:"text,omitempty"`  // ← 可选字段
    TurnID string `json:"turn_id,omitempty"`
}
```

**验证结果**：✅ 字段名和类型完全匹配

### 2.2 错误处理对齐 ✅

**Backend 返回**（当 gate 开启 + text 为空）：
```json
{
  "type": "error",
  "code": "client_asr_required",
  "message": "user.speech.end.text is required when VOICE_CLIENT_ASR_REQUIRED is enabled"
}
```

**iOS 处理**：
- 当前 iOS 实现会正常发送 `text`（成功时非空，超时/失败时为 nil）
- Backend gate 关闭时，`text=nil` 正常转发
- Backend gate 开启时，`text=nil` 会收到错误（符合预期）

---

## 三、Phase 2 联调计划

### 3.1 环境配置

| 组件 | 环境 | 配置 |
|------|------|------|
| **iOS** | TestFlight Beta | `main` 分支（包含 I13） |
| **Backend** | Staging | `main` 分支（包含 B13） |
| **环境变量** | Staging | `VOICE_CLIENT_ASR_REQUIRED=false`（默认） |

### 3.2 联调 Case

#### Case 1: 正常识别成功 ✅

**iOS 操作**：
```
用户说话："今天学习了 Redux middleware 的实现原理"
iOS ClientASR: 300ms → "今天学习了 Redux middleware 的实现原理"
```

**iOS 发送**：
```json
{
  "type": "user.speech.end",
  "turn_id": "turn-1",
  "text": "今天学习了 Redux middleware 的实现原理"
}
```

**Backend 处理**：
- Gate check: `text` 非空 → 通过
- B12 hit detection: 使用 `text` 字段匹配 Badge
- 返回：`feedback.badge`（如果命中）

**验收标准**：
- [ ] iOS console 显示 `speech_client_asr_completed` 埋点
- [ ] Backend logs 显示 `voice user speech frame ... text="今天学习了..."`
- [ ] iOS 收到 `feedback.badge` 帧（如果笔记库有匹配）

#### Case 2: 超时 fallback ⏱️

**iOS 操作**：
```
用户说很长句子（15 秒）
iOS ClientASR: 800ms 超时 → nil
```

**iOS 发送**：
```json
{
  "type": "user.speech.end",
  "turn_id": "turn-2",
  "text": null  // 或不包含 text 字段
}
```

**Backend 处理**（gate 关闭）：
- Gate check: `clientASRRequired=false` → 跳过检查
- B12 hit detection: `text` 为空，使用 Volcengine ASR fallback
- 返回：`feedback.badge`（使用服务端 ASR 结果）

**验收标准**：
- [ ] iOS console 显示 `speech_client_asr_skipped(timeout)` 埋点
- [ ] Backend logs 显示 `text=""` 或不显示 text 字段
- [ ] Backend 调用 Volcengine ASR
- [ ] iOS 仍然收到 `feedback.badge`（服务端兜底）

#### Case 3: Gate 开启测试 🔒

**Backend 配置**：
```bash
export VOICE_CLIENT_ASR_REQUIRED=true
```

**iOS 操作**：
```
用户说话（模拟 ClientASR 失败）
iOS ClientASR: 失败 → nil
```

**iOS 发送**：
```json
{
  "type": "user.speech.end",
  "turn_id": "turn-3",
  "text": null
}
```

**Backend 处理**：
- Gate check: `clientASRRequired=true` + `text` 为空 → **拒绝**
- 返回错误帧：
  ```json
  {
    "type": "error",
    "code": "client_asr_required",
    "message": "user.speech.end.text is required when VOICE_CLIENT_ASR_REQUIRED is enabled"
  }
  ```

**验收标准**：
- [ ] iOS 收到 `error` 帧
- [ ] Backend logs 显示拒绝日志
- [ ] 不触发 B12 hit detection

#### Case 4: Badge 命中率对比 📊

**测试步骤**：
1. 准备 50 个测试句子（覆盖不同主题）
2. 配置 A：`VOICE_CLIENT_ASR_REQUIRED=false` + iOS ClientASR 启用
3. 配置 B：iOS ClientASR 禁用（使用 RawClientASRTranscriber）
4. 对比 Badge 命中结果

**验收标准**：
- [ ] Client ASR 命中率 vs Server ASR 命中率差异 < 5%
- [ ] Client ASR 平均响应时间 < 300ms
- [ ] Server ASR 平均响应时间 > 500ms

---

## 四、联调验收标准

### 必须通过（P0）

- [ ] **Case 1 通过**：iOS 发送非空 text，Backend 正常处理
- [ ] **Case 2 通过**：iOS 发送 nil text（gate 关闭），Backend fallback 到服务端 ASR
- [ ] **iOS 埋点正常**：三个事件正确触发并上报
- [ ] **Backend 接收正常**：logs 能看到 `text` 字段
- [ ] **无崩溃**：iOS 和 Backend 都稳定运行

### 可选验证（P1）

- [ ] **Case 3 通过**：gate 开启时拒绝空 text
- [ ] **Case 4 通过**：Badge 命中率对比 < 5% 差异
- [ ] **性能测试**：Client ASR 响应时间 < 300ms（P95）

---

## 五、联调前准备清单

### iOS 侧

- [ ] **构建 TestFlight 版本**
  - Branch: `main`（包含 PR #39）
  - 配置：默认启用 ClientASR
  - 邀请测试用户

- [ ] **准备测试设备**
  - 真机（iPhone）
  - Xcode Instruments（性能监控）
  - 网络代理（Charles / Proxyman）查看 WebSocket 流量

### Backend 侧

- [ ] **部署 Staging 环境**
  - Branch: `main`（包含 commit `f7b637c`）
  - 配置：`VOICE_CLIENT_ASR_REQUIRED=false`
  - 验证健康检查通过

- [ ] **配置日志**
  - 确保 `voice user speech frame` 日志显示 `text` 字段
  - 配置日志级别：INFO

- [ ] **准备监控**
  - Grafana dashboard（可选）
  - 实时查看 logs（`kubectl logs -f`）

### 测试数据

- [ ] **准备笔记库**
  - 创建测试用户
  - 上传至少 10 条笔记（覆盖不同主题）
  - 记录笔记关键词（用于验证 Badge 匹配）

- [ ] **准备测试句子**
  - 50 个测试句子（CSV 或文本文件）
  - 覆盖：短句、长句、中英文混合、专业术语

---

## 六、联调时间安排

### Day 1（2026-09-02）

**上午**：
- [ ] Backend 部署到 staging（DevOps）
- [ ] iOS TestFlight 构建（iOS Team）
- [ ] 测试环境验证（QA）

**下午**：
- [ ] Case 1 + Case 2 联调（iOS + Backend）
- [ ] 埋点验证（Analytics Team）

### Day 2（2026-09-03）

**上午**：
- [ ] Case 3 联调（gate 开启测试）
- [ ] Case 4 性能测试（QA）

**下午**：
- [ ] 整理联调报告
- [ ] 修复发现的问题（如有）
- [ ] 准备 Phase 3 灰度方案

---

## 七、联调成功标准

### 技术指标

- [x] iOS I13 实现完成
- [x] Backend B13 实现完成
- [x] 所有单元测试通过
- [ ] Case 1-2 联调通过（P0）
- [ ] Case 3 联调通过（P1）
- [ ] Badge 命中率持平（±5%）

### 文档更新

- [x] iOS 实现文档（5 份）
- [x] Backend 实现文档（2 份）
- [ ] 联调报告（待联调后）
- [ ] Phase 3 灰度方案（待制定）

---

## 八、下一步行动

### 🔴 立即执行（今天）

- [ ] **通知 DevOps**：部署 Backend staging
- [ ] **通知 iOS Team**：构建 TestFlight
- [ ] **通知 QA**：准备测试环境和数据

### 🟡 明天开始（09-02）

- [ ] **联调 Case 1-2**
- [ ] **收集日志和埋点数据**
- [ ] **记录发现的问题**

### 🟢 后天完成（09-03）

- [ ] **联调 Case 3-4**
- [ ] **整理联调报告**
- [ ] **制定 Phase 3 灰度方案**

---

## 九、风险提示

### 已知风险

1. **iOS ClientASR 准确率**
   - 风险：如果准确率 < 85%，Badge 命中率可能下降
   - 缓解：Case 4 对比测试，如果差异 > 5% 则调整超时时间

2. **Backend Volcengine ASR fallback**
   - 风险：fallback 逻辑未完整实现
   - 缓解：Case 2 重点验证 fallback 路径

3. **网络延迟**
   - 风险：staging 环境网络慢，影响性能测试
   - 缓解：使用真实网络环境，记录 P50/P95/P99 延迟

### 回滚预案

**iOS 侧**：
```swift
// 如果联调失败，禁用 ClientASR
Container.shared.clientASRTranscriber.register {
    RawClientASRTranscriber()  // 返回 nil，全部 fallback
}
```

**Backend 侧**：
```bash
# 保持 gate 关闭（默认配置）
# 无需特殊操作
```

---

## 十、联系人

| 角色 | 姓名 | 职责 | Slack Channel |
|------|------|------|---------------|
| **iOS Lead** | @tangzzz | 联调协调、问题修复 | #client-asr |
| **Backend Lead** | @backend-lead | 日志验证、fallback 确认 | #client-asr |
| **QA Lead** | @qa-lead | Case 执行、数据收集 | #client-asr |
| **DevOps** | @devops | 环境部署、日志配置 | #infra |
| **Product** | @pm | 验收标准、Phase 3 决策 | #product |

---

**最后更新**：2026-09-01 19:56  
**下次更新**：联调开始后（2026-09-02）

---

## 附录：快速命令参考

### iOS 测试

```bash
# 运行 ClientASR 测试
swift test --filter SpeechSessionMiddlewareClientASRTests

# 查看日志（真机）
idevicesyslog | grep "ClientASR"
```

### Backend 测试

```bash
# 运行 B13 测试
go test ./internal/voicegateway -run "TestHandler_.*ClientASR" -v

# 查看 staging logs
kubectl logs -f deployment/voice-gateway -n staging | grep "user.speech.end"

# 临时开启 gate（测试）
kubectl set env deployment/voice-gateway VOICE_CLIENT_ASR_REQUIRED=true -n staging
```

### 网络调试

```bash
# 使用 Charles 查看 WebSocket 流量
# 过滤：wss://staging-api.fluentwork.com/voice

# 使用 websocat 模拟客户端
websocat wss://staging-api.fluentwork.com/voice
```
