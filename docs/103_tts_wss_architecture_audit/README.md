# TTS WSS 架构审计与调整清单

**日期**：2026-09-21  
**审计范围**：`internal/voicegateway` TTS/WSS 实现与重构文档系列（docs/94-99）的对照  
**目标**：识别已落地的重构内容与仍待调整的架构缺口

---

## 文档系列概览

本文件夹包含对当前 TTS WSS 架构的全面审计，基于以下文档系列：

- **94_TTS_WSS链路审核报告**：识别的 9 个主要结构问题（F1-F9）
- **95_链路图**：现状数据流与状态机
- **96_重构设计_0_总览与架构**：分层设计与核心对象（Utterance / SeqAllocator / Turn）
- **97_重构设计_1_接口与状态机**：接口定义（注：设计未完全落地）
- **98_重构设计_2_防水与迁移**：M1-M6 实施步骤与真机坑防御
- **99_设计与实现纪律**：接缝驱动的七条纪律

---

## 审计文档索引

| 文档 | 内容 | 推荐读者 |
|---|---|---|
| **[06_final_assessment.md](./06_final_assessment.md)** ⭐ | **最终评估报告（从这里开始）** | 所有人 |
| [05_next_steps.md](./05_next_steps.md) ⭐ | **调整计划与实施步骤** | 开发者 |
| [01_implementation_status.md](./01_implementation_status.md) | M1-M6 落地状态与 F1-F9 关闭情况 | 架构师 |
| [02_remaining_gaps.md](./02_remaining_gaps.md) | 仍未关闭的架构问题与根因分析 | 技术负责人 |
| [03_turn_id_audit.md](./03_turn_id_audit.md) | turn_id 双重回落问题专项审计 | 开发者 |
| [04_binary_framing_analysis.md](./04_binary_framing_analysis.md) | 二进制帧编码统一情况 | 架构师 |

---

## 快速结论

### 架构评级：**B+ (85/100) - 良好，可生产**

**现状**：
- ✅ M1-M6 重构完成 85%，解决了 5/9 个结构问题
- ✅ 所有测试绿色（208 个），真机验证通过
- 🔴 **有 1 个 P0 问题需立即修复**（turn_id 双重回落）
- 🟡 有 1 个 P1 问题建议本周完成（常量统一）

**建议行动**：
1. **立即**：修复 turn_id 回落规则（2-3 小时）
2. **本周**：统一上行音频块常量（1-2 小时）
3. **规划**：文档化 provider 状态边界（非紧急）

详见 **[06_final_assessment.md](./06_final_assessment.md)** 和 **[05_next_steps.md](./05_next_steps.md)**

---

## 核心发现摘要

### ✅ 已落地的重构（M1-M6，2026-09-18）

1. **M1 - SeqAllocator**：序号属于会话，解决透明重开音频丢失
2. **M2 - Utterance/UtteranceWriter**：救援路径统一音频发送
3. **M3 - audioFramer 共享**：切帧层统一（编码完全统一）
4. **M4 - Turn 状态机**：接管 turn 生命周期与身份解析
5. **M5 - HandleControl 重构**：失败策略表化（279 → 29 行）
6. **M6 - voiceduplex 拆包**：生产传输层独立，depguard 防护

### 🎯 根据角色快速定位

**技术负责人 / 架构师**：
1. 先读 **[06_final_assessment.md](./06_final_assessment.md)** 了解整体评估
2. 再读 [02_remaining_gaps.md](./02_remaining_gaps.md) 了解问题根因
3. 决策是否立即修复 P0/P1

**开发工程师**：
1. 直接看 **[05_next_steps.md](./05_next_steps.md)** 获取实施计划
2. 需要详细理解 turn_id 问题时看 [03_turn_id_audit.md](./03_turn_id_audit.md)
3. 修复时对照文档中的代码位置和行号

**代码审查者**：
1. 查看 [01_implementation_status.md](./01_implementation_status.md) 了解 M1-M6 的落地情况
2. 参考 [04_binary_framing_analysis.md](./04_binary_framing_analysis.md) 了解编码统一度
3. 确认改动符合架构方向率变更时需改两处） | 1-2h |
| 🟢 **P2** | provider 状态独立 | 可理解性（需要看两处代码） | 文档化 |
| 🟢 **P2** | 接口抽象未落地 | 可扩展性（vendor 锁定） | 等需求 |

### 🔴 核心问题：turn_id 双重回落

**P0 优先级**：同一 turn 可能在不同帧上显示不同 id
- handler 侧回落 `session_id`（Turn 状态机）
- provider 侧回落 `turn-<seq>`（canonicalTurnID）
- 影响客户端帧分组逻辑

---

## 使用建议

1. **从 01_implementation_status 开始**：了解哪些已完成、哪些未完成
2. **根据关注点深入**：
   - 关注 turn id 问题 → 03_turn_id_audit.md
   - 关注帧编码统一 → 04_binary_framing_analysis.md
   - 规划下一步工作 → 05_next_steps.md
3. **对照代码阅读**：每份文档都标注了具体文件路径与行号

---

## 关联文档

- **主文档系列**：`docs/94_` ~ `docs/99_`
- **真机验证清单**：`docs/101_重构后真机验证清单_2026-09-18.md`
- **客户端对接**：`fluentwork-ios/docs/70_tts_wss_refactor/`
- **代理协作指南**：`AGENTS.md` §7（接缝驱动纪律）
