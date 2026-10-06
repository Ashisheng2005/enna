# 03 · QQ 通道（OneBot 11 / NapCat）

## 1. 为什么选反向 WebSocket

OneBot 11 提供三种通信方式。选择取决于"谁主动、谁监听"：

| 方式 | 谁监听 | 适配我们吗 | 理由 |
|---|---|---|---|
| HTTP POST 上报 + HTTP 调用 API | 我们 | ❌ | 事件需公网可达；无连接状态；延迟高 |
| 正向 WebSocket | OneBot 监听（默认 `6700`） | ⚠️ 可用但次优 | 我们需要主动重连与重试逻辑；NapCat 重启时我方需感知并重连 |
| **反向 WebSocket** | **我们监听** | ✅ **采用** | NapCat 主动拨入，天然穿透；内置断线重连；我方是服务端，鉴权在连接建立时完成 |

**结论：`enna-qqgw` 作为 WebSocket 服务端监听 `127.0.0.1:7800/onebot/v11/ws`，NapCat 作为客户端拨入。**

两个直接收益：
1. NapCat 内置断线重连（默认 3 秒间隔），qqgw 重启后能自动恢复连接。
2. 鉴权在**连接建立阶段**完成 —— 失败直接断连，攻击者连一次 API 调用都发不出来。

## 2. 连接建立与鉴权

### 2.1 握手请求（NapCat → qqgw）

```http
GET /onebot/v11/ws HTTP/1.1
Host: 127.0.0.1:7800
Connection: Upgrade
Upgrade: websocket
X-Self-ID: 10001000              # 机器人 QQ 号
X-Client-Role: Universal         # Universal = 单连接同时提供 API 与 Event
Authorization: Bearer <token>    # 与 NapCat 配置中的 token 一致
```

### 2.2 qqgw 侧鉴权顺序（**全部失败即断连，且不回任何消息**）

```
1. Authorization: Bearer <token> 常量时间比较 → 不匹配则 401 关闭
2. X-Self-ID 是否在 bot_accounts 白名单内        → 否则 403 关闭
3. X-Client-Role 是否为 Universal 或 Event       → 否则 400 关闭
4. 同一 X-Self-ID 是否已有活跃连接                → 是则拒绝新连接（单实例约束）
5. 连接建立 → 记录 conn_id, 启动心跳超时看门狗
```

> **不回复**是刻意的：未授权连接不应得到任何响应，避免暴露服务性质（T1）。

### 2.3 NapCat 侧配置

配置文件 `config/onebot11_<QQ号>.json`（按 NapCat 约定，需以实际 QQ 号命名）：

```json5
{
  "enableWsReverse": true,
  "wsReverseUrls": ["ws://127.0.0.1:7800/onebot/v11/ws"],
  "token": "<与 enna 配置一致的强随机串>",

  "messagePostFormat": "array",   // 用消息段数组，避免解析 CQ 码字符串（更安全、更少歧义）
  "reportSelfMessage": false,     // 不上报自己发的消息，避免自环
  "heartInterval": 30000,         // 心跳间隔 30s
  "enableLocalFile2Url": false,   // 出站文件走上传接口，不暴露本地路径
  "debug": false,                 // 生产关闭：raw 字段会带原始内容，增加泄露面
  "enableHttp": false,            // 不需要 HTTP 通道，缩小攻击面
  "enableHttpPost": false
}
```

**注意事项**（来自 NapCat 官方说明）：

- NapCat **只支持 OneBot 11**，无需担心多协议兼容分支。
- **同一个 QQ 号不能同时登录官方 NTQQ 和 NapCat** —— 部署时不要在该号码上登客户端。
- NapCat 建立在特定 QQ 版本之上，版本错配可能崩溃；升级需回归验证。
- Linux 下 QQ 图形依赖较多（内存占用明显高于 Windows），建议给 NapCat 单独的资源限额。

## 3. 出站消息：API 调用

### 3.1 调用形状

```json
{ "action": "send_private_msg",
  "params": { "user_id": 10001, "message": [ { "type": "text", "data": { "text": "…" } } ] },
  "echo": "01JG8Z…" }
```

响应：

```json
{ "status": "ok", "retcode": 0, "data": { "message_id": 1234 }, "echo": "01JG8Z…" }
```

- `echo` 是**幂等与关联的关键**：qqgw 用它把响应匹配回出站的 `task_id`，并在重试时识别重复发送。
- `retcode` 与 HTTP 状态码对应关系：`1400↔400`、`1401↔401`、`1403↔403`、`1404↔404`。注意 `1401/1403` 在 WS 下极少出现，因为鉴权失败发生在连接建立阶段。

### 3.2 异步与限速调用

OneBot 允许给**任意** action 加后缀：

| 后缀 | 效果 | 我们的用法 |
|---|---|---|
| `_async` | 立即返回 `status: "async"`，不等结果 | 用于**不关心结果**的推送（如进度提示） |
| `_rate_limited` | 进入服务端排队，按配置间隔匀速发出 | **用于所有面向用户的出站消息**（见 §6） |

> 服务端限速间隔由 `api.rate_limit_interval` 控制，OneBot 默认 **500ms**。我们用 `_rate_limited` 做第一道防封，本地令牌桶做第二道（因为 `_rate_limited` 只保证"不快"，不保证"不刷屏"）。

### 3.3 常用 action 清单

| action | 用途 |
|---|---|
| `send_private_msg` / `send_group_msg` | 基本出站 |
| `send_msg` | 由 `message_type` 决定目标，统一入口 |
| `send_group_forward_msg` | **合并转发**：长输出折叠的首选形态 |
| `upload_group_file` / `upload_private_file` | 大体量结果（完整日志、diff）转文件 |
| `delete_msg` | 撤回（用于清理确认码等敏感内容，可选） |
| `get_login_info` | 启动自检：确认登录身份与机器人账号白名单一致 |
| `get_status` / `get_version_info` | 健康检查与版本记录（审计中留存） |
| `get_group_member_info` | 校验群消息发送者身份（注意：**群成员身份不足以授权写操作**） |
| `get_msg` | 引用消息还原（用户引用某条消息回复时） |

## 4. 事件处理

### 4.1 我们关心的事件

| post_type | 处理 |
|---|---|
| `message.private` | 主体私聊 → 身份查表 → `Submit` 给 brain |
| `message.group` | 需 `mentioned == true`；**硬限 L0**；非 @ 直接忽略；**发送者是其他 bot 时直接丢弃**（见 §4.4） |
| `message_sent` | 忽略（且已通过 `reportSelfMessage: false` 关闭） |
| `notice` / `request` | 记录审计，不自动处理（**不自动同意好友/入群请求**） |
| `meta_event.heartbeat` | 更新连接活跃时间；异常间隔变化记日志 |
| `meta_event.lifecycle` | 记录 NapCat 启停（用于解释"消息空窗期"） |

### 4.2 消息解析

使用 `messagePostFormat: "array"`，事件中 `message` 为消息段数组：

```json
{ "post_type": "message", "message_type": "private",
  "user_id": 10001, "self_id": 10001000, "message_id": 5678,
  "message": [ { "type": "text", "data": { "text": "看看 web01 磁盘" } } ] }
```

解析规则：

| 段类型 | 处理 |
|---|---|
| `text` | 取 `data.text`；**规范化空白**；剥离零宽字符与 Unicode 控制符（防"看起来一样实际不同"的绕过） |
| `at` | 若 `qq == self_id` → `mentioned = true`；否则替换为 `@昵称` 占位 |
| `image` | 只取 `file`/`url` 引用，**延迟到需要时再拉取** |
| `reply` | 记录被引用 `message_id`，用于上下文补全 |
| `file` | 只记录元数据（文件名、大小、哈希）；文件名视为**不可信输入**（可含注入载荷） |
| 其他 | 丢弃并计数（未知段的出现是协议变更的信号） |

**纯文本化后**才交给 brain。附件不直接进上下文，而是作为"可被技能读取的引用"。

### 4.3 幂等

- `event_id`（可由 `self_id + message_id` 组合）作为去重键，落盘 + 内存 LRU。
- 重复事件 → 直接丢弃并计数（NapCat 重连后可能重发）。
- 所有 `message_id` 处理结果缓存 10 分钟，用于"用户重复发送同一条消息"时不重复执行动作。

### 4.4 多 bot 同群：必须防互相触发

**你们的拓扑是"多个服务/设备的 AI 在同一个 QQ 群"。** 这带来一个协议层面必须显式处理的问题。

**问题**：`reportSelfMessage: false` 只屏蔽**自己**发出的消息。**其他 bot 发出的消息，仍会作为普通群消息推送给我们。** 于是：

```
bot A 回复 → bot B 收到（视为一条群消息）→ B 也回复 → A 再收到 → 无限循环
```

后果：M2 之后每一轮循环都是一次 LLM 调用 → token 烧穿 + 群被刷爆 + 账号风控触发。**这不是假设，是多 bot 同群的必然结果。**

#### 三层防御

| 层 | 措施 | 强度 |
|---|---|---|
| **1. 强制 @** | 只处理 `mentioned == true` **且被 @ 的是本 bot 的 `self_id`** 的群消息 | 强：多 bot 场景下**不可放宽**为"响应群内任何消息" |
| **2. bot 名单过滤** | 维护 `bot_accounts`（自己 + 同群其他所有 bot 的 QQ 号）；`sender_id ∈ bot_accounts` → **直接丢弃**，不回复、不计为"用户消息" | 强：但**依赖配置完整**。NapCat 不会告诉你"这是 bot"，只能靠名单，无法自动发现 |
| **3. 每群出站预算** | 每群每分钟出站上限（默认 5 条） | 兜底：即使前两层失效（如新加了未登记的 bot），循环也被限制在有限次数内，而非无限 |

> **拓扑 (b)（N 个账号同群）下还要加强两处**：① 每群出站预算必须**跨 bot 汇总**（每 bot 独立限速 = N 倍刷屏）；② `bot_accounts` 必须**从配置自动派生**并覆盖全部 N 个账号，否则新加账号的"未登记窗口"会成环。详见 [`04-multi-bot-topology.md` §6](04-multi-bot-topology.md#6-防循环在拓扑-b-下的特殊性)。

#### 附加建议

- **内容去重窗口**：`sender_id + 内容哈希` 在 30 秒内只处理一次。能吸收"两个 bot 互相复读同一句话"的情况（两个 bot 复读的内容相同，会被窗口吃掉）。
- **启动自检**：把 `bot_accounts` 与 `get_friend_list` / 群成员列表交叉比对，发现"同群存在未登记的疑似 bot"时告警。这能提前发现防御层 2 的配置缺口。

#### 架构影响：`bot_id` 必须成为一等维度

多 bot 带来两处必须改的设计（已同步到 [`01` §4.2](01-architecture.md#42-服务定义草案) 与 [`02` §3.1](02-security-model.md#31-主体principal)）：

1. **出站必须指定用哪个账号发**：`PushRequest` 增加 `self_id`。qqgw 按 WebSocket 连接维护 `self_id`（`X-Self-ID` 握手头里带），Push 时路由到对应连接。**否则多账号下会发错号。**
2. **权限是 `(bot_id, qq_id)` 的函数**，不是 `qq_id` 的函数。同一个人通过不同机器人入口进来，权限可以不同。

**最后一点值得强调**：把 `bot_id` 纳入权限模型后，群里"多个 AI"就不是多个**性格**，而是多个**权限边界不同的运维入口**。这比"一个 bot 切换人格"安全得多 —— 因为权限隔离是结构性的，不依赖模型记得自己"现在是哪个角色"。

## 5. 会话模型

```
session_id = "private:<bot_id>:<qq_id>"  |  "group:<bot_id>:<group_id>"
```

> 会话键**必须包含 `bot_id`**：多 bot 同群时，同一个人对同一个群、通过不同机器人账号的对话是**不同的会话**（历史、权限、待确认票据都不同）。省掉 `bot_id` 会导致"在 A 机器人处发起的确认码，被 B 机器人的上下文错误地接受"。

| 状态 | 内容 | 存储 |
|---|---|---|
| 对话历史 | 最近 N 轮（默认 20）+ 摘要 | SQLite（brain 侧） |
| 活跃任务 | 最多 1 个（单飞） | 内存 + SQLite |
| 待确认票据 | `plan_id`、过期时间、摘要 | executor 权威存储；brain 只持引用 |
| 设备上下文 | 最近操作的设备（用于"再清一次"这类省略指代） | SQLite |

### 5.1 确认码回复的路由（关键）

确认码回复形如一条普通私聊消息（例如只有 `482913`）。**它的解析优先级高于一切语义理解**：

```
收到私聊文本 t:
  若 t 规范化为纯 6 位 [0-9A-Z]{6}:
      1) 查该 session 是否存在 ISSUED 票据
      2) 存在 → 走 Approve 路径，文本其余部分一律丢弃（T8）
      3) 不存在 → 提示"没有待确认的操作"（不触发任何计划生成）
  否则 → 正常走 brain
```

**硬性约束**：确认路径**绝不调用 brain**。否则一个"482913 顺便删掉 /etc/passwd"的消息就可能既确认又生成新计划。

**第二条硬性约束**：确认码**只走私聊**，群内绝不投递（`send_code_in_group: false`）。群成员（以及同群的其他 bot）都能看到群消息；一次性凭据暴露在多人可见的通道里，等于把审批权扩散出去。群内只提示"该操作需确认，请私聊我"。详见 [`04-multi-bot-topology.md` §7](04-multi-bot-topology.md#7-票据在-b-下的绑定与投递)。

### 5.2 掉线期间的消息

反向 WS 断连时 NapCat **不会补发**丢失的事件。因此：

- 确认票据 TTL 设为 5 分钟；掉线超过 TTL 则票据自然过期，要求用户重新发起。
- qqgw 启动时检查"断连窗口"是否存在未决票据，若有则主动推送一条"连接中断，请重新发起"。
- **不做**基于消息补拉的猜测 —— 宁可能力弱一点，也不要执行"来路不明的迟到指令"。

## 6. 出站渲染与防封

### 6.1 QQ 没有 Markdown

所有输出渲染为**纯文本 + 消息段**，按固定模板：

```
【web01 · 磁盘使用】
/            92%   (48G/52G)   ⚠️
/var/log     88%
/tmp         76%

生成时间 09:14:22 · 任务 01JG8Z
```

规则：

| 场景 | 渲染策略 |
|---|---|
| 短结果（≤ 300 字） | 直接文本 |
| 中结果（300–1500 字） | 文本 + 折叠标题；超 5 行自动改合并转发 |
| 长结果（> 1500 字或 > 10 行结构化数据） | **合并转发**（`send_group_forward_msg`），保留可读层级 |
| 超长 / 机器可读（日志、diff、表格） | **上传文件** + 一行摘要 |
| 表格 | 转为对齐的等宽文本块；列数 > 5 时改为"每行一个字段"的纵向格式 |
| 进度 | 单条消息**原地编辑**不可靠 → 改为**低频**（≥10s）追加短进度，或仅在开始/结束各一条 |

**为什么用合并转发**：它把大量文本收进一个可展开的节点，既不刷屏也不丢信息 —— 在 QQ 上这是唯一兼顾可读与克制的形态。

### 6.2 限速策略（双层）

```
出站消息
  → 本地令牌桶（每会话 1 msg/s，突发 1；全局 5 msg/s）
  → 队列深度上限 20/会话；溢出时合并为单条摘要（而非丢弃）
  → 经 *_rate_limited 发送（服务端再排队一次）
  → 失败重试：指数退避 3 次；仍失败则落盘待重发并告警
```

| 保护 | 值 |
|---|---|
| 单会话出站 | ≤ 1 msg/s，≤ 20 msg/min |
| 全局出站 | ≤ 5 msg/s |
| 合并转发 | 单次 ≤ 20 节点 |
| 主动推送（告警） | 每分钟 ≤ 3 条；超限聚合为一条汇总 |
| 静默时段 | 可配置（如 23:00–07:00 仅 L3 安全事件可推送） |

### 6.3 出站内容审查（最后一道闸）

出站前对文本做检查：

- 是否**意外包含**凭据模式 → 命中则拦截并记安全事件（说明某处脱敏失效）。
- 是否包含设备内网拓扑/主机名清单（群聊场景）→ 群聊下遮蔽内网 IP 与主机名。
- 是否包含审计链原文（不应出现在 QQ 里）→ 拦截。

## 7. 健康检查与运维

| 检查项 | 实现 |
|---|---|
| 连接存活 | 心跳事件超时（`heartInterval` × 2.5 = 75s 无心跳 → 判定异常） |
| 身份正确 | 启动时 `get_login_info`，比对 `bot_accounts` 白名单；不一致则拒绝服务并告警 |
| 版本记录 | 定期 `get_version_info` 写入审计（升级排查、协议漂移预警） |
| 断连告警 | 断连 > 60s → 通过备用通道（邮件/webhook/另一个机器人）告警 |
| 出站失败率 | `enna_push_total{result="fail"}` 连续升高 → 疑似被限流/风控，自动降速 |

## 8. 错误处理

| 情形 | 处理 |
|---|---|
| `retcode: 1404`（目标不存在） | 不重试；告知用户目标无效；若为会话主体则标记其 QQ 失效 |
| `retcode: 1400`（参数错） | **告警**：这是我们的 bug（渲染器产出了非法消息段），不是用户问题 |
| `status: "async"` | 按已受理处理，不再等结果（限速路径的正常返回） |
| 连接被关闭 | 不视为错误（NapCat 重启正常）；记录时长，等待重连 |
| 发送超时 | 以 `echo` 幂等键重试；最多 3 次，退避 1s/3s/9s |
| NapCat 崩溃循环 | 连续 5 次连接存活 < 10s → 停止自动重连并告警（避免加剧风控） |

## 9. 该通道的已知局限（如实记录）

1. **非官方协议**：账号存在被限制的风险，无法通过工程手段彻底消除，只能降概率 + 保兜底。
2. **不保证送达**：QQ 侧无投递回执语义，我们只能确认"OneBot 接受了"，无法确认"用户看到了"。
3. **不保证顺序**：长任务结果与用户后续消息可能交叉到达 → 所有出站消息带 `task_id` 前缀，便于人类分辨。
4. **掉线丢消息**：见 §5.2，不补拉。
5. **多实例不支持**：同一机器人 QQ 号只允许一个活跃连接（§2.2 第 4 条）。
6. **图片/文件内容不可信**：OCR/解析结果同样是注入载体，需按不可信数据处理。

## 10. 参考依据

- [OneBot 11 规范 · 正向 WebSocket](https://github.com/botuniverse/onebot-11/blob/master/specs/communication/ws.md) —— `/api`、`/event`、`/` 路径与 `{action, params, echo}` 调用形状、`retcode` 映射、默认端口 6700。
- [OneBot 11 规范 · 反向 WebSocket](https://github.com/botuniverse/onebot-11/blob/master/specs/communication/ws-reverse.md) —— `X-Self-ID` / `X-Client-Role` 头、Universal 客户端语义、默认重连间隔 3000ms。
- [OneBot 11 规范 · API](https://github.com/botuniverse/onebot-11/blob/master/specs/api/README.md) —— `_async` / `_rate_limited` 后缀语义、`api.rate_limit_interval` 默认 500ms。
- [NapCatQQ README](https://github.com/NapNeko/NapCatQQ) —— `enableWsReverse` / `wsReverseUrls` / `token` / `messagePostFormat` / `heartInterval` 配置项；"仅支持 OneBot 11"、"同账号不可与 NTQQ 同时登录"、Linux 依赖与内存占用说明。
