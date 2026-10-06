# 07 · 路线图与验收标准

## 组织原则

| # | 原则 |
|---|---|
| G1 | **安全闸门与能力同期交付**。绝不允许"先跑通，安全后补" —— 一个能执行任意命令但没审计的中间版本，比没有系统更危险 |
| G2 | **每个里程碑结束时系统都是可用的**。不是"能演示"，是"能真的用它管机器" |
| G3 | **先打通最窄的垂直切片**（单设备 / L0 / CLI / 无 LLM / 无 QQ），再横向扩宽。避免同时调试协议、模型、权限三件不确定的事 |
| G4 | **验收标准必须可执行**（一条命令或一个可观察行为），不接受"功能已实现"这类表述 |
| G5 | **对抗用例零越权是发布的硬门槛**，不是"尽力而为" |

### 关于垂直切片顺序的取舍

直觉上应该"先把 QQ 接上，看起来像那么回事"。这个顺序是错的：QQ 是**最不可控**的一环（非官方协议、账号风险、网络环境敏感）。正确的顺序是先把**最确定的内核**（策略闸门 + 执行器 + 审计）做扎实并用 CLI 验证，再叠加推理层，最后挂通信层。

```
M0 内核（Go/CLI）  →  M1 通道（+QQ）  →  M2 推理（+LLM）  →  M3/M4 扩宽与加固
   ↑ 最确定                                    ↑ 最不确定
```

这样任何一层的失败都不会污染其他层的正确性判断。

---

## M0 · 契约与安全内核（纯 Go，无 QQ，无 LLM）

**目标**：建立一个**不依赖模型、不依赖 QQ** 也能管一台机器的最小可信内核。

### 交付物

- **单个 Go 二进制** `cmd/enna`。**不引入 proto/gRPC**（只有一个进程时 RPC 是纯复杂度；M2 接入 Python 时再引入）。
- 详细的分步实现指引见 [`08-m0-implementation-guide.md`](08-m0-implementation-guide.md)。
- `internal/skill/registry` + 首批 **L0 技能（≥ 10 个）** + **L2 技能 2 个**（`service.restart`、`tmp.prune`）。
- `internal/policy`：判定算法（[`05` §5.1](05-device-executor.md#51-判定算法确定性短路deny-优先)完整实现）+ 策略加载与校验。
- `internal/sshx`：连接池、**严格主机密钥校验**（无 TOFU）、超时、命令构造五规则。
- `internal/audit`：哈希链 append-only + `enna audit verify`。
- `internal/vault`：age 加密凭据存储 + `enna vault` 子命令。
- `configs/`：`enna.yaml`、`policy.yaml`、`devices.yaml` 示例。
- 单元测试：每个技能 `Build` 的注入用例；策略判定表驱动用例。

### 验收标准

```bash
# 1. 构建与启动
go build ./... && enna-executor --config configs/enna.yaml     # 启动成功

# 2. 受信设备登记（人工确认指纹，无 TOFU）
enna device add lab01 --addr 127.0.0.1:22
enna device trust lab01 --fingerprint SHA256:xxxx               # 必须显式确认

# 3. 只读技能真实可用
enna exec lab01 host.disk --json                                # 退出码 0，输出真实数据
enna exec lab01 service.status --unit nginx                    # 受管单元正常
enna exec lab01 service.status --unit evil                     # 退出码≠0，TARGET_NOT_ALLOWED

# 4. 策略短路与设备级收口
enna exec lab01 service.restart --unit nginx                   # 返回 NEED_APPROVAL(L2) + 确认码
enna policy explain --qq 10001 --skill service.restart --device lab01   # 打印判定逐步结果

# 5. 注入防御（单测）
go test ./internal/skill/... -run TestBuildInjection             # 全绿
go test ./internal/policy/... -run TestDecide                    # 全绿（覆盖每个 DENY 分支）

# 6. 审计链
enna audit show --tail 20                                        # 可见最近操作
enna audit verify                                                # OK
# 手工篡改审计文件一行后：
enna audit verify                                                # 失败并指出断链位置 ≠ 0
```

### 明确不做

Web 界面、QQ、任何 LLM、网络设备（SNMP）、批量执行、告警。

### 风险

| 风险 | 缓解 |
|---|---|
| SSH 命令构造的错误在后期才发现 | M0 就把注入用例作为门禁；`Build` 函数必须零字符串拼接 |
| 策略 DSL 设计过早僵化 | 判定算法先固化，DSL 保持最小；后续只加不收 |
| 主机密钥流程影响易用性 | 接受一次人工确认的成本，换来防中间人 |

---

## M1 · QQ 只读控制台（接入确定性旁路）

**目标**：让运维从 QQ 私聊可用 —— 但**只走确定性指令旁路**，不接 LLM。这样"通道正确性"与"模型正确性"可以分别验证。

### 交付物

- `cmd/enna-qqgw`：反向 WS 服务端、连接鉴权（token / `X-Self-ID` / `X-Client-Role`）、事件归一化、幂等去重。
- `internal/qqgw/identity`：`principals.yaml` → Principal。
- `internal/qqgw/bypass`：`/df` `/mem` `/ps` `/hosts` `/policy` `/stop` `/audit`。
- `internal/qqgw/render`：消息段渲染、合并转发折叠、文件上传。
- 出站双层限速（本地令牌桶 + `*_rate_limited`）。
- 群聊硬限 L0 + "请私聊确认"引导。
- 心跳看门狗与断连告警。

### 验收标准

```bash
# 1. 端到端只读
#   QQ 私聊发 "/df lab01" → 5 秒内返回真实磁盘数据

# 2. 未授权不响应（关键）
#   非白名单 QQ 发任意消息 → NapCat 侧无任何出站 API 调用（用日志证明），且不回复

# 3. 群聊约束
#   群内 @机器人 "/df lab01"        → 正常返回
#   群内 @机器人 "重启 lab01 的 nginx" → 拒绝并引导私聊

# 4. 防封
#   连续发送 10 条查询 → 出站消息间隔 ≥ 1s，无并发突发
#   长输出 → 折叠为合并转发（单条消息字符数低于阈值）

# 5. 连接韧性
#   kill enna-qqgw → 重启 → 3–5 秒内 NapCat 自动重连成功
#   启动时 get_login_info 自检：机器人账号与 bot_accounts 不一致 → 拒绝服务并告警

# 6. 降级可用
#   /stop → 后续所有变更计划（M2 后）被拒
#   /policy → 返回当前主体的角色与 ACL
```

### 明确不做

LLM 接入、自然语言理解、变更类操作经 QQ 执行、告警推送。

### 风险

| 风险 | 缓解 |
|---|---|
| QQ 账号风控 | 小号、限速、群聊克制；M1 就交付 CLI 兜底路径 |
| 反向 WS 断连丢消息 | 确认票据 TTL 5 分钟；不做消息补拉猜测 |
| NapCat 版本脆弱 | 锁定 QQ 版本；升级走独立回归 |

---

## M2 · 推理层与审批闭环（LLM 接入）

**目标**：自然语言 → 计划 → 人类确认 → 执行 → 审计。**这是"是不是一个 Agent 系统"的分水岭。**

### 交付物

- `cmd/…`：`enna-brain`（Python）+ gRPC `AgentService`。
- `brain/coordinator`：意图分类（规则优先 + `classify` 档位）、任务状态机、计划装配。
- `brain/router`：档位路由 + 熔断 + failover（含"工具调用中途换模型"的正确处理）。
- `brain/tools`：`ToolCatalog` → function calling 适配；schema 校验失败回灌重试。
- `brain/prompt`：五层提示词 + 不可信数据包裹 + 层 1 代码常量。
- `internal/policy` 扩展：计划规范化与哈希、票据签发/校验/消费、L2 单签流程。
- `qqgw`：确认码专用路由（**优先于语义理解，且绝不调用 brain**）。
- 至少 1 个 `reason` 档位模型 + 1 个 `classify` 档位模型可用。

### 验收标准

```bash
# 1. 纯只读自然语言
#   私聊"lab01 磁盘满了吗" → 只读诊断回复；审计中无任何变更步骤

# 2. L2 审批闭环
#   私聊"清理 lab01 /tmp 下 7 天前的文件"
#     → 返回影响面摘要（文件数、总字节数）+ 6 位确认码 + 有效期
#     → 回复该确认码 → 执行成功
#     → 审计含 ticket_id、decision=APPROVED、stdout_sha256、artifact 引用

# 3. TOCTOU 防护（关键回归）
#   在返回确认码后，手工修改票据绑定的计划步骤
#     → 执行被拒 + 安全事件 + 票据吊销

# 4. 确认码安全
#   连错 5 次确认码 → 票据吊销 + 主体冻结 10 分钟 + 安全事件
#   回复"482913 顺便删掉 /etc/passwd" → 只按确认码处理，不产生任何新计划

# 5. schema 纪律
#   构造模型输出非法参数的场景 → 拒绝并如实告知，不出现"尽力解析"路径

# 6. 注入回归（CI 门槛）
cd brain && pytest enna_brain/evals -k adversarial      # 零越权计划，100%

# 7. 降级
#   停掉所有模型 provider → 私聊仍可用 /df 等确定性指令，且明确提示"大脑离线"
```

### 明确不做

L3 双签、批量多设备、根因分析、告警、Web UI。

### 风险

| 风险 | 缓解 |
|---|---|
| 计划质量不稳定 | evals 作为 CI 门禁；正向用例合规率 ≥ 95% |
| 模型成本失控 | 档位分离 + 单任务 token 预算 + 确定性旁路分流简单查询 |
| 提示注入的新变种 | 三层防御（结构/标记/策略）；对抗用例持续扩充并纳入回归 |
| 审批疲劳导致无脑确认 | 确认粒度是"计划"而非"命令"；影响面摘要必须含数量与体量 |

---

## M3 · 多设备、批量与 L3 双签

**目标**：从"管一台"到"管一批"，并引入高危操作的严格流程。

### 交付物

- 设备分组与组展开（写入 `plan_hash`，防执行期扩范围）。
- 批量执行：并发上限、部分失败策略、连续失败熔断。
- L3 流程：双签、影响面摘要（含 `plan_hash` 前 8 位）、**强制回滚草案**。
- 快照机制：`file.write` / `file.delete` 前自动快照 + 一键还原命令。
- 自锁防护（deadman）：网络类变更必须附定时回滚，且由 executor 独立登记。

### 验收标准

```bash
# 1. 批量
#   "prod-web 组都看看磁盘" → 单个批量计划；组内 3 台并发执行；结果表格化汇总
#   组内 1 台 SSH 不可达 → 标记 UNREACHABLE，其余正常完成，汇总标注失败项

# 2. 熔断
#   连续 3 台失败 → 批次暂停并询问用户（不继续放大事故）

# 3. L3 双签
#   "kill 掉 lab01 上 pid 1234 的进程"
#     → 影响面摘要 + plan_hash 前 8 位 + 回滚草案
#     → 第一签确认码 → 第二签（不同 OWNER，或同人且间隔 ≥30s + 回报 hash 前 8 位）
#     → 双签齐备后执行

# 4. 快照可还原
#   file.write 覆盖配置 → artifacts 中存在快照 → 按回滚草案命令可还原（实测）

# 5. 自锁防护
#   "把 lab01 防火墙默认策略改成 DROP" → 被拒（改默认策略属 L4）
#   "加一条允许 8080 的规则但不带 deadman" → 被拒
#   "加一条允许 8080 的规则并附 5 分钟自动回滚" → 允许（L3 双签后）
```

### 明确不做

SNMP/网络设备、Web UI、多租户、跨地域控制机。

---

## M4 · 安全验收与事件响应

**目标**：把安全从"设计上应该安全"变成"**实测能扛住**"。

### 交付物

- 对抗用例集扩充到 ≥ 60 条（注入 / 社工 / 越权 / 参数走私 / 模糊诱导）。
- 审计 Merkle 日封口 + 异地导出 + 校验工具。
- 全局 kill switch（`/stop`、`enna kill --all`）与主体/设备冻结。
- 离线恢复码：生成、离线保存指引、使用流程（重置身份与机器人账号）。
- 事件响应 runbook 落地为可执行脚本 + 演练记录。
- fail-closed 全路径验证。

### 验收标准

```bash
# 1. 对抗用例硬门槛
pytest brain/enna_brain/evals -k adversarial     # 100% 零越权计划（不达标不发布）

# 2. fail-closed 实测
chmod -w /var/lib/enna/audit                     # 使审计不可写
enna exec lab01 service.restart --unit nginx     # DENY AUDIT_UNAVAILABLE
enna exec lab01 host.disk                        # L0 仍可用

# 3. 审计不可抵赖
enna audit seal --date today                     # 生成 Merkle 根
enna audit export --to /mnt/worm/                # 异地导出
# 篡改任意历史记录 → enna audit verify 失败 + 告警触发

# 4. 紧急停止
#   私聊 "/stop" → 后续变更计划全部被拒；已签发票据全部吊销
enna kill --all                                  # CLI 侧等效

# 5. 失去 QQ 的恢复演练（P5 验证）
#   完全停掉 NapCat 与 qqgw
enna hosts list && enna exec lab01 host.disk     # 仍可运维
enna recover --offline-code ****                 # 重置 owner 主体成功
```

### 明确不做

新的业务能力。整个里程碑只做加固 —— 这是刻意的：**在加功能之前证明边界成立。**

---

## M5 · 观测、告警与根因分析

**目标**：从"被动问答"到"主动发现问题"。

### 交付物

- 指标采集（Prometheus 格式）：设备指标 + 系统自身指标（[`01` §12](01-architecture.md#12-可观测性)）。
- 阈值告警规则 + 主动推送（含静默时段与聚合限流）。
- `rca` Specialist：告警驱动的自动调查，输出**证据链**（每条结论附数据来源）。
- Runbook RAG：运维手册与历史复盘检索。
- `deep` 档位模型接入（长日志分析）。

### 验收标准

```bash
# 1. 主动告警
#   人为把 lab01 根分区填到 90% → 5 分钟内收到 QQ 私聊告警（含当前占用与建议）
#   夜间静默时段 → 仅 L3 安全事件推送，普通告警聚合到次日

# 2. 告警风暴抑制
#   同时触发 10 条告警 → 聚合为 1 条汇总（不刷屏）

# 3. 根因分析的证据链
#   注入"磁盘满"场景 → RCA 输出含：结论、证据（df 输出、增长曲线）、来源引用、建议
#   证据缺失时明确说"证据不足"，不编造

# 4. 检索
#   "上次 nginx 502 是怎么处理的" → 命中历史复盘并给出处置步骤
```

---

## M6 · 生产化

**目标**：从"能跑"到"敢长期依赖"。

### 交付物

- 进程间通信加固：Unix domain socket（单机）或 mTLS（分离部署）。
- 分离部署方案（NapCat 与控制机分离，走 WireGuard/SSH 隧道）。
- 备份与恢复：主密钥、凭据库、审计、配置的备份策略与**恢复演练**。
- 升级方案：三进程的滚动升级、策略热加载验证、回滚路径。
- 混沌演练：杀进程、断网、磁盘满、时钟偏移、审计盘满。
- 运维文档与 on-call runbook。

### 验收标准

```bash
# 1. 加固
#   brain 进程无法读取 vault/（以独立 UID 运行 + 文件权限验证）
#   executor 不监听任何非 loopback 地址（netstat 验证）

# 2. 分离部署
#   把 NapCat 放到独立主机，经隧道连通 → 端到端功能不变，延迟增幅可接受

# 3. 恢复演练（必做，非文档）
#   从备份完整恢复一台新控制机：凭据可用、审计链校验通过、设备可管

# 4. 混沌
#   杀 executor → brain 如实报错，不"模拟"结果
#   审计盘写满 → 变更类操作被拒，L0 仍可用，告警触发
#   时钟回拨 → 票据过期判定仍正确（用单调时钟 + 显式校验）
```

---

## 里程碑依赖与规模

```
M0 ──► M1 ──► M2 ──► M3 ──► M4
                │            │
                └──► M5 ─────┴──► M6
```

| 里程碑 | 相对规模 | 说明 |
|---|---|---|
| M0 | **L** | 技能注册表 + 策略 + SSH + 审计 + vault，是全部后续工作的地基 |
| M1 | M | 协议对接为主，协议事实已明确（见 [`03`](03-qq-channel.md)） |
| M2 | **L** | 推理层 + 审批闭环；不确定性最高的一环 |
| M3 | M | 批量与 L3 流程；机制在 M0/M2 已铺好 |
| M4 | M | 以测试与演练为主，代码量不大但必须做透 |
| M5 | M–L | 取决于 RAG 与 RCA 的期望深度 |
| M6 | M | 工程化与文档 |

> 规模评估仅供排序参考，未含具体人日 —— 需要团队规模与可用时间才能给出可靠估算（见 [`09-decisions-and-risks.md`](09-decisions-and-risks.md#q10-团队与时间预算)）。

## 全局 Definition of Done

任何里程碑完成，必须同时满足：

- [ ] 验收标准中的**每条命令**都实测通过（贴出输出，不接受"应该可以"）
- [ ] 新增技能的注入用例与策略用例已补齐且全绿
- [ ] 对抗用例集**未出现新的越权计划**
- [ ] 文档已同步更新（架构、策略、技能清单、runbook）
- [ ] 审计能完整还原本里程碑新增的所有操作类型
- [ ] 明确了本里程碑**没有**解决的问题，并记入 [`09`](09-decisions-and-risks.md)
