# Close Plans: #43 + #42

> 两个 P0 线上事故的 close 计划。代码层已修(74_ 验证日志)+ 单测全绿,**待人工确认真机联调后 close**。

---

## #43 — keep alive / auto-reopen idle Volc duplex audio path

### Close Reason(gh close comment)

```markdown
## ✅ 关闭原因(2026-09-06)

**状态**:代码层修复完成 + 单测全绿,真机长时回归纳入 W3 Day 1-3 联调范围

### 修复证据
- commit `5e3b8b0`:keepalive 60s 阈值 + 空 commit 探针 + reopen-once
- 单测 6/6 全绿:`go test ./internal/voicegateway/... -count=1 -race`
  - TestHandleClientAudio_ProbeOnIdle
  - TestHandleClientAudio_ProbeFails_ReturnError
  - TestVolcDuplexSession_KeepaliveThreshold
  - TestVolcDuplexSession_ProbeTimeout
  - TestReopenOnceAfterBrokenPipe
  - TestProbeFnOverrideForTests

### 真机验证(待 W3 Day 1-3 完成)
- [ ] 真机 1 次 + 脚本化 5 次:idle ≥ 3 min 后恢复说话,对话继续不报错
- [ ] 若无法 reopen:仅发 1 次稳定 `provider_audio_failed`,iOS 干净重试不重发
- [ ] 联调结果回写到 `docs/40_研发流程与协作/74_V2_0_凭证与P0修复验证日志_2026-09-06.md` §3.2

### 关联
- 文档:`docs/40_研发流程与协作/74_V2_0_凭证与P0修复验证日志_2026-09-06.md` §3.1
- 代码:`internal/voicegateway/provider_volc_duplex.go`
- 启动包:W3 Day 1-3 真机联调 owner:`@voice-gateway 当周值班`
```

### 风险与回退

| 风险 | 触发条件 | 回退方案 |
|---|---|---|
| keepalive 改动引发 Volc 上游限流 | Volc 商务侧 QPS 上限 | 改为「被动重连」(空闲后下一次 audio forward 才触发),零额外 QPS |
| 真机长时回归发现新症状 | ≥3 min 静默后 vendor socket 仍坏 | reopen-once 改 reopen-twice,或增加 backoff |

---

## #42 — wire corpus-backed BadgeEmitter for live voice-gateway

### Close Reason(gh close comment)

```markdown
## ✅ 关闭原因(2026-09-06)

**状态**:代码层修复完成 + 单测全绿,真机 `feedback.badge emitted` 验证纳入 W3 Day 1-3 联调范围

### 修复证据
- commit `77e0578` + `fde8390` + `64453e3`:`cmd/voice-gateway` 主进程挂 BadgeEmitter
- 单测 7/7 + turn_id 2/2 全绿:
  - BadgeEmitter 7/7:TestBadgeEmitter_Emit / TestBadgeEmitter_NoEmit / TestBadgeEmitter_Dedupe / TestBadgeEmitter_RateLimit / TestBadgeEmitter_DBUnavailable / TestBadgeEmitter_TurnIDPropagation / TestBadgeEmitter_Async
  - turn_id 2/2:TestBadgeEmitter_ActiveTurnID / TestBadgeEmitter_TurnIDAlignment

### 真机验证(待 W3 Day 1-3 完成)
- [ ] 真机 Volc Duplex 链路出现 `feedback.badge emitted`,`phrase_block_id` / `turn_id` 均非空
- [ ] iOS `state.badgeFeedback.entries[].turnID` 与后端完全一致
- [ ] 无命中场景**不发**帧;badge 800ms 超时/失败不影响音频流 / 转录 / `session.end`
- [ ] 联调结果回写到 `docs/40_研发流程与协作/74_V2_0_凭证与P0修复验证日志_2026-09-06.md` §3.3

### 关联
- 文档:`docs/40_研发流程与协作/74_V2_0_凭证与P0修复验证日志_2026-09-06.md` §3.1
- 代码:`internal/voicegateway/badge_emitter.go` + `cmd/voice-gateway/main.go`
- 启动包:W3 Day 1-3 真机联调 owner:`@voice-gateway 当周值班`
```

### 风险与回退

| 风险 | 触发条件 | 回退方案 |
|---|---|---|
| emitter 接入后命中激增导致 iOS 抖动 | 语料库量大于预期 | 加 `badge` 限流:每 session ≤ N 条/分钟(具体值在 W2 内与 iOS 联调定) |
| 真机 turn_id 与 iOS 不一致 | iOS 端 turn_id 命名规则变更 | 实施 I20 turn 超时兜底时同步对齐(I20 已就绪) |

---

## 执行命令(review 通过后)

```bash
# 关闭 #43
gh issue close 43 \
  --comment "$(cat <<'EOF'
✅ 代码修复 commit 5e3b8b0;单测 6/6 全绿。
真机长时回归纳入 W3 Day 1-3 联调范围,联调结果回写 74_ §3.2。
详见:docs/40_研发流程与协作/74_V2_0_凭证与P0修复验证日志_2026-09-06.md §3.1
EOF
)"

# 关闭 #42
gh issue close 42 \
  --comment "$(cat <<'EOF'
✅ 代码修复 commit 77e0578+fde8390+64453e3;BadgeEmitter 7/7 + turn_id 2/2 全绿。
真机 feedback.badge emitted 验证纳入 W3 Day 1-3 联调范围,联调结果回写 74_ §3.3。
详见:docs/40_研发流程与协作/74_V2_0_凭证与P0修复验证日志_2026-09-06.md §3.1
EOF
)"
```
