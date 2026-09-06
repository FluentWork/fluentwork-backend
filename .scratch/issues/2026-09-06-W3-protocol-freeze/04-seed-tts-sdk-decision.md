# P0-PROTO-04 — seed-tts-2.0 SDK 决策评审(Opus 解码方案)

> **GitHub Title**: `P0-PROTO-04: seed-tts-2.0 SDK 决策评审 + Opus 解码方案 ADR`
> **Labels**: `v2.0-blocker`, `priority/P0`, `protocol-freeze`, `decision-review`, `adr`
> **Milestone**: `V2.0 W3`
> **截止**: 2026-09-09(周三)18:00
> **Owner**: iOS Lead (主持) + 后端 TL (共同决策)
> **关联**: 79_ V2.0 §九 行动清单 #4 + §七-B.3 WSS 写入缓冲池(下游)

---

## §0 Issue Body(整段可粘贴)

```markdown
## 🎯 目标
针对 iOS 端 TTS 流式播放场景, 决策 Opus 解码方案, 输出 ADR-0073 决策记录。直接影响 Issue 05 的 iOS mock decoder 接入代码, 以及 Issue 06 协议封板的 codec 字段值确认。

## 🚧 阻塞条件
- Issue 03 全部 sub-ticket 完成(ai.tts.audio 帧字段已定义)
- iOS Lead 提供至少 2 个候选方案对比
- 后端 TL 提供 backend 端是否需要额外 SDK 适配(火山 seed-tts-2.0)

## 📋 评审选项(至少 3 个, 互斥)

### 选项 A: iOS 系统原生(推荐 baseline)
- **库**: `libopus` via `OpusToolbox` Swift wrapper 或直接调 C API
- **优点**: 0 第三方依赖, 系统稳定性高, 无许可证问题
- **缺点**: Swift 集成需要写 C 桥接, 编译复杂度高
- **iOS 版本**: iOS 13+ 原生支持 Opus(AVAudioEngine + AVAudioConverter)

### 选项 B: 第三方 Swift Package
- **候选 1**: `opus-swift` (https://github.com/grpc/grpc-swift) - gRPC 官方维护
- **候选 2**: `OpusKit` (https://github.com/BackRooms/OpusKit) - 社区维护
- **优点**: Swift API 友好, 集成快
- **缺点**: 维护活跃度不确定, 许可证需确认(Apache 2.0 / MIT / GPL)

### 选项 C: 字节跳动 seed-tts-2.0 SDK(若提供)
- **来源**: 火山引擎官方 SDK
- **优点**: 与 voicegateway 后端 SDK 版本对齐, 长期支持
- **缺点**: 需商务对接, 凭证依赖(C-2 阻塞), iOS 集成尚未验证

### 选项 D: 暂不优化, 用 PCM 替代
- **临时方案**: voicegateway 不输出 Opus, 直接走 PCM 16-bit LE
- **优点**: 协议简单, iOS 端零解码负担
- **缺点**: 带宽增加 4x(1KB Opus vs 4KB PCM), 移动流量成本高
- **适用**: 紧急 fallback 方案

## 📋 评审会议议程(60 分钟, 9/9 上午)

| 时间 | 议题 | 负责人 | 产出 |
|---|---|---|---|
| 0-10 min | 选项 A 现状调研: iOS AVAudioEngine + Opus 支持矩阵 | iOS Lead | 表格 |
| 10-25 min | 选项 B 第三方库对比: 维护活跃度 / 许可证 / API 易用性 | iOS Lead | 对比表 |
| 25-40 min | 选项 C 商务可行性: seed-tts-2.0 SDK 是否覆盖 iOS | 后端 TL | 答复 |
| 40-50 min | 选项 D 临时 fallback 评估: PCM 带宽成本 | 后端 TL | 数字 |
| 50-60 min | 决议 + ADR-0073 起草 | 双方 | ADR commit |

## ✅ 验收
- [ ] 评审会议召开, 4 个选项全部讨论
- [ ] 产出 `docs/40_研发流程与协作/73_seed_tts_SDK_Opus_Decision_2026-09-09.md` ADR
- [ ] ADR 包含: 上下文 / 决策 / 后果 / 风险 / 拒绝的选项及理由
- [ ] iOS Lead + 后端 TL 在 ADR commit 中签字
- [ ] ADR 决策与 Issue 03 的 codec 字段值一致(都选 Opus)
- [ ] 引用 79_ §四 WSS 冻结清单 + §七-B.3 WSS 写入缓冲池

## 🔗 依赖
- Blocked by: P0-PROTO-03 全部 sub-ticket 完成
- Blocks: P0-PROTO-05 (iOS mock decoder 必须用 ADR 决策的方案)
- 关联: 79_ §七-B.3(WSS 写入缓冲池为下游 ADR-0072 候选)
```

---

## §1 ADR-0073 模板

新建文件 `docs/40_研发流程与协作/73_seed_tts_SDK_Opus_Decision_2026-09-09.md`:

```markdown
# ADR-0073: iOS 端 TTS Opus 解码库决策

**Status**: PROPOSED → ACCEPTED(评审通过后)
**Date**: 2026-09-09
**Authors**: iOS Lead, 后端 TL

## Context

WSS V2.0 协议(P0-PROTO-03)决定 ai.tts.audio 帧的 codec 字段 = "opus"。
iOS 端需要选型 Opus 解码库, 用于 AVAudioEngine + 流式 TTS 播放。

候选 4 个选项, 见 P0-PROTO-04 §0。

## Decision

**选择**: 选项 ____ (评审决议)

## Consequences

### 正面
- (具体决策的收益)

### 负面
- (具体决策的成本 / 风险)

### 中和
- (无明显正负的影响)

## Alternatives Rejected

- **选项 A**(原生): ___________
- **选项 B**(第三方): ___________
- **选项 C**(火山 SDK): ___________
- **选项 D**(PCM fallback): ___________

## Follow-up Actions

- [ ] Issue 05 iOS mock decoder 接入使用本 ADR 决策
- [ ] Issue 06 协议封板时, codec 字段值确认(本 ADR 是否影响 codec 默认值)
- [ ] (W7 性能验证) §七-B.3 WSS 写入缓冲池 ADR-0072 独立评估

## References

- 79_ V2.0 §四 WSS 冻结清单
- 79_ V2.0 §七-B.3 WSS 写入缓冲池
- P0-PROTO-03 ai.tts.audio 帧字段定义
- P0-PROTO-05 iOS mock decoder 接入
```

---

## §2 gh CLI 落库命令

```bash
cd /Users/apple/Developments/FluentWork\ App/fluentwork-backend

gh issue create \
  --title "P0-PROTO-04: seed-tts-2.0 SDK 决策评审 + Opus 解码方案 ADR" \
  --label "v2.0-blocker,priority/P0,protocol-freeze,decision-review,adr" \
  --milestone "V2.0 W3" \
  --body-file .scratch/issues/2026-09-06-W3-protocol-freeze/04-seed-tts-sdk-decision.md
```

---

## §3 评审前预读材料

**iOS Lead 准备**:
1. iOS 13-17 Opus 支持矩阵(AVAudioEngine 文档)
2. `opus-swift` 与 `OpusKit` GitHub stars / last commit / open issues
3. iOS App bundle size 限制 + 第三方库体积影响

**后端 TL 准备**:
1. 火山 seed-tts-2.0 SDK iOS 端官方文档(若有)
2. 商务对接进度(若 SDK 选 C, 凭证 C-2 是否能解锁 iOS 集成)
3. 带宽成本测算: PCM vs Opus 在月活 10k 用户下的流量成本

---

## §4 风险与回滚

| 风险 | 应对 |
|---|---|
| 选项 A 集成耗时超 1 dev-day | 降级到选项 D (PCM fallback), Issue 06 协议封板时 codec 默认值改为 "pcm" |
| 选项 B 第三方库许可证问题 | 立即排除, 强制选 A 或 C |
| 选项 C 商务阻塞(C-2 未解锁) | 排除 C, 选 A 或 B |
| 评审超时 | 严格 60 分钟, 未决议项降级到 Issue 05 实施时边做边定(不推荐, 但有预案) |

---

## §5 不在本 Issue 范围内

- ❌ 不实际写 iOS 集成代码(留给 Issue 05)
- ❌ 不修改 WSS 协议 codec 字段默认值(留给 Issue 06, 但本 ADR 必须明确 codec 值)
- ❌ 不启动 §七-B.3 WSS 写入缓冲池(独立 ADR-0072, W7 启动)
- ❌ 不引入 voicegateway 后端侧的 SDK 改动(Issue 06 之后)