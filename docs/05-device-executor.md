# 05 · 设备执行器（enna-executor）

> 这是系统的安全内核：**唯一持有凭据、唯一构造命令、唯一做出授权判定的地方。**

## 1. 不变量

| # | 不变量 |
|---|---|
| I1 | 技能集合在启动期静态注册，运行期不可新增/不可动态加载 |
| I2 | 任何真实命令都由技能代码从**已校验的参数**构造；不存在"把模型输出当命令执行"的路径 |
| I3 | 风险等级由技能代码声明；策略文件只能**下调** |
| I4 | 一切 L1 以上动作在审计落盘成功后才执行（fail-closed） |
| I5 | 凭据只在本进程内存中存在最小必要时间；不落日志、不进 RPC |
| I6 | 同设备的变更类操作串行执行 |
| I7 | 未注册设备一律拒绝，不提供"临时连一台"的口子 |

## 2. 技能注册表

### 2.1 技能描述

```go
type RiskLevel int  // L0Read, L1SafeWrite, L2Mutate, L3Dangerous, L4Forbidden

type Skill struct {
    Name        string            // "service.restart"，点分命名
    Summary     string            // 给模型看的一句话（会进 ToolCatalog）
    Params      *jsonschema.Schema// 参数校验，strict（拒绝未声明字段）
    Risk        RiskLevel
    TargetKind  TargetKind        // HOST | CONTAINER | FILE | NONE
    Timeout     time.Duration
    OkExitCodes []int             // 例如 grep 无匹配返回 1 也算成功
    RollbackHint string           // L2+ 必填：如何回滚，或"不可自动回滚"
    Build       func(target Target, args Args) (Command, error)  // 命令构造，唯一出口
    Parse       func(out Output) (any, error)                    // 结构化输出
}
```

### 2.2 为什么技能名是点分命名而非工具名

`service.restart` 这样的名字**自带分类语义**，便于策略书写（`service.*`）、风险归组与审计聚合。同时它对人可读 —— 审计日志里出现 `service.restart` 比 `exec_tool_7` 有用得多。

### 2.3 命令构造是唯一出口（I2）

```go
// 唯一允许产生命令的地方。没有第二个出口。
type Command struct {
    Bin    string    // 绝对路径，如 "/usr/bin/systemctl"
    Args   []string  // 逐个参数，绝不含未转义的整串
    Sudo   bool      // 是否需要 sudo -n
    Stdin  []byte
}
```

`Build` 的输入是**已过 JSON Schema 的参数**，输出是结构化 argv。审计记录 `Bin + Args` 的哈希与全文（脱敏后）。

## 3. 命令构造规则（关键工程细节）

### 3.1 SSH 协议的现实约束

SSH 的 `exec` 请求负载是**单个字符串**，远端 `sshd` 会把它交给用户的登录 shell 执行（等价于 `sh -c "<string>"`）。协议层没有 argv 数组的传递方式。这意味着**远端一定会经过一次 shell 解析**，我们无法绕开。

因此防御必须在**我们这一侧**做，且必须做到位：

### 3.2 五条构造规则

| # | 规则 | 说明 |
|---|---|---|
| R1 | **参数不来自自由文本** | 参数来自 JSON Schema 约束的枚举、整数、或强正则字符串。模型无法传入任意字符串 |
| R2 | **白名单正则校验字符串参数** | 如 `unit`: `^[A-Za-z0-9_.@:-]{1,64}$`；`container`: `^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`；`signal`: 枚举 `TERM\|KILL\|HUP\|USR1\|USR2` |
| R3 | **路径规范化 + 前缀断言** | `filepath.Clean` → 断言落在允许前缀内（如 `/var/log/`、`/tmp/`）→ 拒绝含 `..`、符号链接逃逸、NUL、换行 |
| R4 | **POSIX 单引号转义** | 任何进入命令串的字符串：`'` → `'\''`，并**拒绝**含 `\n`、`\r`、`\x00` 的输入 |
| R5 | **二进制走绝对路径，禁用 shell 内建** | `Bin` 必须是绝对路径白名单内的可执行文件；禁止 `sh`/`bash`/`eval`/`env`/`xargs` 作为 `Bin` |

**反例（禁止写法）**：

```go
// ❌ 灾难：模型可控的字符串直接拼进 shell
cmd := fmt.Sprintf("systemctl restart %s", args["unit"])
cmd := "grep " + userPattern + " " + logPath
cmd := fmt.Sprintf("journalctl -u %s --since '%s'", unit, since)
```

**正例**：

```go
func buildServiceRestart(t Target, a Args) (Command, error) {
    unit, err := validateUnit(a.Unit)      // R2 白名单正则
    if err != nil { return Command{}, err }
    if !slices.Contains(allowedUnits, unit) {  // 二次收口：只允许清单内的服务
        return Command{}, ErrUnitNotAllowed
    }
    return Command{
        Bin:  "/usr/bin/sudo",
        Args: []string{"-n", "/usr/bin/systemctl", "restart", unit},  // argv，无 shell 拼接
        Sudo: true,
    }, nil
}
```

`unit` 的**双重收口**（正则 + 设备级服务白名单）是刻意的：正则防注入，白名单防"合法但错误的目标"。

### 3.3 服务的设备级白名单

`devices.yaml` 允许为每台设备声明可管理的服务：

```yaml
- id: web01
  addr: 10.0.0.11:22
  groups: [prod-web]
  credential_ref: web01-key
  managed_units: [nginx, php-fpm, redis]     # service.* 技能只能作用于这些单元
  allowed_paths: [/var/log/nginx, /tmp]      # file/log 技能只能作用于这些前缀
```

**这是最有价值的第二层防御**：即使模型幻觉出一个合法格式的服务名，只要不在 `managed_units` 里就会被拒。

## 4. 首批技能清单

风险等级标注遵循一个明确原则：**看爆炸半径，而不只看可逆性**。`tmp.prune`（限定 `/tmp`、限定时长、只删普通文件、先干跑列清单）爆炸半径有界，故为 L2；而通用 `file.delete` 无界，故为 L3。

### 4.1 L0 · 只读观测（25 项）

| 技能 | 参数 | 实现要点 |
|---|---|---|
| `host.info` | — | `uname -a`、`/etc/os-release`、`uptime` |
| `host.load` | — | `/proc/loadavg` + `nproc`（算归一化负载） |
| `host.mem` | — | `/proc/meminfo`（含 available，比 `free` 更准） |
| `host.disk` | — | `df -B1 -x tmpfs -x devtmpfs` |
| `host.disk.dir_usage` | `path`（白名单前缀）, `depth`(1–2) | `du -x -B1 --max-depth`；**重操作，超时 120s** |
| `host.cpu.snapshot` | — | `/proc/stat` 两次采样算使用率 |
| `host.net` | — | `ip -j addr`、`ip -j route` |
| `host.sockets` | `proto`(枚举 tcp/udp/all), `state` | `ss -H -tulpn` |
| `host.netstat_summary` | — | `ss -s` |
| `host.uptime.reboots` | `count`(1–20) | `last -x reboot \| head` |
| `process.top` | `by`(cpu/mem), `limit`(1–50) | `ps -eo pid,ppid,user,pcpu,pmem,etime,comm --sort=-%cpu` |
| `process.find` | `name`(正则白名单), `user` | `pgrep -a`；**不接受任意正则**，仅允许 `[A-Za-z0-9_.-]{1,32}` |
| `process.tree` | `pid`(整数) | `ps -ef --forest` 过滤 |
| `service.status` | `unit`（受管清单） | `systemctl show -p ... --no-pager` |
| `service.list` | `state`(枚举) | `systemctl list-units --type=service` |
| `service.failed` | — | `systemctl --failed` |
| `log.journal` | `unit`, `lines`(1–500), `since`(枚举: 1h/6h/24h/7d) | `journalctl -u … -n … --since …` |
| `log.file` | `path`（白名单）, `lines`(1–500) | `tail -n`；拒绝二进制 |
| `log.grep` | `unit`/`path`, `pattern`（受限字符集）, `lines` | 参数化，**禁用管道拼接** |
| `container.ps` | `all`(bool) | `docker ps --format json` |
| `container.logs` | `name`, `lines` | `docker logs --tail` |
| `cron.timers` | — | `systemctl list-timers` + `crontab -l` |
| `security.logins` | `failed`(bool), `lines` | `last` / `lastb` |
| `net.tcp_check` | `host`(内网白名单), `port`(1–65535) | `nc -z -w3`；**目标受 allowlist 约束**，防被用作内网扫描器 |
| `net.ping` | `host`(内网白名单), `count`(1–5) | `ping -c`；同上 |

### 4.2 L1 · 低危写（2 项）

| 技能 | 参数 | 说明 |
|---|---|---|
| `artifact.write` | `name`, `content` | 写入 `/var/lib/enna/artifacts/`（控制机本地），路径经 Clean + 前缀断言 |
| `enna.self_prune` | `keep_days`(≥1) | 只清理 enna 自己的临时目录 |

### 4.3 L2 · 可逆变更（6 项）

| 技能 | 参数 | 回滚提示 | 备注 |
|---|---|---|---|
| `service.restart` | `unit` | 重启本身即恢复动作；失败则 `service.status` 诊断 | 只允许 `managed_units` |
| `service.reload` | `unit` | 失败时回退到 restart 或重启前配置 | 更温和，优先于 restart |
| `service.start` | `unit` | `service.stop` | 仅当当前为 inactive |
| `container.restart` | `name` | 再次 restart | |
| `container.start` | `name` | `container.stop`(L3) | |
| `tmp.prune` | `path`（限 `/tmp`,`/var/tmp`）, `older_than_days`(≥1), `pattern`(受限) | **不可自动回滚** —— 因此强制先干跑并列出文件清单 | 硬约束见下 |

`tmp.prune` 的强制实现约束：

```
find <path> -xdev -type f -mtime +<N> -name '<pattern>' -print   # 干跑
   → 文件清单进入"影响面摘要"（含数量与总字节数）
   → 人类确认后才执行 -delete
```
- `-xdev` 不跨越文件系统边界
- **不跟随符号链接**（避免 `-L` 带来的越界删除）
- 拒绝 `path` 为 `/`、`/etc`、`/var/log`、`/home`
- 单次删除文件数或总字节数超过阈值（默认 5000 个 / 5GB）→ 自动升级为 L3

### 4.4 L3 · 高危变更（11 项）

| 技能 | 参数 | 双签 | 回滚草案 | 备注 |
|---|---|---|---|---|
| `process.kill` | `pid` 或 `name`, `signal`(枚举) | ✅ | 重启对应服务（若受管）| 执行前必须 `process.tree` 确认目标，摘要中列出将被影响的进程 |
| `service.stop` | `unit` | ✅ | `service.start` | |
| `container.stop` / `container.kill` | `name` | ✅ | `container.start` | |
| `file.delete` | `path`（白名单前缀） | ✅ | **先复制到 artifacts 快照**，摘要给出还原命令 | 强制快照后才允许删除 |
| `file.write` | `path`, `content`, `mode` | ✅ | 覆盖前存快照（`file.snapshot` 自动前置） | 配置文件类变更 |
| `package.install` / `package.remove` | `name`（来自仓库查询结果，非自由文本） | ✅ | 记录变更前后包清单 `package.snapshot` | |
| `sysctl.set` | `key`（白名单）, `value` | ✅ | 记录旧值 | 内核参数 |
| `firewall.rule.add` | 结构化字段（非命令行） | ✅ | 给出对称删除命令 | 见 §4.5 自锁规则 |
| `cron.add` | `schedule`(结构化), `command`（仅受管脚本绝对路径） | ✅ | 给出删除指引 | 不接受任意命令 |

### 4.5 L4 · 禁止（部分示例）

完整清单见 [`02-security-model.md` §7](02-security-model.md#7-l4-禁止清单示例代码级硬编码)。要点：

- `reboot` / `shutdown` / `halt`
- 修改 `sshd_config` / `authorized_keys` / `sudoers` / `/etc/shadow`
- 修改防火墙**默认策略**（允许加规则，不允许改默认策略）
- `mkfs` / `dd of=/dev/*` / `wipefs` / `fdisk`
- `shell.run`（任意命令透传）—— **默认不注册此技能**
- 对不在 `devices.yaml` 中的目标执行任何写操作

**自锁防护**：`firewall.rule.add` 与任何网络可达性相关变更，必须随计划提交**定时回滚**（deadman），例如注册一个 `at now + 5 minutes` 的还原任务，且该还原由 executor 独立登记、不依赖 brain 存活。否则一律 DENY。

## 5. 策略 DSL

`configs/policy.yaml`：

```yaml
version: 1

defaults:
  unknown_skill:      DENY
  device_unreachable: DENY
  audit_unavailable:  DENY          # 风险 >= L1 时
  max_risk_by_channel:
    private: L3
    group:   L0                     # 群聊硬限（主张：不可配置上调）

risk_overrides:                     # ⚠️ 只能下调，加载时校验，上调即拒绝启动
  service.reload: L1
  container.start: L1

principals:
  - qq_id: 10001
    display: "老王"
    role: OWNER
    acl:
      - { group: prod-web, max_risk: L2 }
      - { group: lab,      max_risk: L3 }
  - qq_id: 10002
    display: "值班同学"
    role: OPERATOR
    acl:
      - { group: prod-web, max_risk: L1 }

batch:
  max_concurrency: 5
  abort_on_consecutive_failures: 3   # 疑似共因故障时熔断，防雪崩
```

### 5.1 判定算法（确定性、短路、deny 优先）

```
decide(principal, channel, device, skill, args) -> Decision

 1. principal 存在且 enabled?              否 → DENY UNKNOWN_PRINCIPAL
 2. channel 允许该技能的声明风险?           否 → DENY CHANNEL_RESTRICTED
 3. skill 已注册?                          否 → DENY UNKNOWN_SKILL
 4. skill ∈ forbidden?                     是 → DENY FORBIDDEN_SKILL
 5. device 存在且 ∈ principal.acl?         否 → DENY NO_DEVICE_ACL
 6. device 状态 != FROZEN?                 否 → DENY DEVICE_FROZEN
 7. effective = min(skill.risk, override, acl.max_risk)      # 只收紧
 8. args 过 skill.Params schema（strict）? 否 → DENY INVALID_ARGS
 9. 技能特有收口（managed_units / allowed_paths / 目标 allowlist）? 否 → DENY TARGET_NOT_ALLOWED
10. effective >= L1 且审计不可用?          是 → DENY AUDIT_UNAVAILABLE
11. effective == L0 → ALLOW
    effective == L1 → ALLOW（全量审计）
    effective == L2 → NEED_APPROVAL(sigs=1, ttl=5m)
    effective == L3 → NEED_APPROVAL(sigs=2, ttl=5m, rollback_required=true)
    effective == L4 → DENY FORBIDDEN_SKILL
```

**任何一步 DENY 立即短路**，不继续判定 —— 避免"部分通过"导致的实现分歧。

### 5.2 策略加载

- 启动时加载并**校验**（上调风险的 override 直接拒绝启动）。
- 运行期 `SIGHUP` 或 `enna policy reload` 热加载；加载失败保留旧策略（**不回退到宽松**）。
- 每次加载记录策略文件哈希到审计 —— 事后能证明"当时用的是哪版策略"。

## 6. SSH 传输层

| 关注点 | 设计 |
|---|---|
| 连接池 | 每设备最多 2 条连接，空闲 60s 回收；keepalive 30s |
| **主机密钥** | 严格校验。**首次连接不做 TOFU** —— 必须先 `enna device trust <id> <fingerprint>` 由人工确认。指纹变更即拒绝连接并告警（防中间人） |
| 超时 | 建连 5s；默认执行 15s；技能可声明更长（`host.disk.dir_usage` 120s、`log.*` 30s） |
| 认证 | 优先 ed25519 密钥；支持密码（过渡）；`sudo -n`（非交互，无密码提示） |
| 跳板机 | 支持 `ProxyJump`；跳板机凭据独立管理 |
| 转发 | **默认全部关闭**（no agent forwarding / no port forwarding / no X11）。避免把控制机变成跳板 |
| 并发 | 同设备变更串行（每设备互斥锁）；L0 读并发 ≤ 8 |
| 连接失败 | 标记设备 `UNREACHABLE` 并跳过批次的后续步骤（**不重试变更类操作**，防半执行） |
| 录屏式审计 | 每个技能记录 `Bin + Args`、退出码、stdout/stderr 哈希与全文（脱敏） |

## 7. 输出处理

### 7.1 双通道

```
技能执行输出
  ├─► 全文（脱敏后，gzip）→ artifacts/YYYY-MM-DD/<task_id>/<step>.txt.gz
  │                         审计只记录 sha256 + 引用路径
  └─► 摘录（≤ 8KB，脱敏）  → 返回给 brain 进入 LLM 上下文
```

**为什么必须分离**：一次 `journalctl -n 500` 可能有 200KB。全量进上下文会同时打爆成本与有效注意力，而完全不给出细节又无法诊断。摘录 + 全文引用是唯一可行的平衡。

### 7.2 截断策略

| 输出大小 | 处理 |
|---|---|
| ≤ 8KB | 原样 |
| 8KB–256KB | **头 4KB + 尾 4KB**，中间标注 `… 省略 N 字节（可通过 artifact.read 分段查看）…` |
| > 256KB | 只给**结构化摘要**（行数、匹配数、时间范围、首尾各 20 行）+ 文件引用 |

判定为二进制（含 NUL 或高比例不可打印字节）→ 不给内容，只给类型与大小。

### 7.3 脱敏与 stdout/stderr

- 脱敏器见 [`02-security-model.md` §8.3](02-security-model.md#83-脱敏)，**在落盘与入上下文两处都执行**（防绕过）。
- `stderr` 独立字段：模型常见错误是把 stderr 的告警当成失败原因。
- 退出码语义：技能声明 `OkExitCodes`（如 `log.grep` 无匹配返回 1 属正常），executor 据此判定成功，**避免模型把"没有匹配"误读为"命令失败"**。

## 8. 设备模型

```go
type Device struct {
    ID            string
    Addr          string        // host:port
    Groups        []string
    Tags          []string
    CredentialRef string        // 引用，非明文
    JumpHost      string        // 可选
    ManagedUnits  []string
    AllowedPaths  []string
    Status        Status        // OK | UNREACHABLE | FROZEN | UNTRUSTED
    LastSeen      time.Time
    Fingerprint   string
}
```

| 状态 | 含义 | 行为 |
|---|---|---|
| `OK` | 正常 | 全部允许 |
| `UNREACHABLE` | SSH 不可达 | 拒绝该设备的所有技能（含 L0），避免堆积超时 |
| `FROZEN` | 人工冻结 | 拒绝一切操作；用于事件响应 |
| `UNTRUSTED` | 指纹未确认或已变更 | 只允许 CLI 侧的信任操作 |

**不提供"临时连接任意主机"的能力（I7）**。新设备必须经 `enna device add` 写入配置并人工确认指纹。这是防止"模型诱导加一台攻击者控制的机器"的关键。

## 9. 批量执行语义

| 关注点 | 设计 |
|---|---|
| 目标展开 | `device_group` → 设备列表，在**计划生成时**展开并写入 `plan_hash`（防执行期扩大范围） |
| 并发 | `batch.max_concurrency`（默认 5）；同设备内串行 |
| 部分失败 | 默认 `continue`：逐台记录结果，最后汇总"3 成功 / 1 失败"；有依赖的序列用 `abort_on_error` |
| 熔断 | 连续 3 台失败 → **暂停批次并询问**（很可能是共因故障，继续执行会放大事故） |
| 幂等 | L0 可重试；L2/L3 **不自动重试**（半执行比失败更糟） |
| 汇总呈现 | 表格化：设备 × 结果 × 关键指标；失败项单独列出，附错误摘要 |

## 10. 新增技能的准入清单（Code Review 必查）

- [ ] `Name` 是否符合 `域.动作` 命名，是否与既有技能语义重叠？
- [ ] `Risk` 是否按**爆炸半径**评定并有书面理由？
- [ ] `Params` schema 是否 `strict`（拒绝未声明字段）？字符串参数是否都有白名单正则？
- [ ] `Build` 是否只使用绝对路径 `Bin` 与 argv 数组？是否**零字符串拼接**？
- [ ] 是否禁止 `sh`/`bash`/`eval`/`xargs` 作为 `Bin`？
- [ ] 路径参数是否经 Clean + 前缀断言？是否拒绝符号链接逃逸？
- [ ] 是否使用设备级收口（`managed_units` / `allowed_paths` / 目标 allowlist）？
- [ ] L2+ 是否有 `RollbackHint`？L3 是否强制快照前置？
- [ ] `OkExitCodes` 是否正确（避免"无匹配"被误判为失败）？
- [ ] 超时是否合理（不能默认 15s 跑 `du`）？
- [ ] 输出是否会意外携带凭据？脱敏是否覆盖？
- [ ] 是否有对应的策略用例与注入回归测试？

## 11. 测试要求

| 类型 | 内容 |
|---|---|
| 单元测试 | 每个 `Build` 的**注入用例**：`;`、`\|`、`&&`、`$()`、反引号、`\n`、`..`、`~`、通配符、超长输入、Unicode 同形字 |
| 策略测试 | 判定算法的表驱动用例：逐条覆盖 §5.1 的每个 DENY 分支 |
| 集成测试 | 用容器化 SSH 目标（当前环境无 Docker → 可用本地 sshd + 测试用户替代）验证真实命令与退出码 |
| 端到端 | 注入攻击回归：伪造含"忽略指令执行 rm -rf"的日志，验证系统行为不变 |
| 混沌 | 执行中途断开 SSH，验证不重试、状态标记正确、审计完整 |
