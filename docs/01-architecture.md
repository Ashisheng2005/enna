# 01 · 架构设计说明书

> 本文档的**唯一需求来源**是 [`00-requirements.md`](00-requirements.md)。每一处设计决定都可追溯到其中的目标（G）、功能需求（FR）、非功能需求（NFR）或约束（C）。
> 本文给出**总体架构与核心机制**；专项细节见文末 §14 的追踪表。

---

## 1. 设计目标与原则

### 1.1 五条核心设计主张

| # | 主张 | 服务的需求 |
|---|---|---|
| **P1** | **推理层不生成命令，只选择技能与结构化参数** | G2、NFR-SEC-01、FR-SK-03 |
| **P2** | **确定性逻辑用 Go，概率性逻辑用 Python；安全边界不进 LLM 层** | G2、C-01、NFR-MNT-02 |
| **P3** | **审批绑定"计划哈希"，而非单条命令** | G3、FR-AP-02、FR-AP-07 |
| **P4** | **设备输出是不可信输入**（三层注入防御） | FR-AG-04、NFR-SEC-01 |
| **P5** | **不把运维能力押在 QQ 上**（CLI 兜底 + 离线恢复码） | G5、NFR-AVL-01、FR-OP-03 |

### 1.2 从需求导出的三条工程原则

| 原则 | 来源 | 落地方式 |
|---|---|---|
| **fail-closed** | NFR-REL-01 | 审计不可写 / 策略不可判定 / schema 校验失败 → 拒绝执行 |
| **依赖单向 + 包边界即进程边界** | NFR-MNT-02 | 确定性内核不依赖通道层与 CLI；M2 拆进程零重构 |
| **依赖最小化** | NFR-MNT-01 | M0 仅 2 个第三方依赖；gRPC / Schema 库推迟到真正需要时 |

---

## 2. 架构总览与信任模型

### 2.1 总览

```
                    QQ 客户端（人）
                        │
        ┌───────────────┴───────────────┐
        │   一个 QQ 群（只读总览界面）    │
        └───────────────┬───────────────┘
                        │
        ┌───────────────┴───────────────┐
        │  NapCat #1..#N（外部进程）     │  ← 一设备一账号（拓扑 b）
        │  非官方客户端，视为不可信       │
        └───────────────┬───────────────┘
                        │ N 条反向 WebSocket（NapCat 主动拨入）
                        │ X-Self-ID / X-Client-Role / Bearer token
                        ▼
┌──────────────────────────── 控制机 ────────────────────────────┐
│                                                                │
│   ┌──────────────────────────────────────────────────────────┐ │
│   │  enna-qqgw      接入鉴权 · 连接注册表(bot_id) · 会话状态   │ │
│   │                 消息渲染 · 出站路由(self_id) · 全局限速    │ │
│   └───────┬──────────────────────────────────────┬───────────┘ │
│           │ 入站事件                              │ 出站消息     │
│           ▼                                      ▲             │
│   ┌──────────────────────────┐          ┌────────┴───────────┐ │
│   │ enna-brain（Python，M2）  │          │  若 brain 不可用：  │ │
│   │ 半可信：意图/计划/推理     │          │  确定性指令旁路     │ │
│   │ 无凭据 · 无授权权 · 无命令 │          │  （不消耗 token）   │ │
│   └───────┬──────────────────┘          └────────────────────┘ │
│           │ 技能调用 / 计划提交                                  │
│           ▼                                                     │
│   ┌──────────────────────────────────────────────────────────┐  │
│   │  enna-executor   策略闸门 · 技能注册表 · 审批票据           │  │
│   │  （安全内核）     SSH 传输 · 审计哈希链 · 凭据库            │  │
│   └───────┬──────────────────────────────────────────────────┘  │
│           │                                                     │
│   ┌───────┴────────────┐                                        │
│   │  enna CLI          │  ← 不依赖 QQ / LLM 的恢复与运维通道      │
│   └────────────────────┘                                        │
└───────────────────────────┬────────────────────────────────────┘
                            │ SSH 出站（22）
                            ▼
                受管 Linux 设备 × N（零安装、零监听）
```

**三条硬性边界**：

1. 控制机**不监听公网**：QQ 侧由 NapCat 拨入，设备侧由我们拨出。
2. `enna-executor` **不被 QQ 侧直连**，只接受推理层与本机 CLI 的调用。
3. 凭据**只在 `enna-executor` 内存中**；`qqgw` 与 `brain` 均不可读（FR-AU-05）。

### 2.2 信任模型

| 层 | 组成 | 信任级别 | 关键含义 |
|---|---|---|---|
| 通道 | NapCat ×N、QQ 群成员、同群其他 bot | **不可信** | 仅作事件来源；鉴权与过滤在接入层做 |
| 推理 | `enna-brain`（Python）+ 模型供应商 | **半可信** | 可被提示注入影响，故**无凭据、无授权权、无命令构造权** |
| 内核 | `enna-executor`（策略/技能/SSH/审计/凭据） | **可信** | 唯一做出授权判定与构造命令的地方 |

### 2.3 核心不变量（NFR-SEC-01）

> 即使推理层与模型供应商**被完全控制**，攻击者能造成的最大破坏 =
> **"在策略允许范围内、由有权限的人类确认过的操作"**。

该不变量由以下结构性事实共同保证（**不依赖任何提示词约束**）：

| 保证 | 机制 | 需求 |
|---|---|---|
| 可用动作集封死 | 技能集合启动期静态注册 | FR-SK-02 |
| 参数不可任意 | 严格 schema + 白名单正则 | FR-SK-04 |
| 目标不可越界 | 设备 ACL + 设备级收口 + 拓扑 (b) 一设备一入口 | FR-SK-05、NFR-SEC-02 |
| 风险不可自述 | 风险等级由技能代码声明 | FR-PL-01 |
| 变更需人确认 | 计划级审批 + 票据绑定 plan_hash | FR-AP-02/03/07 |
| 不可静默作恶 | 审计哈希链 + fail-closed | FR-AU-01/02 |
| 无逃逸通道 | 无 `shell.run`（NG1）、无临时连主机（NG4） | NG1、NG4 |

---

## 3. 分阶段形态（**终态 ≠ 起点**）

这是本文档最容易被误读的一点：下面的组件描述是**目标形态**，落地按阶段收敛。

| 阶段 | 进程形态 | 跨进程契约 | 需求范围 | 说明 |
|---|---|---|---|---|
| **M0** | **1 个 Go 二进制**（`enna`） | **无** | FR-SK、FR-PL、FR-AP 部分、FR-AU、FR-OP 部分 | 策略闸门 + 技能 + SSH + 审计 + CLI。**不引入 gRPC / Protobuf / JSON Schema 库** |
| **M1** | 仍是 1 个 Go 二进制（+ NapCat） | 无 | FR-CH-01…10、FR-AG-06 | `qqgw` 作为 package 接入，与 CLI **共用同一条执行路径** |
| **M2** | Go 二进制 + `enna-brain`（Python） | **此时才引入 gRPC** | FR-AG-01…10、FR-AP 完整 | 两个进程才需要契约 |
| **M3** | 同上 | gRPC | FR-SK-11、FR-AP-08、FR-PL-07 | 批量、L3 双签、回滚草案 |
| **M4** | 同上 | gRPC | FR-AG-07、FR-AU-03/08 | 安全验收（只加固，不加功能） |
| **M5** | 同上 | gRPC | FR-AL-*、FR-AG-11 | 观测、告警、根因分析 |
| **M6** | 可按需拆分为多二进制 | gRPC | FR-OP-06 | 加固、分离部署、备份恢复。**包边界已就绪，拆分是配置工作** |

**为什么 M0 坚决不引入 gRPC**：只有一个进程时，RPC 是纯粹的复杂度（proto 文件、代码生成、序列化边界、错误映射），换不来任何收益。JSON Schema 同理 —— 它的唯一刚需是"向 LLM 描述参数"，而模型 M2 才出现。

实现路线见 [`08-m0-implementation-guide.md`](08-m0-implementation-guide.md)。

---

## 4. 组件设计

### 4.1 组件清单与职责

| 组件 | 语言 | 引入阶段 | 职责 | 对应需求 |
|---|---|---|---|---|
| `enna-qqgw` | Go | M1 | 反向 WS 服务端、连接鉴权、`bot_id` 连接注册表、身份映射、事件归一化、出站渲染与路由、双层限速、**确定性指令旁路** | FR-CH-01…11、FR-ID-01/04、FR-AG-06 |
| `enna-executor` | Go | **M0** | 策略闸门、技能注册表、SSH 传输、审批票据、审计哈希链、凭据库 | FR-SK-*、FR-PL-*、FR-AP-*、FR-AU-* |
| `enna-brain` | Python | M2 | 意图路由、计划装配、子 Agent、多模型路由、上下文与记忆 | FR-AG-* |
| `enna` CLI | Go | **M0** | 设备管理、技能直接执行、策略解释、票据吊销、审计校验、紧急停止、离线恢复 | FR-OP-01/02/03/05 |
| NapCat ×N | Node（外部） | M1 | QQ 协议适配，输出 OneBot 11 事件 | C-03 |

**`qqgw` 是唯一同时持有 N 条连接的进程** —— 这是"跨 bot 全局限速"与"跨 bot 循环防护"能落地的前提（§10.1）。

### 4.2 服务定义草案

> 本节描述**M2 才引入**的跨进程契约。M0/M1 不存在这些 RPC —— 组件间是普通的 Go 函数调用。

#### 4.2.1 关键设计决策：入站用「异步提交」而非「双向流」

QQ 侧**没有请求-响应语义**：用户发完消息就走了，NapCat 也不会等待结果。若用长连接双向流承载"提问 → 等待 → 回答"，任何一个环节超时或重启都会**静默丢失结果**，而用户只看到"没反应"。

因此：

- **入站**（qqgw → brain）：`Submit` 为 unary，立即返回 `task_id`，不阻塞。
- **出站**（brain → qqgw）：一切进度与结果经 `Push` 异步发送。
- **代价**：brain 必须自己管理会话状态，不可依赖流存活。
- **收益**：任务生命周期与连接生命周期解耦，长任务不再丢结果。

#### 4.2.2 契约草案

```proto
syntax = "proto3";
package enna.v1;

// ── 出站：唯一的消息出口，由 qqgw 提供 ──
service ChannelService {
  rpc Push(PushRequest) returns (PushAck);
}

message PushRequest {
  uint64 self_id      = 9;   // 用哪个机器人账号发送（拓扑 b 必需，见 04 §3.2）
  string session_id   = 1;   // 会话（含 bot_id，见 §6.5）
  ChannelKind channel = 2;   // PRIVATE | GROUP | CLI
  uint64 target_id    = 3;   // user_id 或 group_id（出站目标）
  RenderHint hint     = 4;   // PLAIN | MERGE_FORWARD | FILE
  string text         = 5;
  repeated Segment segments = 6;
  string task_id      = 7;   // 日志关联与幂等去重
  string chunk_seq    = 8;   // 流式分片序号；空 = 终态
}
message PushAck { bool ok = 1; string retcode = 2; string message = 3; }

// ── 入站：由 brain 提供 ──
service AgentService {
  rpc Submit(MessageEvent) returns (SubmitAck);
  rpc Cancel(CancelRequest) returns (CancelAck);
}

message MessageEvent {
  uint64   bot_id      = 9;   // 收到消息的机器人账号（来自 X-Self-ID）
  string   session_id  = 1;
  ChannelKind channel  = 2;
  uint64   sender_id   = 3;
  string   text        = 4;   // 已剥离 CQ 码的纯文本
  bool     mentioned   = 5;   // 是否 @ 了本 bot
  repeated string attachments = 6;
  int64    ts_ms       = 7;
  string   event_id    = 8;   // 幂等键
}
message SubmitAck { string task_id = 1; bool accepted = 2; string reject_reason = 3; }

// ── 推理层唯一的动作入口 ──
service ToolService {
  rpc List(ListRequest) returns (ToolCatalog);      // 按 principal 过滤可见技能
  rpc Invoke(InvokeRequest) returns (InvokeResult); // 仅 L0/L1 可直接调用
}

service PolicyService {
  rpc SubmitPlan(Plan) returns (PlanDecision);      // 分级 + 签发待确认票据
  rpc Approve(ApproveRequest) returns (ApproveResult);
  rpc GetTicket(GetTicketRequest) returns (Ticket);
  rpc Revoke(RevokeRequest) returns (RevokeAck);
}

service DeviceService {
  rpc List(DeviceListRequest) returns (DeviceList); // 按 principal ACL 过滤
  rpc Describe(DeviceRef) returns (DeviceInfo);
}

message Plan {
  string    task_id   = 1;
  uint64    bot_id    = 5;   // 发起入口（参与票据绑定）
  Principal principal = 2;
  repeated PlanStep steps = 3;
  string    rationale = 4;   // 供人类阅读，不参与授权判定
}
message PlanStep {
  string device_id = 1;
  string skill     = 2;
  string args_json = 3;      // 必须通过该技能的参数校验
}
message PlanDecision {
  DecisionKind kind      = 1;   // ALLOW | NEED_APPROVAL | DENY
  RiskLevel    risk      = 2;
  string       plan_id   = 3;
  string       plan_hash = 4;   // 规范化序列化后的 SHA-256
  string       summary   = 5;   // 影响面摘要（含可验证的量）
  string       deny_code = 6;   // 机器可读，可聚合统计
}
```

#### 4.2.3 契约纪律

| 纪律 | 理由 |
|---|---|
| `args_json` 必须过 schema 校验才算合法步骤；失败则**拒绝**，不降级猜测 | FR-AG-02 |
| 技能目录由内核生成，推理层不硬编码技能列表 | 防模型虚构技能 |
| 所有 RPC 携带 `task_id` / `principal` / `bot_id` | 审计串联 |
| 拒绝原因返回**结构化拒绝码** | FR-PL-08 |

### 4.3 enna-brain（Python 推理层）

- **Coordinator**：意图分类（规则优先，模型兜底）→ 选择执行路径 → 装配计划。
- **Specialists**：`host` / `service` / `log` / `rca`，各自只看到本域技能（**工具集收缩**降低误选）。
- **Router**：按档位选模型 + 熔断 + 故障转移；**数据合规约束优先于模型选择**（FR-AG-05、NFR-COMP-01）。
- **Memory**：会话历史（结构化压缩）、设备画像（只存结论，不存原文日志）、runbook 检索。

**硬性禁止**：持有凭据、生成命令字符串、决定风险等级、批准自己的计划。详见 [`06-brain-orchestration.md`](06-brain-orchestration.md)。

### 4.4 enna CLI（恢复与运维通道）

存在的唯一理由是 G5 / NFR-AVL-01：**QQ 号被封、模型全挂、brain 崩溃时，仍然能接管系统。**

```bash
enna hosts list                     # 设备清单
enna exec web01 host.disk           # 直接调用技能（走完整策略与审计）
enna policy show | explain ...      # 当前策略与判定过程
enna approve <ticket-id> <code>     # 本地审批通道
enna ticket revoke --all            # 吊销全部票据
enna kill --all                     # 全局停止：拒绝新变更计划
enna audit verify | seal | export   # 审计校验、封口、异地导出
enna vault rotate|revoke <device>   # 凭据轮换与吊销
enna device add|trust <id> ...      # 设备登记与指纹确认（防 TOFU）
enna recover --offline-code ***     # 离线恢复码：重置身份与机器人账号
```

**CLI 的能力不低于 QQ 通道**（FR-CH-09），但**同样受策略闸门约束** —— CLI 不是后门，避免"绕过审计改系统"。

---

## 5. 包结构与依赖纪律

### 5.1 目录（M0 精确版）

```
ennaManagement/
├── go.mod                          # module github.com/Ashisheng2005/enna
├── cmd/enna/main.go                # 唯一入口：装配 + 子命令分发
├── internal/
│   ├── config/                     # 配置结构、加载、严格校验（fail-fast）
│   ├── audit/                      # 哈希链审计：record / log / redact / verify
│   ├── device/                     # 设备模型、状态、设备级收口（units/paths）
│   ├── sshx/                       # SSH 传输：command 构造 / client / 连接池
│   ├── skill/                      # 技能注册表
│   │   ├── host/                   # 只读观测技能
│   │   ├── service/                # 服务管理技能
│   │   └── tmp/                    # 临时文件清理
│   ├── policy/                     # 风险分级、判定算法、审批票据
│   └── cli/                        # 子命令实现
└── configs/                        # enna.yaml / policy.yaml / devices.yaml / bots.yaml
```

M1 新增 `internal/onebot/` 与 `internal/qqgw/`；M2 新增 `internal/transport/`（gRPC 装配）与 `brain/`（Python）。

### 5.2 依赖纪律（NFR-MNT-02）

```
允许的依赖方向（单向）：

   cli  ──┐
          ├──►  policy  ──►  skill  ──►  sshx  ──►  device
   qqgw ──┘        │            │
                   └────────────┴──►  audit / config

禁止：
   policy  →  cli / qqgw        ← 内核不得依赖调用方
   skill   →  cli / qqgw
   sshx    →  policy            ← 传输层不知道策略
```

**这条纪律的价值**：M2 拆分进程时，`policy` 与 `skill` 一行都不用改 —— 只是多了一个调用方（gRPC handler）。**包边界就是未来的进程边界。**

### 5.3 让依赖纪律可自动检查

```bash
# 用 go list 检查是否出现反向依赖（可放进 CI）
go list -deps ./internal/policy | grep -q 'internal/qqgw' && echo "❌ 依赖违规" && exit 1
```

---

## 6. 核心机制设计

> 以下机制是架构的承重墙。每条只给结论与关键约束，专项细节见对应文档。

### 6.1 技能与命令构造（FR-SK-02/03/04/05）

| 要点 | 设计 |
|---|---|
| 技能形态 | **struct-of-funcs**：`{Name, Risk, Validate, Build, Parse}`。无状态同质集合，比 interface 更简单 |
| 注册时机 | 启动期显式 `RegisterAll`，**不用 `init()`**（隐式副作用不可测） |
| 命令构造 | `Build` 是唯一出口，返回结构化 `Command{Argv, Sudo, Timeout, OkExitCodes}` |
| 协议事实 | SSH 的 exec 请求是**单个字符串**、远端必过 shell → 因此策略是"**参数不来自自由文本 + 白名单校验 + 一律转义**" |
| 二次收口 | 设备级 `ManagedUnits` / `AllowedPaths` / 目标地址白名单 |
| 退出码 | 非零退出码是**业务结果**，不是 Go error；技能声明 `OkExitCodes` |

详见 [`05-device-executor.md`](05-device-executor.md) §3–§4。

### 6.2 策略闸门（FR-PL-01…09）

判定算法**确定性、顺序执行、deny 短路**：

```
decide(principal, bot_id, channel, device, skill, args) -> Decision

 1. principal 存在且 enabled?                 否 → DENY UNKNOWN_PRINCIPAL
 2. bot 在 principal.bot_scope 内?             否 → DENY NO_BOT_SCOPE
 3. channel 允许该技能的声明风险?               否 → DENY CHANNEL_RESTRICTED
 4. skill 已注册?                              否 → DENY UNKNOWN_SKILL
 5. skill ∈ forbidden?                         是 → DENY FORBIDDEN_SKILL
 6. device 存在且 ∈ principal ACL?             否 → DENY NO_DEVICE_ACL
 7. device.status != FROZEN?                   否 → DENY DEVICE_FROZEN
 8. effective = min(skill.risk, override, acl.max_risk)      # 只收紧
 9. args 过 schema（strict）?                  否 → DENY INVALID_ARGS
10. 设备级收口通过?                            否 → DENY TARGET_NOT_ALLOWED
11. effective >= L1 且审计不可用?              是 → DENY AUDIT_UNAVAILABLE
12. effective == L0/L1 → ALLOW
    effective == L2     → NEED_APPROVAL(sigs=1)
    effective == L3     → NEED_APPROVAL(sigs=2, rollback_required)
    effective == L4     → DENY FORBIDDEN_SKILL
```

**第 8 步的 `min` 是整个安全模型的关键**：配置文件永远无法提升权限（FR-PL-02/04）。

详见 [`02-security-model.md`](02-security-model.md) §4、[`05-device-executor.md`](05-device-executor.md) §5。

### 6.3 计划与审批票据（FR-AP-*）

| 要点 | 设计 |
|---|---|
| 规范化 | 对**语义**敏感、对格式不敏感：参数键递归排序后序列化 |
| 绑定范围 | 主体 + `bot_id` + 设备集合 + 技能 + 参数 + **步骤顺序** |
| 为什么绑计划 | 逐条命令确认会造成审批疲劳，最终等于没确认（P3） |
| 执行期 | **票据携带完整计划副本**，内核只执行票据内步骤 → 批准后追加在结构上不可能 |
| 一次性 | `Issued → Approved → Consumed`；`Consumed` 后不可复用 |
| 确认码 | HMAC 派生 6 位；**常量时间比较**；失败 5 次吊销并冻结 |
| 投递 | **仅私聊**；群内只提示"请私聊确认"（FR-AP-06） |
| 确认路径 | **不调用 LLM**；只提取确认码，忽略其余文本（FR-AP-05） |

详见 [`02-security-model.md`](02-security-model.md) §5–§6。

### 6.4 审计哈希链（FR-AU-01…07）

```
hash_n = SHA256( canonical(record_n) ‖ hash_{n-1} )
```

| 要点 | 设计 |
|---|---|
| 序列化 | **必须用 struct，不能用 map** —— `encoding/json` 对 map 键排序，字段顺序随实现变化会让历史哈希失效 |
| 持久化 | `O_APPEND` + 每次 `File.Sync()`（fsync 是 fail-closed 的物理基础） |
| fail-closed | 审计不可写 → 拒绝 L1 以上动作（**L0 只读仍可用**） |
| 不可删除 | 无删除命令，只有导出 |
| 日封口 | 每日 Merkle 根，可异地导出 |
| 脱敏 | 落盘与入上下文**两处都做**，并记录命中次数 |

详见 [`02-security-model.md`](02-security-model.md) §8。

### 6.5 身份与会话模型（FR-ID-01）

拓扑 (b) 带来两处必须的一等维度：

```
权限 = f(bot_id, qq_id)            ← 同一个人经不同机器人入口，权限可以不同
session_id = "private:<bot_id>:<qq_id>"
           | "group:<bot_id>:<group_id>"
```

| 为什么 | 后果（若不这么做） |
|---|---|
| 出站必须知道用哪个账号发 | 会把 web01 的答复（**包括确认码**）发到 db01 的账号上 |
| 会话必须含 `bot_id` | A 机器人发出的确认码会被 B 机器人的上下文错误接受 |

详见 [`04-multi-bot-topology.md`](04-multi-bot-topology.md) §3–§4。

---

## 7. 关键流程

### 7.1 群内只读（S1 / S3）

```
群成员 @web01助手 "磁盘满了吗"
   │
   ├─ qqgw ①握手鉴权（token + X-Self-ID + X-Client-Role）
   │       ②mentioned==true 且 @ 的是本 bot？否则丢弃
   │       ③sender_id 在 bot_accounts 内？是则丢弃（防 bot 互触发）
   │       ④身份查表 → Principal + bot_scope 校验
   │       ⑤纯 6 位码？→ 走确认路径（不调用 brain）
   │       ⑥确定性指令（/df 等）？→ 直接走内核，不经 brain
   │       ⑦否则 Submit 给 brain（异步，立即返回 task_id）
   ▼
brain 意图分类 → 选技能（L0）→ ToolService.Invoke
   ▼
内核：policy.Decide → ALLOW(L0) → audit.Append → sshx 执行 → Parse 结构化
   ▼
qqgw：渲染 → 群维度全局令牌桶 → *_rate_limited 发送
```

**注意**：群里不会出现确认码，也不会执行任何写操作（FR-ID-04、NG3）。

### 7.2 私聊变更（S2）—— 完整闭环

```
私聊"清理 web01 /tmp 下 7 天前的文件"
   │
   ├─ qqgw 鉴权 → session="private:10001:20001" → Submit
   ▼
brain ①只读诊断（干跑 find 列出文件清单）
      ②生成计划：tmp.prune(path=/tmp, older_than_days=7)
      ③SubmitPlan
   ▼
内核 ④分级：爆炸半径有界 → L2
      ⑤规范化 → plan_hash → 签发票据（5 分钟，一次性）
      ⑥影响面摘要（含文件数、总字节数）
   ▼
qqgw ⑦私聊投递摘要 + 确认码
   ▼
用户 回复确认码
   ▼
qqgw ⑧纯码识别 → 直接 Approve（**不调用 brain**）
   ▼
内核 ⑨常量时间比对 → 校验 plan_hash / principal / bot_id / 未过期 / 未消费
      ⑩audit.Append（fsync）
      ⑪执行票据内步骤（重算哈希做纵深校验）
   ▼
qqgw ⑫私聊回报结果 + artifact 引用
```

### 7.3 L3 双签（S4）

```
第一签：发起人确认码
   ↓
第二签：另一 OWNER；或同一人但 ① 间隔 ≥30s ② 回报 plan_hash 前 8 位
   ↓
核对 rollback 草案非空 + deadman（若为网络类变更）
   ↓
执行（每步前重算哈希）+ 审计记录双签人
```

第二签要求**回报 hash 前 8 位**，是为了强制签署者重新审视具体计划，而非条件反射式确认（对抗审批疲劳，FR-AP-08）。

### 7.4 多 bot 出站路由

```
brain Push{self_id: 10002, ...}
   ↓
qqgw 查 connRegistry[10002]
   ├─ 命中 → 通过该连接的 OneBot action 发送
   └─ 未命中 → ErrBotOffline（该账号 NapCat 掉线）
   ↓
群维度共享令牌桶（跨 bot 汇总）→ 最小间隔排队 → 发送
```

**必须避免**：在"全局唯一连接"或"最后一条活跃连接"上发送。

---

## 8. 状态机

### 8.1 任务

```
SUBMITTED ──► PLANNING ──┬──► EXECUTING ──► DONE
                         │        └──────► FAILED
                         ├──► AWAITING_APPROVAL ──┬──► EXECUTING
                         │                        ├──► DENIED
                         │                        └──► EXPIRED
                         └──► REJECTED（schema 校验失败超限 / 无权限）
任意状态 ──► CANCELLED（用户"停止" 或 CLI kill switch）
```

### 8.2 票据

```
ISSUED ──(正确确认码)──► APPROVED ──(执行完成)──► CONSUMED
   │                        │
   ├──(超时 5min)──► EXPIRED │
   └──(吊销)──────► REVOKED ◄┘
```

`CONSUMED` 后同码不可复用（防重放，FR-AP-03）。

---

## 9. 部署架构

### 9.1 端口分配（默认值，全部可配置）

| 端口 | 进程 | 用途 | 暴露范围 |
|---|---|---|---|
| `7800` | `enna` (qqgw) | OneBot 反向 WebSocket 服务端（NapCat 拨入） | **仅 loopback** |
| `7801` | `enna` (qqgw) | gRPC `ChannelService`（出站推送，M2） | 仅 loopback / UDS |
| `7802` | `enna` (executor) | gRPC `ToolService` / `PolicyService` / `DeviceService`（M2） | 仅 loopback / UDS，**永不监听公网** |
| `7803` | `enna-brain` | gRPC `AgentService`（M2） | 仅 loopback / UDS |
| `9101`/`9102` | 各自 | Prometheus `/metrics` | 内网采集器可达 |

`7800` 与 OneBot 正向 WS 默认的 `6700` 错开，避免与本机其他 OneBot 实现冲突。

### 9.2 三种部署形态

| 形态 | 阶段 | 说明 |
|---|---|---|
| **单机** | M0/M1 | 1 个 Go 二进制 + NapCat。gRPC 不存在 |
| **单机 + Python** | M2–M5 | Go 二进制暴露两个 gRPC 服务 + `enna-brain`。仍全 loopback |
| **分离部署** | M6 | NapCat 与控制机分离，走 WireGuard/SSH 隧道 + mTLS。**executor 仍不得监听公网** |

### 9.3 多 bot 部署（拓扑 b）

- NapCat 用 **systemd 模板单元** `napcat@<qq>.service`，不复制 N 份文件。
- 每实例独立 `onebot11_<qq>.json`，配 `MemoryMax` 与公共 `Slice`。
- 资源量级：Linux 下每实例约 100MB+（C-04 的成本，见 [`04-multi-bot-topology.md`](04-multi-bot-topology.md) §8）。

---

## 10. 并发、限速与背压

| 关注点 | 设计 | 需求 |
|---|---|---|
| 同设备变更 | **串行**（每设备互斥）；L0 读并发 ≤ 8 | FR-SK-07 |
| 同会话任务 | **单飞**：同时只允许一个活跃计划（防连点竞态） | — |
| 出站限速① | 本地令牌桶：单会话 ≤ 1 msg/s、≤ 20 msg/min | FR-CH-05 |
| 出站限速② | OneBot `*_rate_limited`（服务端排队，默认 500ms 间隔） | FR-CH-05 |
| **群维度预算** | **跨 bot 汇总**的共享令牌桶（默认 5 msg/min/群） | FR-CH-11 |
| 群内最小间隔 | 800ms，防"一条消息 @ 多个 bot"时同时涌出 | FR-CH-06 |
| 模型调用背压 | 每档位独立并发信号量；超限排队并给用户进度 | NFR-COST-01 |
| 幂等 | `event_id` 去重；`message_id` 结果缓存 10 分钟 | — |
| 变更重试 | **不自动重试**（半执行比失败更糟） | NFR-REL-02 |

### 10.1 为什么"群维度预算"必须是单例

```
❌ 每连接一个限速器 → N 个 bot = 5N 条/分钟 → 10 个 bot 就是 50 条/分钟刷屏
✅ qqgw 内单例、群维度共享 → 整个群 5 条/分钟
```

**架构约束**：该限速器必须是 `qqgw` 内的**单例**。若 M6 把 qqgw 拆成多进程，这个全局预算会失效 —— 拆分时必须保留单点限速。

---

## 11. 故障模式与降级矩阵

| 故障 | 用户可见影响 | 系统行为 | 需求 |
|---|---|---|---|
| NapCat 掉线（单个） | 该设备入口无响应 | 记录断连 + 告警；**其他设备不受影响**；CLI 可用 | NFR-AVL-03 |
| NapCat 崩溃循环 | 该入口反复中断 | 连续 5 次存活 < 10s → 停止自动重连并告警（避免加剧风控） | — |
| QQ 号被封 | 该入口**永久**失联 | 告警；该设备降级为 CLI 运维；用离线恢复流程换号 | FR-OP-03 |
| brain 崩溃 | 自然语言失效 | qqgw 自动切**确定性旁路**并提示"大脑离线" | FR-AG-06、NFR-AVL-02 |
| 模型供应商全挂 | 同上 | 路由熔断 → 确定性旁路 | FR-AG-05 |
| executor 崩溃 | 无法执行 | brain 如实报错，**绝不由 brain 模拟结果** | — |
| **审计写盘失败** | 变更类被拒 | **fail-closed**：DENY AUDIT_UNAVAILABLE；L0 仍可用 | FR-AU-02 |
| 票据校验失败 | 操作被拒 | 记安全事件 + 吊销票据 + 要求重新发起 | FR-AP-04 |
| 设备 SSH 不可达 | 单设备失败 | 标记 `UNREACHABLE`；**不重试变更类** | NFR-REL-02 |
| 单台设备被注入 | 该设备受影响 | 拓扑 (b) 下**爆炸半径限于该设备** | NFR-SEC-02 |
| 策略加载失败 | 保持旧策略 | **不回退到宽松默认** | FR-PL-06 |

**fail-closed 原则汇总**：审计不可用 → 拒绝写；策略不可判定 → 拒绝执行；schema 失败 → 拒绝而非猜测；模型输出歧义 → 反问而非选一个。

---

## 12. 可观测性

### 12.1 日志

- Go 用 `log/slog`，Python 用 `structlog`，统一字段：`task_id` / `plan_id` / `principal` / `bot_id` / `session_id` / `device_id` / `skill` / `risk`。
- **不记录完整 prompt**（含不可信设备数据），只记哈希与长度。
- `task_id` 作为 trace id 贯穿全链路（FR-AL-04）。

### 12.2 指标（Prometheus）

| 指标 | 用途 |
|---|---|
| `enna_task_total{state}`、`enna_task_duration_seconds{risk}` | 任务吞吐与耗时 |
| `enna_tool_invoke_total{skill,result}`、`enna_tool_duration_seconds{skill}` | 技能健康度 |
| `enna_policy_decision_total{decision,risk}`、`enna_policy_deny_total{code}` | 判定分布（`deny_code` 聚合是重要安全信号） |
| `enna_ticket_total{state}`、`enna_ticket_fail_total{reason}` | 审批流程 |
| `enna_llm_tokens_total{model,kind}`、`enna_llm_failover_total{from,to}` | 成本与可用性 |
| `enna_push_total{result}`、`enna_push_queue_depth{bot_id}` | 出站健康与限速 |
| `enna_ws_connections`、`enna_bot_online{bot_id}` | 通道连接状态 |
| `enna_audit_chain_ok` | **0/1，链校验失败的终极告警** |

### 12.3 安全事件流（FR-AU-08）

越权尝试、票据校验失败、L4 命中、审计链断裂、确认码暴力尝试 → **独立事件流**，走与 QQ 完全独立的告警通道（FR-AL-03）。

---

## 13. 演进与扩展点

| 方向 | 需要新增 | 既有代码改动 | 说明 |
|---|---|---|---|
| **拓扑 (b) 多账号** | 配置条目 + 多连接注册表 | **无**（`bot_id` 已是一等维度） | 代码是 (a) 与 (b) 的共同超集 |
| **网络设备（SNMP/RouterOS）** | 新传输层（与 `sshx` 并列）+ 新技能族 `netdev.*` | 无（`TargetKind` 已预留） | M5 之后单独评估；不要在 SSH 内核未稳时并行做 |
| **edge agent（设备侧拨出）** | 新传输层 + 设备侧二进制 + 离线策略缓存 | 无（`Target` 抽象不假设传输方式） | 当前不需要（C-05 已确认设备可达） |
| **Web 管理台** | 独立前端 + 只读 API | 无（审计已按结构化设计） | 只在审计查询场景真正必要 |
| **MCP 工具生态** | 受控外接适配器 | 无 | **只读 MCP 可直接接入；写操作类必须经 policy 包装**（见 [`09-decisions-and-risks.md`](09-decisions-and-risks.md) Q6） |
| **多租户** | 数据模型大改 | **有** | 明确列为非目标（NG6） |

---

## 14. 需求追踪

| 需求域 | 架构落点 | 专项文档 |
|---|---|---|
| FR-CH 通道与接入 | §4.1、§7.1、§10 | [`03-qq-channel.md`](03-qq-channel.md)、[`04-multi-bot-topology.md`](04-multi-bot-topology.md) |
| FR-ID 身份与权限 | §2.2、§6.5 | [`02-security-model.md`](02-security-model.md) §3 |
| FR-SK 设备与技能 | §6.1 | [`05-device-executor.md`](05-device-executor.md) |
| FR-PL 策略与风险 | §6.2 | [`02-security-model.md`](02-security-model.md) §4–§5 |
| FR-AP 审批与确认 | §6.3、§7.2、§7.3、§8.2 | [`02-security-model.md`](02-security-model.md) §6 |
| FR-AU 审计与凭据 | §6.4 | [`02-security-model.md`](02-security-model.md) §8–§10 |
| FR-AG 编排与推理 | §4.3、§7.2 | [`06-brain-orchestration.md`](06-brain-orchestration.md) |
| FR-AL 告警与观测 | §12 | [`07-roadmap.md`](07-roadmap.md) M5 |
| FR-OP 运维与恢复 | §4.4 | [`02-security-model.md`](02-security-model.md) §11 |
| NFR-SEC-01 核心不变量 | §2.3 | [`02-security-model.md`](02-security-model.md) §1–§2 |
| NFR-MNT-02 依赖单向 | §5.2 | [`08-m0-implementation-guide.md`](08-m0-implementation-guide.md) |
| NFR-LEARN 学习性 | §3、§5.1 | [`08-m0-implementation-guide.md`](08-m0-implementation-guide.md) |

---

## 15. 术语表

| 术语 | 含义 |
|---|---|
| **Principal** | 主体。由 QQ 号映射而来的身份，带角色、可交互 bot 范围与设备 ACL |
| **bot_id / self_id** | 机器人 QQ 账号。拓扑 (b) 下与设备一一对应；是权限与会话的一等维度 |
| **Skill** | 技能。注册表中的具名能力，带参数约束与风险等级。**模型唯一能选择的东西** |
| **Plan** | 计划。有序的 `(device, skill, args)` 步骤序列，是**审批的最小单位** |
| **plan_hash** | 计划规范化序列化后的 SHA-256，是审批票据的绑定对象 |
| **Ticket** | 审批票据。一次性、限时、绑定 plan_hash 与主体与 bot |
| **L0–L4** | 风险等级（只读 / 低危写 / 可逆变更 / 高危 / 禁止），由技能代码声明 |
| **确定性旁路** | 不经过 LLM 的内置指令路径（`/df` 等），用于降级与省成本 |
| **fail-closed** | 无法确认安全时选择拒绝，而非放行 |
| **deadman** | 自锁防护的定时回滚：网络类变更必须附带的自动还原步骤 |
| **拓扑 (a) / (b)** | (a) 一账号多设备；(b) 一设备一账号。见 [`04-multi-bot-topology.md`](04-multi-bot-topology.md) |
