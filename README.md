# Enna · 设备与网络管理员 Agent

> 把运维权限装进 QQ。用户 → QQ → 受管设备上的具体操作。
> LLM 不是聊天窗口，而是**在确定性策略边界内执行管理员动作**的编排器。

**当前状态：设计阶段。仓库内只有文档，无任何代码。** 代码由项目所有者本人编写（见 [`docs/00-requirements.md`](docs/00-requirements.md) NFR-LEARN-01）。

---

## 1. 一句话说明它是什么

**一个由 QQ 驱动、带权限模型与审计的多设备运维控制台。**

它和"把大模型接进 QQ 群聊天"的根本区别在于三条硬性要求：

| # | 要求 | 落地方式 |
|---|---|---|
| 1 | **有真实权限** | 能查指标、重启服务、清磁盘、读日志 —— 动作落在真实设备上 |
| 2 | **有确定性边界** | 权限判定不由 LLM 做出，而由编译进二进制的策略闸门做出 |
| 3 | **有不可抵赖的账** | 每次操作可追溯到人、入口、设备、技能、参数、审批票据与原始输出 |

## 2. 五条核心设计主张

| # | 主张 | 为什么 |
|---|---|---|
| **P1** | **LLM 不生成命令字符串，只选择技能与结构化参数** | 这是整个安全模型的地基。模型输出 `{"skill":"service.restart","args":{"unit":"nginx"}}`，而不是 `systemctl restart nginx`。命令由 Go 侧查表拼装 → **提示注入无法直接变成 RCE** |
| **P2** | **确定性用 Go，概率性用 Python；安全边界不进 LLM 层** | 鉴权、策略、票据、审计、SSH、限速 → Go；规划、推理、多模型路由 → Python |
| **P3** | **审批绑定"计划哈希"，不是"某条命令"** | 确认码 HMAC 绑定规范化后的完整计划；内核只执行票据内记录的步骤 → **批准后追加步骤在结构上不可能** |
| **P4** | **设备输出是不可信输入** | 日志、文件名、进程名都可能含注入载荷。结构化包裹为不可信数据 + P1 的结构性兜底 |
| **P5** | **不给 QQ 单点依赖** | 非官方协议有账号风险。保留 CLI 恢复通道 + 离线恢复码，即使 QQ 号丢失也能接管系统 |

## 3. 架构一览

```
                    QQ 客户端（人）
                        │
        ┌───────────────┴───────────────┐
        │   一个 QQ 群（只读总览界面）    │
        └───────────────┬───────────────┘
                        │
        ┌───────────────┴───────────────┐
        │  NapCat #1..#N（一设备一账号） │  ← 非官方客户端，视为不可信
        └───────────────┬───────────────┘
                        │ N 条反向 WebSocket（NapCat 主动拨入）
                        ▼
┌──────────────────────────── 控制机 ────────────────────────────┐
│  enna-qqgw     接入鉴权 · bot_id 路由 · 渲染 · 全局限速         │
│      │                                        ▲                │
│      ▼                                        │                │
│  enna-brain（Python，M2）        确定性指令旁路（无 LLM 降级）   │
│  半可信：无凭据 · 无授权权 · 无命令构造权                        │
│      │                                                          │
│      ▼                                                          │
│  enna-executor  策略闸门 · 技能注册表 · 票据 · SSH · 审计 · 凭据 │
│  （安全内核）                                                    │
│      ▲                                                          │
│  enna CLI      不依赖 QQ / LLM 的恢复与运维通道                  │
└───────────────────────────┬────────────────────────────────────┘
                            │ SSH 出站（22）
                            ▼
                受管 Linux 设备 × N（零安装、零监听）
```

**三条硬性边界**：控制机不监听公网 · `executor` 不被 QQ 侧直连 · 凭据只在 `executor` 内存中。

**核心不变量**：即使推理层与模型供应商被完全控制，破坏上限 = "在策略允许范围内、由有权限的人类确认过的操作"。

## 4. 技术选型

| 层 | 选型 | 说明 |
|---|---|---|
| QQ 通道 | **OneBot 11 / NapCat** | 能力最全（私聊、群 @、文件）；代价是非官方协议有账号风险 |
| 确定性内核 | **Go 1.26** | 单二进制、并发模型贴合多设备与长连接、便于审计 |
| 编排与推理 | **Python 3.12** | LLM SDK、结构化输出、RAG、评测生态 |
| 进程间契约 | **M0 无**；M2 起 gRPC + Protobuf | 只有一个进程时 RPC 是纯复杂度 |
| 受管设备 | **Linux + SSH（首批）** | 通用性最高；设备须可被控制机直接 SSH（已确认） |
| 凭据 | 本地加密库，**推理层永不接触** | 缩小泄露面 |
| 部署 | systemd + 二进制（当前环境无 Docker） | 与现状一致 |

第三方依赖刻意压到最小：**M0 仅 2 个**（YAML + SSH）。

## 5. 文档地图

按**阅读顺序**排列。`00` 是需求的唯一来源，`01` 是架构的权威描述，其余为专项设计。

| 文档 | 回答什么问题 |
|---|---|
| [`docs/00-requirements.md`](docs/00-requirements.md) | **要做什么、做到什么程度、明确不做什么**（G/NG/FR/NFR/约束） |
| [`docs/01-architecture.md`](docs/01-architecture.md) | **总体架构与核心机制**：组件、信任模型、阶段形态、包结构、流程、部署、故障降级、可观测性 |
| [`docs/02-security-model.md`](docs/02-security-model.md) | 威胁模型、身份与角色、L0–L4 风险分级、计划哈希与审批票据、审计哈希链、凭据管理、注入防御、事件响应手册 |
| [`docs/03-qq-channel.md`](docs/03-qq-channel.md) | OneBot 11 反向 WS 接入、握手鉴权、NapCat 配置、出站渲染与防封、确认码路由、多 bot 防互触发 |
| [`docs/04-multi-bot-topology.md`](docs/04-multi-bot-topology.md) | 拓扑 (b)（N 账号同群）：账号成本提醒、配置模型、`self_id` 出站路由、权限简化、跨 bot 限速、部署与故障模式 |
| [`docs/05-device-executor.md`](docs/05-device-executor.md) | 技能注册表、命令构造五规则、首批技能清单与风险等级、策略 DSL 与判定算法、SSH 传输层、输出处理 |
| [`docs/06-brain-orchestration.md`](docs/06-brain-orchestration.md) | Coordinator/Specialist、工具调用循环、多模型路由与熔断、上下文预算、五层提示词、计划生成规范、对抗评测集 |
| [`docs/07-roadmap.md`](docs/07-roadmap.md) | M0–M6 里程碑：交付物、可执行验收命令、明确不做清单、风险 |
| [`docs/08-m0-implementation-guide.md`](docs/08-m0-implementation-guide.md) | **M0 手把手实现手册**：目录结构、Step 0–9 顺序、每步的 Go 知识点与验收命令、14 个新手坑 |
| [`docs/09-decisions-and-risks.md`](docs/09-decisions-and-risks.md) | 已决策记录及代价、待决策项、风险登记册、待核验假设、许可合规 |

### 按角色选读

| 你的关注点 | 建议路径 |
|---|---|
| 我要判断这事该不该做 | `00` → `01` §1–§3 → `09` 第二部分 |
| 我要开始写代码 | `08` → `07`（M0 验收）→ `05` §3–§4 |
| 我关心安全性 | `02` 全文 → `01` §2.3 → `05` §3 |
| 我关心 QQ 接入 | `03` → `04` → `01` §6.5 |
| 我关心 Agent 智能化 | `06` → `01` §4.2 |
| 我要做决策 | `09` 第五部分（现在需要回答的最小集合） |

## 6. 当前进度与从哪继续

| 阶段 | 状态 |
|---|---|
| 设计文档 `00`–`09` | ✅ 已基线（91 条内部链接零断链） |
| **M0 Step 0–2**：配置加载 + 审计哈希链 + CLI | ✅ **已实现可用** —— `go build` / `go vet` / `go test -race` 全绿，24 顶层 + 40 子用例 |
| M0 Step 3–7：设备模型 / SSH 传输 / 技能注册表 / 策略闸门 / 审批票据 | ⬜ 待实现 |
| M0 Step 8–9：CLI 补全 / 端到端验收 | 🟡 部分（`config` / `audit` 子命令已完成） |

**已经能用的部分**（现在就可以跑）：

```bash
go build ./... && go test -race ./...

cp configs/enna.example.yaml configs/enna.yaml   # 改成本机绝对路径
go run ./cmd/enna config check --config configs/enna.yaml
go run ./cmd/enna audit verify --config configs/enna.yaml
go run ./cmd/enna audit show   --config configs/enna.yaml --tail 20
```

`internal/audit` 本身就是一个**可独立复用的防篡改 append-only 日志**：
篡改任一记录会被精确定位到条号，删除记录会被 `prev_hash` 抓住，
往可疑历史上追加会被拒绝启动（fail-closed）。

代码阅读顺序与五个关键设计问题见 [`docs/08-m0-implementation-guide.md`](docs/08-m0-implementation-guide.md) §1.5。

M0 的最终目标是一个**完全不依赖大模型**的、带防篡改审计与确定性权限闸门的远程运维工具：

```bash
enna device add lab01 --addr 127.0.0.1:22
enna device trust lab01 --fingerprint SHA256:...
enna exec lab01 host.disk --json                 # 真实数据
enna exec lab01 service.restart --unit nginx     # NEED_APPROVAL + 确认码
enna approve <ticket-id> <code>                  # 执行
enna audit verify                                # OK / 指出断链位置
```

QQ 与 LLM 都是在它之上做**加法**。

## 7. 参考项目与许可提示

设计上参考了以下真实项目（**参考架构，不复制代码**）：

- **[ongrid](https://github.com/ongridio/ongrid)** —— Go 实现的 ops Agent，本方案最贴近的参考。值得借鉴三点：edge 侧主动拨出、`cmdpolicy` 策略沙箱包、变更类技能的 double-sign 闸门。**注意：AGPL-3.0**，直接借用代码会触发传染性开源义务，建议 clean-room 参考设计。
- **[OpenClaw](https://github.com/openclaw/openclaw)** —— LLM 驱动的个人设备管理助手，验证了"IM 作为设备管理入口"这条路可行。
- **[OneBot 11 规范](https://github.com/botuniverse/onebot-11)** —— 消息与 API 契约（反向 WS、`_rate_limited` 语义、`retcode` 映射均已核验）。
- **[NapCatQQ](https://github.com/NapNeko/NapCatQQ)** —— OneBot 11 的无头 QQ 实现。

许可合规的完整讨论见 [`docs/09-decisions-and-risks.md`](docs/09-decisions-and-risks.md#q9-许可与合规)。
