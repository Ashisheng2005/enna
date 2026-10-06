# 04 · 拓扑 (b) 设计：多 bot 同群

> 你确认的拓扑：**一台设备 = 一个服务/一个 AI = 一个 QQ 账号 = 一个 NapCat 实例，N 个 bot 同处一个群。**
> 本文是对 [`03-qq-channel.md`](03-qq-channel.md) 的拓扑特化，并给出一个可能影响你决策的实务提醒。

---

## 0. 先说一个结论性提醒 ⚠️

拓扑 (b) 在**技术上是干净的**（下文会看到它的架构收益），但它的主要成本**不在代码里，在 QQ 账号的获取与维持上**：

| 成本项 | 实情 |
|---|---|
| QQ 账号获取 | 每个 QQ 号通常需要**独立手机号**验证。5 台设备 = 5 个号 = 5 个手机号 |
| 账号维持 | N 个号都要保活、防回收、防异地登录风控；N 份封号风险 |
| NapCat 资源 | Linux 下每个实例约 **100MB+**（官方 README 说明，图形依赖多）；10 台 ≈ 1GB+ 常驻内存 |
| 登录维护 | 每个实例都要扫码/NTQQ 数据迁移；同号不能与官方 NTQQ 同时登录 |
| 合规 | 使用非官方客户端本身处于腾讯服务条款的灰色地带，规模的 N 倍放大这个风险 |

**而代码层面，(b) 只是 (a) 的超集** —— 因为 `bot_id` 已经是一等维度（[`01` §4.2](01-architecture.md#42-服务定义草案)），多连接与 `self_id` 路由本来就要写。

### 我的建议：**代码支持 (b)，部署从 (a) 起步**

```
代码：实现多连接 + self_id 路由 + bot→device 映射  → 天然同时支持 (a) 和 (b)
部署：先用 1 个账号（拓扑 a）跑通 M1
扩展：需要"每个设备独立身份"时，加账号、加 bots.yaml 条目即可 → 零代码改动
```

**理由**：先写代码再买账号，避免"账号拿到了但系统还不支持"或"支持了但发现账号搞不到"两种浪费。若你已确定能拿到 N 个号，直接按 (b) 部署也完全没问题 —— 代码是同一套。

下面把 (b) 的设计写完，你可以据此决定走哪条。

---

## 1. 拓扑图

```
                         一个 QQ 群（主要界面）
                                  │
        ┌──────────┬──────────┬───┴──────┬──────────┐
        │          │          │          │          │
    NapCat#1   NapCat#2   NapCat#3   NapCat#4   NapCat#5     ← N 个实例
    QQ 10001   QQ 10002   QQ 10003   QQ 10004   QQ 10005
    (web01)    (web02)    (db01)     (cache01)  (gw01)
        │          │          │          │          │
        └──────────┴──────────┴─────┬────┴──────────┘
                                    │  N 条反向 WebSocket
                                    │  ws://127.0.0.1:7800/onebot/v11/ws
                                    │  各带 X-Self-ID 区分身份
                                    ▼
                        ┌───────────────────────────┐
                        │   enna-qqgw（单进程）      │  ← 唯一的接入点，
                        │   连接注册表 by self_id    │     也是唯一的限速点
                        │   出站路由 by self_id      │
                        └───────────┬───────────────┘
                                    │
                        ┌───────────▼───────────────┐
                        │   policy + skill + sshx   │  ← 完全不变
                        │   （M0 已建好的内核）      │
                        └───────────────────────────┘
```

**关键性质**：`enna-qqgw` 是**单进程持有 N 条连接**。这一点非常重要 —— 它是"跨 bot 的全局限速"和"跨 bot 的循环防护"能落地的前提（见 §6）。

---

## 2. 配置模型

```yaml
# configs/bots.yaml
bots:
  - self_id: 10001                    # 机器人 QQ 号（= NapCat 的 X-Self-ID）
    display: "web01 助手"
    device_id: web01                  # ← 拓扑 (b) 的核心：1:1 映射
    groups: [987654321]               # 允许出现在哪些群（白名单，防被拉到别的群）
    allow_private: true

  - self_id: 10002
    display: "db01 助手"
    device_id: db01
    groups: [987654321]
    allow_private: true

group_policy:
  response_mode: mention_only         # 群内必须 @（多 bot 场景下不可放宽）
  max_risk_in_group: L0               # 群聊硬限（沿用 02 §3.3）
  send_code_in_group: false           # ← 群内绝不发确认码（§7）
  per_group_outbound_per_min: 5       # ← 跨全部 bot 汇总，不是每 bot 5 条（§6）
  reconnect_jitter_ms: 800            # 连接退避抖动，防同时重连（§9）
```

**校验规则**（启动时检查，不通过即拒绝启动）：

| 规则 | 理由 |
|---|---|
| `self_id` 全局唯一 | 重复会让连接路由歧义 |
| `device_id` 必须存在于 `devices.yaml` | 防"配了个不存在的设备" |
| `device_id` 在拓扑 (b) 下应唯一 | 一设备一 bot；重复说明你想的是 (a)，应改用 (a) 的配置 |
| `groups` 必须显式列出 | 空列表 = 不响应任何群 = 防止被陌生人拉群 |

---

## 3. 连接注册表与路由

### 3.1 入站

```
NapCat#3 建立连接
  → 握手头 X-Self-ID: 10003, Authorization: Bearer <token>
  → qqgw 校验：token 合法? / 10003 ∈ bots.yaml? / 10003 是否已有活跃连接?
  → 注册：connRegistry[10003] = conn
  → 事件到达时：取 conn.self_id → 查 bots.yaml → 得到 device_id 与 group 白名单
  → 归一化为 MessageEvent{ bot_id: 10003, session_id: "group:10003:987654321", ... }
```

### 3.2 出站（**多账号下最容易出错的地方**）

```go
type PushRequest struct {
    SelfID    uint64    // ← 必须指定用哪个账号发
    SessionID string
    Channel   ChannelKind
    TargetID  uint64
    // ...
}

func (g *Gateway) Push(ctx context.Context, req PushRequest) error {
    conn, ok := g.connRegistry[req.SelfID]
    if !ok {
        return ErrBotOffline            // 该账号的 NapCat 掉线了
    }
    // 用该连接的 OneBot action 发送
    return conn.Call(ctx, "send_msg", params, echoFor(req))
}
```

**必须避免的错误**：在某个"全局唯一连接"上发送，或用"最后一条活跃连接"发送。多账号下这会**把 web01 的答复发到 db01 的账号上** —— 用户看到的是"我 @ 的是 web01，回答我的是 db01"，而更糟的是**确认码可能被发到错误的账号私聊里**。

**测试要求**：Mock 两个连接，断言 `Push{SelfID: 10002}` 只出现在 10002 的连接上。这是 M1 必须有的单测。

### 3.3 会话键

```
session_id = "private:<bot_id>:<qq_id>"  |  "group:<bot_id>:<group_id>"
```

含 `bot_id` 是硬要求（[`03` §5](03-qq-channel.md#5-会话模型)）：同一个人对同一个群、通过不同 bot 的对话是不同会话，历史与待确认票据都不同。

---

## 4. 拓扑 (b) 的架构收益：权限模型变简单了

这是 (b) 真正的好处，值得单独说。

在 `bot ↔ device` 为 1:1 时：

| 项 | 拓扑 (a)（一账号多设备） | **拓扑 (b)（一账号一设备）** |
|---|---|---|
| 用户要说明目标 | 必须说"web01 的磁盘" | **@ 哪个 bot 就隐含了哪台设备** |
| 越权风险 | 模型可能把设备名搞错 | **结构上不可能**：一个 bot 只能操作一台设备 |
| 权限粒度 | `principal × device_group → max_risk` | `principal × bot → max_risk`，天然更细 |
| 提示注入影响面 | 一次注入可波及所有设备 | **一次注入最多波及该 bot 对应的那一台设备** |
| 审计可读性 | 日志里全是 user_id | 日志里 `bot_id` 直接告诉你入口 |

**最重要的一条是第 4 行**：注入的爆炸半径从"整个设备群"收缩到"一台设备"。这是**结构性隔离**（同 [`03` §4.4](03-qq-channel.md#44-多-bot-同群必须防互相触发) 里说的"多个权限边界不同的入口"），比靠提示词约束模型可靠得多。

对应地，`principals.yaml` 简化：

```yaml
principals:
  - qq_id: 10001
    role: OWNER
    bot_scope: [10001, 10002, 10003]   # 可交互的 bot = 可管理的设备（入口即权限）
  - qq_id: 10002
    role: OPERATOR
    bot_scope: [10001]                 # 只能管 web01
```

`device_acl` 的 group 维度仍保留，用于**批量操作**（一次操作一组设备）—— 但那是 M3 的事。

---

## 5. 群聊与私聊的分工（**已决策：方案 A**）

你的场景里群聊是**主要界面**，但 [`02` §3.3](02-security-model.md#33-通道约束重要) 把群聊硬限在 L0。这两者需要调和。

### 5.1 采纳方案：群 = 只读总览，私聊 = 变更通道

```
群里：  @web01助手 磁盘满了吗
        → 正常返回只读结果（L0）

群里：  @web01助手 重启 nginx
        → "该操作需要确认。请私聊我，我会给出影响面与确认码。"
        → 同时（可选）私聊该用户："你刚才请求重启 web01 的 nginx，需要我继续吗？"

私聊：  （重新发起）重启 nginx
        → 影响面摘要 + 确认码 → 确认 → 执行
```

**为什么不在群里直接给确认码**：群成员都能看到码。哪怕群是你自己的私有运维群，也不应把一次性凭据暴露在多人可见的通道里 —— 这是"最小暴露面"的基本要求。

**为什么要重新发起而不是"用群里的计划继续"**：群消息的发送者身份只在群语境下成立，且群里的请求可能被其他人接话干扰。让私聊重新走一遍计划生成，逻辑最简单、审计最清晰。

**代价（必须接受）**：用户要把**每个 bot 都加为好友**才能做变更。N 个 bot = 加 N 次好友。这是 (b) 的主要 UX 摩擦。

> ✅ **本方案已确认采纳（方案 A）。**

### 5.2 未采纳：若群确实完全私有（对应选项 B）

如果你的群**只有你一个人 + N 个 bot**（无其他人类成员），那么群聊的信任级别接近私聊，可以把 `max_risk_in_group` 调到 L2。

但**仍有两条必须保留**：
1. `send_code_in_group: false` —— 确认码走私聊。理由是群里有 N 个 bot，消息会互相可见；一旦某个 bot 被攻陷，它就能看到别人的确认码。
2. 群聊下仍走 L3 双签（若启用 L3）。

**我不推荐**把群聊开放到 L2 以上，即使群是私有的 —— 因为 (b) 的注入面随 N 增长，而群里每个 bot 都会读到其他 bot 的消息（这是协议层的事实，见 §6）。

### 5.3 决策结果

✅ **采纳选项 A**（群 = 只读总览，私聊 = 变更通道，群内不投递确认码）。落地为配置：

```yaml
group_policy:
  max_risk_in_group: L0        # 群聊硬限，不可配置上调
  send_code_in_group: false    # 群内不投递确认码
```

| 选项 | 状态 |
|---|---|
| **A** | ✅ **已采纳** —— 群 L0 只读；变更走私聊；群内不发确认码 |
| B | 未采纳（群完全私有 → 群内允许 L2） |
| C | 保留为后续可选项：若"加 N 次好友"的摩擦被证明不可接受，再评估网页/CLI 确认通道 |

> **注**：即使将来改选 B，`send_code_in_group: false` 仍必须保留 —— 群里有 N 个 bot，消息互相可见，某个 bot 被攻陷就能看到别人的确认码。

---

## 6. 防循环在拓扑 (b) 下的特殊性

[`03` §4.4](03-qq-channel.md#44-多-bot-同群必须防互相触发) 给的三层防御在 (b) 下**需要加强两处**：

### 6.1 `bot_accounts` 名单必须完整，且有"未登记窗口"

N 个 bot 意味着任意两个之间都可能成环，潜在环数是 **O(N²)**。

危险窗口：**新加一个 bot 账号、但还没把它加进其他 bot 的名单时**，它会和已有 bot 互相触发。

对策：
- `bot_accounts` **从 `bots.yaml` 自动派生**（所有 `self_id` 的集合），不手工维护 → 消除"漏加一个"的可能。
- 启动时用 `get_group_member_list` 交叉检查：群里是否存在"看起来像 bot（昵称含'助手'、发言模式固定）但不在 `bots.yaml`"的成员 → 告警。
- 每群出站预算作为最后兜底。

### 6.2 每群出站预算必须**跨 bot 汇总**

```
❌ 错误：每个 bot 独立限 5 条/分钟  →  N 个 bot = 5N 条/分钟  →  10 个 bot 就是 50 条/分钟刷屏
✅ 正确：群维度共享一个令牌桶      →  整个群 5 条/分钟
```

**这一条只能在 `enna-qqgw` 里实现**，因为它是唯一同时持有 N 条连接的进程。如果将来把 qqgw 拆成多进程，这个全局预算就会失效 —— 所以**限速器应该是 qqgw 内的单例，而不是每个连接一个**。这是架构上必须记住的约束。

### 6.3 群内并发响应

群里一条消息 @ 了 3 个 bot（常见于"大家都看看磁盘"），3 个 bot 会同时回复。这不算 bug，但会瞬间产生 3 条消息。

对策：群内出站加**最小间隔**（默认 800ms），让回复排队而不是同时涌出。用户体验上也更可读。

---

## 7. 票据在 (b) 下的绑定与投递

原设计（[`02` §6](02-security-model.md#6-审批票据)）票据绑定 `plan_hash + principal + devices`。(b) 下补两条：

| 项 | 规则 |
|---|---|
| **绑定** | 追加绑定 `bot_id`。票据只在**同一 bot** 的私聊中可被确认（跨 bot 不可认领） |
| **投递** | 确认码**只经私聊**发送（`send_code_in_group: false`）。群里只提示"请私聊我" |
| **认领** | 允许"群内发起只读 → 私聊重新发起变更"，但不允许"群内发起的票据在私聊被确认"（因为群内本就不该产生票据，群聊硬限 L0） |

这样规则保持简单：**票据的产生、投递、确认三者都在私聊里闭环。**

---

## 8. 部署：N 个 NapCat 实例

用 **systemd 模板单元**，别复制 N 份文件：

```ini
# /etc/systemd/system/napcat@.service
[Unit]
Description=NapCat QQ (OneBot 11) for account %i
After=network-online.target

[Service]
Type=simple
User=napcat
WorkingDirectory=/opt/napcat
ExecStart=/opt/napcat/napcat.sh -q %i
Restart=always
RestartSec=10
# 每个实例的配置：/opt/napcat/config/onebot11_%i.json
# 内存上限，防单个实例失控拖垮整机
MemoryMax=512M
# 避免 N 个实例同时重启互相干扰
Slice=napcat.slice

[Install]
WantedBy=multi-user.target
```

```bash
systemctl enable --now napcat@10001 napcat@10002 napcat@10003
systemctl status 'napcat@*'
```

### 资源预算（按 Linux 下每实例约 100MB+ 估算）

| N | NapCat 内存 | 说明 |
|---|---|---|
| 1–3 | ~300MB | 完全无压力 |
| 5 | ~500MB+ | 建议给 `napcat.slice` 设 `MemoryMax` 总上限 |
| 10 | ~1GB+ | 需要认真考虑是否值得，或改用拓扑 (a) |
| 20+ | ~2GB+ | 强烈建议改 (a)：账号成本与资源成本都失控 |

**每实例的 `onebot11_<qq>.json` 关键项**（其余同 [`03` §2.3](03-qq-channel.md#23-napcat-侧配置)）：

```json5
{
  "enableWsReverse": true,
  "wsReverseUrls": ["ws://127.0.0.1:7800/onebot/v11/ws"],
  "token": "<N 个实例共用同一个 token 即可，身份靠 X-Self-ID 区分>",
  "messagePostFormat": "array",
  "reportSelfMessage": false,
  "heartInterval": 30000
}
```

> **token 可以共用**：它只证明"连接来自可信的 NapCat"，身份由 `X-Self-ID` 承担。若想更严格，可给每实例独立 token 并在 qqgw 侧绑定 `self_id ↔ token`，但收益有限（都在 loopback 上）。

---

## 9. 故障模式（(b) 特有）

| 故障 | 影响 | 处理 |
|---|---|---|
| 单个 NapCat 挂 | **只有该设备的入口失联**，其余 bot 正常 | 该 bot 的 `Push` 返回 `ErrBotOffline`；告警；重启后自动重连。**不影响其他设备** ← (b) 的容错优势 |
| 某 QQ 号被封 | 该入口**永久**失联 | 告警 + 该设备降级为 CLI 运维；用离线恢复流程换号（改 `bots.yaml` 的 `self_id`，一条配置） |
| N 个实例同时重连 | 短时间内 N 个连接建立 → 可能被判定异常 | `reconnect_jitter_ms` 抖动退避；连接建立加全局限速（如 1 conn/s） |
| 群被拉入陌生人 | 陌生人可 @ bot 触发 L0 查询（信息泄露） | `groups` 白名单：非白名单群的事件直接丢弃；**群成员变更时告警** |
| 某 bot 被人格/提示注入攻陷 | 该 bot 可能试图指挥其他 bot | 名单过滤使 bot 消息被丢弃 → **bot 之间无法互相指挥**（结构性，不靠提示词） |
| 单台设备 SSH 不可达 | 该 bot 的写操作失败 | 该设备标记 `UNREACHABLE`；只读也拒绝（避免堆积超时） |

> 表格第 1 行和第 5 行是 (b) 的两个实质性优势：**故障隔离**与**注入隔离**。这也是为什么即使账号成本高，(b) 在安全上仍然是有吸引力的选择。

---

## 10. 迁移路径（(a) ↔ (b) 是配置变化，不是重构）

因为 `bot_id` 已是一等维度，两种拓扑共用同一套代码：

```
(a) → (b)：  加 QQ 账号，加 NapCat 实例，bots.yaml 加条目（每条的 device_id 不同）
(b) → (a)：  停掉多余 NapCat，bots.yaml 只留一条，该条的 device_id 置空（允许多设备）
```

**唯一需要代码支持两种模式的点**：

| 配置 | 行为 |
|---|---|
| `bot.device_id` 有值 | **设备锁定**：该 bot 只能操作这一台；消息里不需要也不允许指定其他设备 |
| `bot.device_id` 为空 | **多设备模式**：技能调用必须显式带 `device_id`，走 `device_acl` 判定 |

建议在 `device_id` 有值时，**策略层强制覆盖**目标设备（忽略模型给的其他设备名）—— 这样"设备锁定"是策略层的硬约束，而不是靠模型自觉。

---

## 11. 决策状态

| # | 决定 | 状态 |
|---|---|---|
| 1 | Go module path | ✅ `github.com/Ashisheng2005/enna` |
| 2 | 群聊风险上限（§5.3 的 A/B/C） | ✅ **采纳 A**：群 L0 只读，变更走私聊，群内不发确认码 |
| 3 | 部署走 (a) 还是 (b)（或先 (a) 后 (b)） | ⏳ 待定 —— 建议**先 (a) 跑通 M1，代码支持 (b)**。省钱省事，且不浪费代码 |
| 4 | 上几台设备 / 几个 QQ 号 | ⏳ 待定 —— 决定 NapCat 实例数与资源规划（§8）；若 ≥ 10，更强烈建议 (a) |

以上都**不阻塞 M0** —— M0 是纯 Go 内核，跟 QQ 无关。
