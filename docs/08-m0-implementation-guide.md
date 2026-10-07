# 08 · M0 实现手册（单二进制 · 最简内核）

> 写给**要亲手敲代码的人**。每一步都含：目标 → 要写的文件 → 关键设计 → Go 知识点 → 验收 → 坑。
> 本文档对应 [`07-roadmap.md`](07-roadmap.md) 的 M0，但**做了架构收敛**（见 §0）。

---

## 0. 先收敛架构：M0 只做一个 Go 二进制

### 0.1 与 `01-architecture.md` 的差异（这是刻意的简化）

`01` 描述的是**终态**：三个进程 + gRPC + proto。那是目标，不是起点。

M0 收敛为：

| 项 | 终态（01） | **M0 实际做法** | 理由 |
|---|---|---|---|
| 进程数 | Go×3 + Python×1 + NapCat | **1 个 Go 二进制** | 只有一个进程时，RPC 是纯粹的复杂度 |
| 跨进程契约 | gRPC + Protobuf | **不存在** | 两个进程才需要契约 |
| 技能参数校验 | JSON Schema | **手写 `Validate` 函数** | JSON Schema 的唯一刚需是"给 LLM 描述参数"，模型 M2 才来 |
| CLI 框架 | cobra | **标准库 `flag`** | 先学标准库；cobra 是便利，不是能力 |
| 配置格式 | YAML + pydantic | **YAML + 手写 `Validate()`** | 同上 |
| 通信 | WebSocket + gRPC | **无**（CLI 直接调用） | M1 才接 QQ |

**唯一需要的第三方依赖**：
- `gopkg.in/yaml.v3` —— 配置解析
- `golang.org/x/crypto/ssh` —— SSH
- `filippo.io/age` —— 凭据加密（**M0 可延后**，先靠文件权限 0600 + 后续补）

除此之外全用标准库。

### 0.2 为什么"单二进制"不等于"一坨代码"

关键手法：**用 package 边界预留未来的进程边界。**

```
M0（单进程）                          M2（多进程）
┌────────────────────────────┐        ┌──────────┐   ┌────────────┐
│  enna (一个 Go 二进制)      │        │  enna    │   │   enna-    │
│                            │        │  (Go)    │◄──┤   brain    │
│  internal/cli              │        │          │gRPC│  (Python) │
│  internal/policy     ──────┼──┐     │ qqgw +   │   └────────────┘
│  internal/skill      ──────┤  │     │ executor │
│  internal/sshx       ──────┤  │     │ + policy │
│  internal/audit      ──────┤  │     └──────────┘
│  internal/qqgw   (M1) ─────┘  │
└────────────────────────────┘  │
                                 └─ M2 时：qqgw 变成 gRPC server，
                                    policy/skill 完全不动
```

**只要 `internal/policy` 和 `internal/skill` 不 import `internal/cli`，也不 import `internal/qqgw`，将来拆进程就是零成本的。** 这条纪律叫"依赖单向"，是 M0 最值得练习的架构能力。

### 0.3 M0 完成后的形态

```bash
enna device add lab01 --addr 127.0.0.1:22 --user enna-ops --key ~/.ssh/enna_lab01
enna device trust lab01 --fingerprint SHA256:xxxx
enna exec lab01 host.disk --json        # 真实数据
enna exec lab01 service.restart --unit nginx   # NEED_APPROVAL + 确认码
enna approve <ticket-id> <code>          # 执行
enna audit show --tail 20
enna audit verify                        # OK / 指出断链位置
```

**没有 QQ，没有 LLM，但这已经是一个能用的、有审计的远程运维工具。** 这句话是 M0 的验收本质。

---

## 1. M0 会练到的 Go 能力（学习地图）

| Step | 内容 | Go 知识点 |
|---|---|---|
| 0 | 模块与目录 | `go mod`、package 组织、import 环检测 |
| 1 | 配置加载 | struct tag、`yaml.Unmarshal`、fail-fast 校验 |
| 2 | 审计哈希链 | `crypto/sha256`、`encoding/hex`、`O_APPEND`、`File.Sync` |
| 3 | 设备模型 | 自定义类型 + 方法、`String()` |
| 4 | SSH 传输 | `golang.org/x/crypto/ssh`、`errors.As`、goroutine + channel 超时 |
| 5 | 技能注册表 | **struct-of-funcs**、注册表模式、`init()` 的取舍 |
| 6 | 策略闸门 | 接口、错误 vs 结构化返回值、短路逻辑 |
| 7 | 审批票据 | `crypto/hmac`、`crypto/subtle`、**时间注入以便测试** |
| 8 | CLI 装配 | `flag.NewFlagSet`、`os.Args` 分发、构造注入 |
| 9 | 测试 | table-driven test、`t.Run`、`testing.TB`、模糊测试入门 |

**这份路线本身就是一份 Go 进阶练习清单** —— 不是"顺便学 Go"，而是每一步都刻意选了一个值得掌握的语言特性。

---

## 1.5 本仓库当前的代码状态

**M0 的 Step 0–2 已实现且验证通过** —— 是可直接使用的代码，不是伪代码，也不是骨架。

| 文件 | 状态 |
|---|---|
| `internal/config/config.go` | ✅ 配置加载 + 全部校验规则（含跨平台路径处理） |
| `internal/audit/record.go` | ✅ `Record` / `ComputeHash` / `HashBytes` |
| `internal/audit/log.go` | ✅ `Open`（状态恢复 + 拒绝被篡改的历史）、`Append`（fsync + 毒化）、`Verify` |
| `internal/audit/read.go` | ✅ `Read` |
| `internal/audit/redact.go` | ✅ 脱敏规则 + `Redact` |
| `internal/cli/`、`cmd/enna/` | ✅ 子命令分发（`config check` / `audit verify` / `audit show`） |
| `*_test.go` | ✅ 24 个顶层用例 + 40 个子用例 |
| `configs/enna.example.yaml` | ✅ 含全部安全约束的注释 |

### 验证结果

```bash
go build ./...        # 0
go vet ./...          # 0
go test -race ./...   # ok  internal/audit    ok  internal/config
```

实测通过的端到端行为：

- `enna config check` 输出设备与主体摘要；
- `enna audit verify` 校验链完整性并打印链尾哈希；
- **篡改任意一条记录** → 报"哈希不匹配"并精确定位到条号；
- **抽掉中间一条** → 报 `prev_hash` 断裂；
- **往被篡改的历史上追加** → `Open` 拒绝启动（fail-closed）。

### 建议的阅读顺序

代码注释承担了"为什么这么设计"的说明。按这个顺序读能顺着因果走：

| 顺序 | 文件 | 读什么 |
|---|---|---|
| 1 | `internal/audit/record.go` | 为什么用 struct 而不是 map 做哈希序列化（`ComputeHash` 的 doc 里有三条"为什么"） |
| 2 | `internal/audit/log.go` | `Open` 的状态恢复与"拒绝可疑历史"、`Append` 的 fsync 与"毒化"、`Verify` 的双重校验 |
| 3 | `internal/audit/audit_test.go` | **测试即规格**：篡改 / 删除 / 重开续链分别怎么被抓住 |
| 4 | `internal/audit/redact.go` | 脱敏规则里那两个刻意的决定（`\b` 的坑、分隔符归一） |
| 5 | `internal/config/config.go` | `KnownFields`、`errors.Join`、"配置不能提升权限"的第一道落地 |
| 6 | `internal/config/config_test.go` | 表驱动测试的写法 + 跨平台路径处理（为什么用 `filepath.VolumeName` 而不写死 `/`） |
| 7 | `internal/cli/cli.go` | 标准库 `flag` 的子命令分发模式（子命令的本质就是独立的 `FlagSet`） |

### 值得停下来想清楚的五个问题

代码注释里有答案，但建议先自己答一遍再看：

1. 审计记录为什么**不能用 `map[string]any`** 序列化？
2. `Write` 之后为什么**必须 `Sync`**？
3. 写入失败为什么要把日志**"毒化"**，而不是回滚 `seq` 重试？
4. 路径前缀判断为什么**不能用 `strings.HasPrefix`**？（提示：`/var/logs-evil` 与 `/var/log`）
5. 脱敏规则里 `password` 那组为什么**不加 `\b`**？（提示：`DB_PASSWORD=x`）

### 关于 git 历史的一点说明

`8a76e98`（"参考实现"提交）里**不包含 `internal/audit/`** —— 当时 `.gitignore` 的
`audit/` 规则把整个包静默排除了（详见其后 `58c4d39` 的修正说明）。

所以 **audit 包的可用版本以当前代码为准**，不要用 `git show 8a76e98 -- internal/audit/` 去找答案 —— 那里没有。

---

## 2. 目录结构（M0 精确版）

```
ennaManagement/
├── go.mod                          # module github.com/Ashisheng2005/enna
├── cmd/enna/main.go                # 唯一入口：装配 + 分发
├── internal/
│   ├── config/                     # 配置结构、加载、校验
│   │   ├── config.go
│   │   └── config_test.go
│   ├── audit/                      # 哈希链审计
│   │   ├── record.go               # Record + computeHash
│   │   ├── log.go                  # Open/Append/Verify
│   │   ├── redact.go               # 脱敏
│   │   └── log_test.go
│   ├── device/                     # 设备模型与状态
│   │   └── device.go
│   ├── sshx/                       # SSH 传输（可替换接口）
│   │   ├── target.go
│   │   ├── client.go               # Dial/Run
│   │   ├── command.go              # shellQuote / BuildCommand
│   │   └── client_test.go
│   ├── skill/                      # 技能注册表与技能实现
│   │   ├── skill.go                # Skill 类型、Registry
│   │   ├── host/disk.go            # 第一个技能
│   │   ├── host/load.go
│   │   ├── service/status.go
│   │   ├── service/restart.go      # 第一个 L2
│   │   ├── tmp/prune.go            # 第一个需干跑的技能
│   │   └── inject_test.go          # 注入回归用例
│   ├── policy/                     # 闸门 + 票据
│   │   ├── risk.go                 # Risk 枚举
│   │   ├── decide.go               # Decide 判定算法
│   │   ├── ticket.go               # 票据
│   │   ├── decide_test.go          # 表驱动，覆盖每个 DENY 分支
│   │   └── ticket_test.go
│   └── cli/                        # 子命令实现
│       ├── root.go                 # 分发
│       ├── exec.go
│       ├── device.go
│       ├── audit.go
│       └── approve.go
└── configs/
    ├── enna.yaml
    ├── policy.yaml
    └── devices.yaml
```

> **注意包名**：`skill/host`、`skill/service` 是子包，让技能按域分文件。避免一个包里塞 40 个技能。

---

## 3. 分步实现

每一步都是**可独立验证**的。做完一步就跑一次验收命令 —— 不要写完 8 个文件再一起调试。

### Step 0 · 初始化 ✅ 已完成

```bash
go mod init github.com/Ashisheng2005/enna
go get gopkg.in/yaml.v3
go get golang.org/x/crypto/ssh
go install golang.org/x/tools/cmd/goimports@latest
```

**先建 `.gitignore` 和 `.golangci.yml`**（哪怕 M0 只开两个 linter）：

```yaml
linters:
  enable: [errcheck, govet, staticcheck, ineffassign]
```

> `errcheck` 从第一天就开。Go 新手最常见的 bug 是忽略了返回的 error，而这类 bug 在运维系统里意味着**静默失败**。

**验收**：`go build ./...` 通过（此时无代码，也应成功）。

---

### Step 1 · 配置加载 ✅ 已完成

**文件**：`internal/config/config.go`

```go
type Config struct {
    DataDir string       `yaml:"data_dir"`
    Audit   AuditSpec    `yaml:"audit"`
    Policy  PolicySpec   `yaml:"policy"`
    Devices []DeviceSpec `yaml:"devices"`
}

func Load(path string) (*Config, error)   // 读文件 → Unmarshal → Validate
func (c *Config) Validate() error          // 所有约束集中在这里
```

**关键设计**：

| 决定 | 理由 |
|---|---|
| 配置错误**必须启动失败** | 静默用默认值 = 策略可能比你以为的宽松。运维系统的配置错误应该"响亮地失败" |
| `Validate()` 独立于 `Load()` | 便于测试：不碰文件系统就能测校验规则 |
| 路径统一存**绝对路径** | SSH 与审计都依赖路径；相对路径在换工作目录后会静默失效 |

**Go 知识点**：struct tag 的语法与反射的关系 —— tag 本身不生效，是 `yaml` 库通过反射读取它。

**坑**：`yaml.v3` 默认**不报未知字段**。加 `yaml.Decoder` + `KnownFields(true)`：

```go
dec := yaml.NewDecoder(f)
dec.KnownFields(true)      // ← 拼错的字段名会报错，而不是被静默忽略
if err := dec.Decode(&c); err != nil { ... }
```

这一行能省掉几小时的"为什么我的配置没生效"。

**验收**：故意写错一个字段名 → 启动报错；故意删掉 `data_dir` → 报错。

---

### Step 2 · 审计哈希链 ✅ 已完成（**M0 最有教学价值的一步**）

**文件**：`internal/audit/record.go`、`log.go`、`redact.go`

```go
type Record struct {
    Seq        uint64    `json:"seq"`
    TS         time.Time `json:"ts"`
    TaskID     string    `json:"task_id"`
    Principal  string    `json:"principal"`
    Channel    string    `json:"channel"`
    DeviceID   string    `json:"device_id"`
    Skill      string    `json:"skill"`
    ArgsHash   string    `json:"args_hash"`
    Risk       string    `json:"risk"`
    Decision   string    `json:"decision"`
    DenyCode   string    `json:"deny_code,omitempty"`
    ExitCode   int       `json:"exit_code"`
    StdoutHash string    `json:"stdout_hash,omitempty"`
    Redactions int       `json:"redactions"`
    PrevHash   string    `json:"prev_hash"`
    Hash       string    `json:"hash"`
}
```

**核心逻辑（要理解，不要背）**：

```go
func (r Record) computeHash() string {
    c := r          // 值拷贝
    c.Hash = ""     // ← 关键：不能把 hash 算进自己
    b, err := json.Marshal(c)
    if err != nil { panic("audit: record not marshalable: " + err.Error()) }
    h := sha256.New()
    h.Write(b)
    h.Write([]byte(r.PrevHash))
    return hex.EncodeToString(h.Sum(nil))
}
```

**三个必须理解的点**：

1. **为什么用 struct 而不是 `map[string]any`**
   `encoding/json` 对 **map 的键做排序**，对 struct 按**字段声明顺序**输出。
   如果用 map，字段顺序会随 Go 版本/实现变化 → 历史记录的哈希全部失效 → 审计链自己断掉。
   **用 struct = 用字段声明顺序当作稳定序列化契约。** 这也意味着：**以后往 Record 加字段要加在末尾**（或在链上留版本号），否则旧记录重算哈希会变。

2. **为什么 `PrevHash` 写在 hash 输入里而不是只存字段**
   链式结构的本质是"每条记录承诺了它前面的全部历史"。只在一行里存 `prev_hash` 而不参与计算，等于没有链。

3. **为什么 `File.Sync()` 不能省**
   `Write` 只写到 OS 页缓存。断电时缓存丢失 → 你"记录过"的操作在磁盘上不存在 → 审计失去意义。
   ```go
   if _, err := f.Write(append(line, '\n')); err != nil { return err }
   if err := f.Sync(); err != nil { return err }        // ← fail-closed 的物理基础
   ```
   代价：每次操作一次 fsync（毫秒级）。对运维操作频率来说完全可接受。

**打开文件的方式**：

```go
f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
```
- `O_APPEND` 保证并发写时不会互相覆盖（内核保证原子追加）。
- 权限 `0600`，只有本进程用户可读。

**`Verify` 的写法**：

```go
func Verify(path string) (Result, error) {
    // 逐行读 → Unmarshal → 重算 computeHash → 比对 Hash
    //             → 比对 PrevHash 是否等于上一行的 Hash
    // 返回：总行数、第一个断链的 Seq 与原因
}
```

**坑**：不要用 `bufio.Scanner` 读审计文件 —— 默认 `MaxScanTokenSize` 是 64KB，一行超长会静默停止。用 `bufio.Reader.ReadBytes('\n')`。

**脱敏**：`redact.go` 用正则替换密码/token/私钥块，返回替换后的字节与命中次数。**在写盘和返回给调用方两处都要过一遍**。

**验收**：

```bash
enna audit show --tail 5     # 看到 5 条记录，prev_hash 首尾相接
enna audit verify            # OK
# 手工把某一行里的 exit_code 改掉
enna audit verify            # 失败，报出断链的 seq
```

> 这一步做完，你已经实现了一个**防篡改 append-only 日志**。这个模式在很多系统里都能复用（配置变更记录、采购流水、状态机事件）。

---

### Step 3 · 设备模型

**文件**：`internal/device/device.go`

```go
type Status int
const (
    StatusOK Status = iota
    StatusUnreachable
    StatusFrozen
    StatusUntrusted
)
func (s Status) String() string

type Device struct {
    ID            string
    Addr          string
    User          string
    KeyPath       string
    JumpHost      string
    Groups        []string
    ManagedUnits  []string      // service.* 技能的白名单
    AllowedPaths  []string      // file/log 技能的路径前缀白名单
    Fingerprint   string
    Status        Status
}

func (d Device) HasGroup(g string) bool
func (d Device) UnitAllowed(unit string) bool
func (d Device) PathAllowed(p string) bool
```

**关键设计**：`ManagedUnits` 和 `AllowedPaths` 是**第二层防御**（第一层是参数正则）。即使模型幻觉出一个格式合法的服务名，不在清单里就拒绝。这是 [`05` §3.3](05-device-executor.md#33-服务的设备级白名单) 的落地。

**Go 知识点**：为什么 `String()` 方法有意义 —— 实现 `fmt.Stringer` 后，`fmt.Printf("%s", status)` 自动可用。这是 Go 用**小接口**做多态的典型方式，标准库到处在用。

**`PathAllowed` 的正确实现**：

```go
func (d Device) PathAllowed(p string) bool {
    clean := filepath.Clean(p)                 // 1. 规范化（干掉 .. 与重复斜杠）
    if !filepath.IsAbs(clean) { return false } // 2. 必须绝对路径
    for _, prefix := range d.AllowedPaths {
        rel, err := filepath.Rel(filepath.Clean(prefix), clean)
        if err != nil { continue }
        // 3. rel 不以 ".." 开头才算在前缀内
        if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
            return true
        }
    }
    return false
}
```

**坑**：`strings.HasPrefix(clean, prefix)` 是**错的** —— `/var/logs-evil` 会匹配前缀 `/var/log`。必须用 `filepath.Rel` 判断路径关系。这个坑在真实系统里出过事。

---

### Step 4 · SSH 传输层

**文件**：`internal/sshx/target.go`、`command.go`、`client.go`

#### 4.1 命令构造（`command.go`）—— 整个 M0 最需要小心的代码

**必须理解的协议事实**：SSH 的 `exec` 请求负载是**一个字符串**，远端 sshd 把它交给登录 shell 执行（等价 `sh -c "<string>"`）。协议层没有 argv 数组。**所以远端一定会过一次 shell，我们绕不开。**

因此防御必须在我们这侧：

```go
// BuildCommand 把已验证的 argv 拼成远端可安全执行的命令串。
// 规则：拒绝控制字符；每个参数一律 POSIX 单引号转义。
func BuildCommand(argv []string, sudo bool) (string, error) {
    if len(argv) == 0 { return "", ErrEmptyArgv }
    if !filepath.IsAbs(argv[0]) { return "", ErrNotAbsolute }  // 二进制必须绝对路径
    quoted := make([]string, 0, len(argv)+2)
    if sudo {
        quoted = append(quoted, "/usr/bin/sudo", "-n")   // -n = 非交互，绝不提示密码
    }
    for _, a := range argv {
        q, err := shellQuote(a)
        if err != nil { return "", err }
        quoted = append(quoted, q)
    }
    return strings.Join(quoted, " "), nil
}

func shellQuote(s string) (string, error) {
    if strings.ContainsAny(s, "\x00\n\r") {
        return "", fmt.Errorf("%w: %q", ErrBadArg, s)   // 控制字符直接拒绝
    }
    return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'", nil
}
```

**为什么"一律加引号"而不是"只在需要时加"**：判断"是否需要引号"的逻辑本身容易出错（shell 的元字符集合很大且有心智陷阱）。**统一转义 = 没有分支 = 没有漏判**。这也让代码更容易审查。

**Go 知识点**：
- `fmt.Errorf("%w: %q", ErrBadArg, s)` —— `%w` 包装错误，`errors.Is/As` 才能穿透。这是 Go 1.13+ 的错误处理核心，务必掌握。
- `%q` 打印带引号的字符串，调试时能看出不可见字符。
- 用 `errors.New` 定义哨兵错误（`ErrBadArg`），供上层 `errors.Is` 判断。

**测试**：`command_test.go` 里表驱动跑这些输入，**全部必须被拒绝或安全转义**：

```go
inputs := []string{
    "nginx", "nginx; rm -rf /", "nginx && id", "`id`", "$(id)",
    "nginx\nid", "nginx\x00", "a'b", "\"a\"", "*", "../../etc/passwd",
    "-flag", "--", " ", "",
}
```

> 这份用例就是 [`05` §11](05-device-executor.md#11-测试要求) 提到的注入回归。**先写测试再写实现**（TDD），因为你的直觉一定会漏掉某个字符。

#### 4.2 主机密钥校验（安全红线）

```go
func hostKeyCallback(knownHostsPath string) (ssh.HostKeyCallback, error) {
    return knownhosts.New(knownHostsPath)
}
```

**禁止使用 `ssh.InsecureIgnoreHostKey()`**。建议加一个自定义 lint 或用 `grep` 卡在 CI 里 —— 这行代码一旦混进来，SSH 就完全失去了身份保证，中间人可以静默接管。

`knownhosts.New` 返回的 callback 在密钥不匹配时会返回错误。**不要 wrap 掉这个错误去"重试"** —— 密钥变了就该硬失败并告警。

#### 4.3 执行与超时（`client.go`）

```go
func (c *Client) Run(ctx context.Context, cmd string) (Result, error) {
    sess, err := c.conn.NewSession()
    if err != nil { return Result{}, fmt.Errorf("new session: %w", err) }
    defer sess.Close()

    var stdout, stderr bytes.Buffer
    sess.Stdout, sess.Stderr = &stdout, &stderr

    done := make(chan error, 1)          // ← 必须带缓冲，见下方坑
    go func() { done <- sess.Run(cmd) }()

    select {
    case <-ctx.Done():
        _ = sess.Signal(ssh.SIGKILL)     // 尽力终止远端进程
        _ = sess.Close()
        <-done                            // ← 等 goroutine 退出，防泄漏
        return Result{}, ctx.Err()
    case err := <-done:
        return toResult(stdout.Bytes(), stderr.Bytes(), err)
    }
}
```

**这段代码有三个必须理解的 Go 点**：

1. **`golang.org/x/crypto/ssh` 不接 `context`**
   它的 `Run` 是阻塞的。所以超时只能自己实现：起一个 goroutine 跑，主 goroutine `select` 等 context。
   这是 Go 里包装"不支持 context 的阻塞 API"的标准手法。

2. **`done` 必须 `make(chan error, 1)`**
   如果不带缓冲：超时分支返回后没人再读 `done`，那个 goroutine 写完 channel 就**永久阻塞** → goroutine 泄漏。每次超时泄漏一个，长期运行必然出问题。
   **"给无人接收的 channel 写值"是 Go 最经典的泄漏来源。**

3. **`<-done` 那行不能省**
   超时后直接 return 会让 goroutine 在后台继续跑（还持有 session 引用）。等它退出才是干净的。

**退出码不是 error（重要设计）**：

```go
func toResult(stdout, stderr []byte, err error) (Result, error) {
    if err == nil {
        return Result{ExitCode: 0, Stdout: stdout, Stderr: stderr}, nil
    }
    var ee *ssh.ExitError
    if errors.As(err, &ee) {
        // 命令执行了，但返回非 0 —— 这是"业务结果"，不是传输故障
        return Result{ExitCode: ee.ExitStatus(), Stdout: stdout, Stderr: stderr}, nil
    }
    // 真正的故障：连接断、认证失败、超时
    return Result{Stdout: stdout, Stderr: stderr}, fmt.Errorf("ssh run: %w", err)
}
```

**为什么这个区分至关重要**：
- `journalctl ... | grep foo` 没匹配时返回 1 —— 这是**正常结果**，不是错误。
- 如果把非 0 退出码一律当 error，模型（M2）会把"没有匹配"误读成"命令失败"，然后开始瞎修。

所以技能要声明"哪些退出码算成功"：

```go
type Command struct {
    Argv        []string
    Sudo        bool
    Timeout     time.Duration
    OkExitCodes []int        // 例如 log.grep: []int{0, 1}
}
```

`errors.As` 的用法也要掌握：它是**穿透包装链**找特定错误类型，比类型断言（`err.(*ssh.ExitError)`）强，因为它能穿透 `fmt.Errorf("%w")` 的包装。

**验收**：
```bash
# 能连上并返回真实数据
enna exec lab01 host.disk
# 密钥不匹配时硬失败（手工改 known_hosts 后测试）
# 超时（把远端命令换成 sleep 60，设 5s 超时）→ 5s 内返回，无 goroutine 泄漏
```

**Go 知识点补充**：用 `go test -race` 跑测试。上面这段并发代码如果写错，race detector 能帮你发现。

---

### Step 5 · 技能注册表 + 第一个技能

**文件**：`internal/skill/skill.go`、`internal/skill/host/disk.go`

#### 5.1 技能用什么表示：**struct-of-funcs**（推荐）

```go
type Skill struct {
    Name        string
    Summary     string
    Risk        policy.Risk
    TargetKind  TargetKind           // HOST | NONE
    Timeout     time.Duration
    OkExitCodes []int

    Validate func(args Args) error
    Build    func(dev device.Device, args Args) (sshx.Command, error)
    Parse    func(res sshx.Result) (any, error)
}

type Args map[string]any
```

**为什么不用 interface**（这是个好问题，值得想清楚）：

| 方式 | 适用场景 |
|---|---|
| `interface { Name() string; Build() ... }` | 每个技能需要**独立状态**或**多个实例**（如不同实现） |
| `struct of funcs`（推荐 M0） | 技能是**无状态的同质集合**，只是行为的组合 |

四十几项技能形状完全一样、都无状态，用 struct 更简单、更易读、更容易批量构造。标准库也是这个思路：`http.HandlerFunc` 就是把函数适配成接口。

> **当你需要"某个技能有配置参数"或"同一技能有多个实现"时，再换成 interface。** 现在就换是过度设计。

#### 5.2 注册表（**不要用 `init()`**）

```go
type Registry struct { m map[string]Skill }

func NewRegistry() *Registry
func (r *Registry) Register(s Skill) error     // 重名返回错误
func (r *Registry) Get(name string) (Skill, bool)
func (r *Registry) List() []Skill              // 排序后返回，便于稳定输出

// 显式装配，在 main 里调用
func RegisterAll(r *Registry) error {
    if err := host.Register(r); err != nil { return err }
    if err := service.Register(r); err != nil { return err }
    return nil
}
```

**为什么不用 `init()` 自动注册**：

`init()` 看起来优雅（加个文件就自动生效），但代价是**隐式副作用**：
- 无法从代码看出哪些技能被注册了（要全局搜索）。
- 测试时无法只注册一部分技能（`init()` 一定会跑）。
- 注册顺序不可控（Go 按文件名的字典序初始化）。
- 技能重名冲突只在运行时暴露。

**显式 `RegisterAll` 让注册表成为一个可读的、可测试的清单。** 「显式优于隐式」在 Go 社区是强共识 —— `init()` 用得越少越好。

#### 5.3 第一个技能：`host.disk`

```go
func Register(r *skill.Registry) error {
    return r.Register(skill.Skill{
        Name:        "host.disk",
        Summary:     "查看文件系统使用率（df）",
        Risk:        policy.L0Read,
        TargetKind:  skill.TargetHost,
        Timeout:     10 * time.Second,
        OkExitCodes: []int{0},
        Validate: func(args skill.Args) error {
            return skill.NoArgs(args)          // L0 无参数：拒绝任何多余参数
        },
        Build: func(dev device.Device, args skill.Args) (sshx.Command, error) {
            return sshx.Command{
                Argv: []string{"/bin/df", "-B1", "-x", "tmpfs", "-x", "devtmpfs", "--output=source,target,size,used,avail,pcent"},
            }, nil
        },
        Parse: parseDf,
    })
}
```

**`Validate` 的设计**：`NoArgs(args)` 拒绝了**任何**未声明参数 —— 这叫 **strict 校验**。宽松校验（忽略未知参数）会让"模型传了 `path` 参数但其实技能不认"这种情况静默通过，产生"用户以为在指定路径、实际在全盘"的误解。

**`Parse` 把文本变成结构体**：

```go
type DiskUsage struct {
    Filesystems []Filesystem `json:"filesystems"`
}
type Filesystem struct {
    Source  string `json:"source"`
    Target  string `json:"target"`
    SizeB   int64  `json:"size_bytes"`
    UsedB   int64  `json:"used_bytes"`
    AvailB  int64  `json:"avail_bytes"`
    UsedPct int    `json:"used_pct"`
}
```

**为什么一定要 Parse**：不要把原始 `df` 文本直接返回。结构化输出有三个好处 ——
1. 模型（M2）不用解析文本格式，错误率大幅下降；
2. 可以程序化判断"是否超过阈值"（M5 告警直接用）；
3. 输出给 QQ 时能自定义渲染（横向表格 / 纵向）。

**Go 知识点**：手动解析时用 `strings.Fields` 而不是 `strings.Split(s, " ")` —— 后者在多个连续空格时会产生空字符串。这类"看起来能用其实会崩"的细节，是文本解析的常见坑。

**验收**：`enna exec lab01 host.disk --json` 输出结构化 JSON。

---

### Step 6 · 策略闸门

**文件**：`internal/policy/risk.go`、`decide.go`

```go
type Risk int
const (
    L0Read Risk = iota
    L1SafeWrite
    L2Mutate
    L3Dangerous
    L4Forbidden
)
func (r Risk) String() string     // "L0".."L4"
func ParseRisk(s string) (Risk, error)

type Channel int
const (ChannelCLI Channel = iota; ChannelPrivate; ChannelGroup)

type DecisionKind int
const (Allow DecisionKind = iota; NeedApproval; Deny)

type DecideRequest struct {
    Principal Principal
    Channel   Channel
    Device    device.Device
    Skill     skill.Skill
    Args      skill.Args
    AuditOK   bool
}

type Decision struct {
    Kind     DecisionKind
    Risk     Risk          // 生效后的风险（收紧过的）
    DenyCode string        // 机器可读，用于审计聚合
    Reason   string        // 人类可读
}

func Decide(req DecideRequest, pol Policy) Decision
```

**判定算法**严格按 [`05` §5.1](05-device-executor.md#51-判定算法确定性短路deny-优先) 实现，要点：

1. **顺序执行、任何 DENY 立即短路** —— 不要"收集所有问题再一起报"，因为部分通过会带来实现分歧。
2. **`effective = min(技能声明, override, ACL 上限)`** —— 策略只能**收紧**。`min` 这个字是整个安全模型的关键：配置文件永远无法提升权限。
3. **返回结构化 `DenyCode` 而不是 error** —— 判定结果是**正常的业务输出**，不是异常。`DenyCode` 会被审计聚合（"本周 NO_DEVICE_ACL 出现 30 次"是有价值的安全信号），`Reason` 给人看。

**Go 知识点**：这里体现"**error 用于异常，值用于结果**"的 Go 设计哲学。很多新手把所有失败都塞进 `error`，导致调用方无法区分"权限不足"（要告诉用户原因）和"数据库连不上"（要重试 + 告警）。

**表驱动测试**（`decide_test.go`）：

```go
func TestDecide(t *testing.T) {
    tests := []struct{
        name string
        req  DecideRequest
        want DecisionKind
        wantCode string
    }{
        {"L0 私聊放行", ..., Allow, ""},
        {"群聊 L2 拒绝", ..., Deny, "CHANNEL_RESTRICTED"},
        {"未知技能", ..., Deny, "UNKNOWN_SKILL"},
        {"设备不在 ACL", ..., Deny, "NO_DEVICE_ACL"},
        {"审计不可用且 L1", ..., Deny, "AUDIT_UNAVAILABLE"},
        {"审计不可用但 L0", ..., Allow, ""},
        {"L2 需确认", ..., NeedApproval, ""},
        {"L3 需双签", ..., NeedApproval, ""},
        // ... 每个分支都要有一条
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) { /* ... */ })
    }
}
```

**`t.Run` 的价值**：失败时能精确看到是哪条用例挂了，而不是"TestDecide failed"。表驱动 + `t.Run` 是 Go 测试的标准姿势。

**验收**：`go test ./internal/policy/ -run TestDecide -v` 全绿，且**每个 DENY 分支都有对应用例**。

---

### Step 7 · 审批票据

**文件**：`internal/policy/ticket.go`

```go
type Ticket struct {
    ID        string
    PlanHash  string
    Principal string
    Devices   []string
    Risk      Risk
    Nonce     string
    IssuedAt  time.Time
    ExpiresAt time.Time
    State     State        // Issued | Approved | Consumed | Expired | Revoked
    FailCount int
}

func (s *Store) Issue(plan Plan, now time.Time) (Ticket, string, error)  // 返回票据与确认码
func (s *Store) Approve(id, code, principal string, now time.Time) (Ticket, error)
func (s *Store) Consume(id string, now time.Time) error
```

#### 7.1 确认码

```go
func (s *Store) codeFor(t Ticket) string {
    mac := hmac.New(sha256.New, s.secret)
    // 把票据的关键信息全部纳入，任何一处变化都会改变确认码
    io.WriteString(mac, t.PlanHash)
    io.WriteString(mac, t.Principal)
    io.WriteString(mac, strings.Join(t.Devices, ","))
    io.WriteString(mac, strconv.FormatInt(t.ExpiresAt.Unix(), 10))
    io.WriteString(mac, t.Nonce)
    sum := mac.Sum(nil)
    return base32.StdEncoding.EncodeToString(sum)[:6]
}
```

**校验必须用常量时间比较**：

```go
if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
    // 失败计数 +1，达到 5 次吊销票据并冻结主体
}
```

**为什么必须常量时间**：普通的 `==` 在第一个不同字节处就返回，攻击者可以通过**测量响应时间**逐字节猜出正确值。6 位码的搜索空间不大，时序侧信道会显著降低破解成本。
**这是初学者最容易忽略的密码学实践** —— 知道"要用 HMAC"但不知道"比较也要讲究"。

#### 7.2 时间必须注入（可测试性的关键）

```go
func (s *Store) Approve(id, code, principal string, now time.Time) (Ticket, error)
//                                                      ↑ 不在这里调 time.Now()
```

**如果函数内部调 `time.Now()`，你就无法测试"票据过期"这条路径** —— 除非 `time.Sleep`（慢）或改系统时间（危险）。
把 `now` 作为参数传入，测试里就能自由构造"过期 1 秒"、"恰好到期"这些边界：

```go
{"恰好到期", t.IssuedAt.Add(5*time.Minute), ErrTicketExpired},
{"差 1 秒到期", t.IssuedAt.Add(5*time.Minute - time.Second), nil},
```

**这个手法叫"控制时间来源"，是让涉及时间的逻辑变得可测的标准做法。** 同样适用于审计时间戳、缓存 TTL、重试退避。

#### 7.3 一次性语义

```go
func (s *Store) Consume(id string, now time.Time) error {
    t, ok := s.m[id]
    if !ok { return ErrNotFound }
    if t.State != Approved { return ErrBadState }
    // 已过期？
    if now.After(t.ExpiresAt) { t.State = Expired; return ErrTicketExpired }
    t.State = Consumed          // ← 此后任何重放都会因 State 检查失败
    return nil
}
```

**验收**：
```bash
enna exec lab01 service.restart --unit nginx   # 拿到 ticket-id 和确认码
enna approve <ticket-id> 000000                # 错误码 → 计数+1
# 连错 5 次 → 票据吊销 + 主体冻结
enna approve <ticket-id> <正确码>              # 执行成功
enna approve <ticket-id> <正确码>              # 失败：已消费
```

---

### Step 8 · CLI 装配 🟡 部分完成（`config` / `audit` 子命令已实现，其余待 Step 3–7）

**文件**：`cmd/enna/main.go`、`internal/cli/*.go`

**M0 用标准库 `flag`，不用 cobra**：

```go
func main() {
    if len(os.Args) < 2 {
        usage()
        os.Exit(2)
    }
    var err error
    switch os.Args[1] {
    case "exec":    err = cli.Exec(os.Args[2:])
    case "device":  err = cli.Device(os.Args[2:])
    case "audit":   err = cli.Audit(os.Args[2:])
    case "approve": err = cli.Approve(os.Args[2:])
    default:        usage(); os.Exit(2)
    }
    if err != nil {
        fmt.Fprintln(os.Stderr, "错误:", err)
        os.Exit(1)                         // ← 非 0 退出码，让脚本能判断
    }
}
```

子命令内部：

```go
func Exec(argv []string) error {
    fs := flag.NewFlagSet("exec", flag.ContinueOnError)
    jsonOut := fs.Bool("json", false, "以 JSON 输出")
    if err := fs.Parse(argv); err != nil { return err }
    rest := fs.Args()
    if len(rest) < 2 { return errors.New("用法: enna exec <device> <skill> [--k=v ...]") }
    // ...
}
```

**为什么要先学标准库 `flag`**：
- 它只有 200 行左右，你能完整读懂，理解"命令行解析"的本质。
- `flag.NewFlagSet` 体现了"子命令就是独立解析器"这个通用模式 —— cobra 也是这个原理，只是包装更好。
- **知道底座再上框架**，否则遇到框架的诡异行为就只能猜。

**装配（构造注入，不用 DI 框架）**：

```go
func Exec(argv []string) error {
    cfg, err := config.Load(cfgPath)             // 1. 配置
    if err != nil { return err }

    aud, err := audit.Open(cfg.Audit.Path)       // 2. 审计
    if err != nil { return err }
    defer aud.Close()

    reg := skill.NewRegistry()                   // 3. 技能
    if err := skill.RegisterAll(reg); err != nil { return err }

    pool := sshx.NewPool(cfg.SSH)                // 4. SSH 连接池
    defer pool.Close()

    // 5. 编排：policy.Decide → aud.Append → 执行
    ...
}
```

**Go 知识点**：Go 不需要 DI 框架，**显式构造 + 传参**就是最好的依赖注入。这让依赖关系一眼可见，也是 Go 社区的主流做法。

---

### Step 9 · 测试与验收

```bash
go test ./... -race                # 全部测试 + 竞态检测
go vet ./...
golangci-lint run
```

三类测试都要有：

| 类型 | 位置 | 内容 |
|---|---|---|
| 注入回归 | `skill/inject_test.go` | 每个 `Build` 跑一遍危险输入集（`;`、`&&`、`` ` ``、`$()`、`\n`、`..`、通配符、Unicode 同形字） |
| 策略表驱动 | `policy/decide_test.go` | 覆盖每个 DENY 分支 + `min` 收紧语义 |
| 票据边界 | `policy/ticket_test.go` | 错误码 5 次、过期、消费后重放、篡改 plan_hash |

**M0 完整验收清单**：

```bash
go build ./... && go test ./... -race            # 全绿
enna device add lab01 --addr 127.0.0.1:22
enna device trust lab01 --fingerprint SHA256:...
enna exec lab01 host.disk --json                  # 真实数据
enna exec lab01 service.status --unit nginx       # 受管单元通过
enna exec lab01 service.status --unit evil        # TARGET_NOT_ALLOWED
enna exec lab01 service.restart --unit nginx      # NEED_APPROVAL + 确认码
enna approve <id> <code>                          # 执行成功
enna approve <id> <code>                          # 已消费，失败
enna audit show --tail 10 && enna audit verify    # OK
# 篡改一行 → enna audit verify 失败
```

**做完这 10 条命令能全部通过，M0 就完成了。** 此时你已经拥有：一个防篡改审计的、有确定性权限闸门的、远程执行工具 —— 而且它完全不依赖大模型。

---

## 4. Go 新手最容易踩的坑（针对本项目）

| # | 坑 | 后果 | 做法 |
|---|---|---|---|
| 1 | 忽略 `error` 返回值 | 运维系统里的**静默失败** | 第一天开 `errcheck` |
| 2 | `map` 当审计记录序列化对象 | 字段顺序不稳定 → **审计链自己断掉** | 用 struct，字段顺序即契约 |
| 3 | 省略 `File.Sync()` | 断电后"记录过的操作"不存在 | 审计写入必须 fsync |
| 4 | `strings.HasPrefix` 做路径前缀判断 | `/var/logs-evil` 绕过 `/var/log` 白名单 | 用 `filepath.Rel` |
| 5 | 无缓冲 channel + 提前 return | goroutine 泄漏，长期运行崩溃 | `make(chan error, 1)`；超时后 `<-done` |
| 6 | `strings.Split(s, " ")` 解析命令输出 | 连续空格产生空串 | `strings.Fields` |
| 7 | 函数内直接调 `time.Now()` | **无法测试过期逻辑** | 把 `now` 作为参数注入 |
| 8 | 用 `==` 比较确认码 | 时序侧信道 | `subtle.ConstantTimeCompare` |
| 9 | `defer` 写在循环里 | 资源累积到函数结束才释放 | 循环体内抽成函数，或显式关闭 |
| 10 | `panic` 处理可预期错误 | 一个坏配置打挂整个服务 | `panic` 只用于"绝不该发生"的情况 |
| 11 | 用 `init()` 自动注册 | 隐式副作用、无法单独测试 | 显式 `RegisterAll` |
| 12 | slice 共享底层数组 | 修改一个影响另一个 | 需要独立副本时显式 `slices.Clone` |
| 13 | `ssh.InsecureIgnoreHostKey()` | 完全失去 SSH 身份保证 | 红线，CI 里 grep 卡住 |
| 14 | 把非 0 退出码当 Go error | 模型误判"没匹配"为"执行失败" | `errors.As(*ssh.ExitError)` 单独处理 |

---

## 5. M1 怎么接（不改内核）

M0 完成后，把 QQ 接上是**加法**，不是改造：

```
新增：
  internal/onebot/      OneBot 11 事件与消息段类型、CQ 码解析
  internal/qqgw/        反向 WS 服务端、鉴权、会话、渲染、限速
  internal/qqgw/into    QQ 消息 → 复用 M0 的 policy.Decide + 技能执行
```

**唯一需要动的既有代码**：把 `cli.Exec` 里那段"装配 + 编排"抽成一个 `internal/engine` 包，让 CLI 和 qqgw 共用同一条执行路径。

**这是 M0 最重要的架构收益**：因为 M0 就坚持了 `policy` / `skill` 不依赖 `cli`，M1 只是**多了一个调用方**。

> ⚠️ M1 有个你们的拓扑特有的风险（多个 bot 同群会互相触发），已在 [`03-qq-channel.md` §4.4](03-qq-channel.md#44-多-bot-同群必须防互相触发) 补上，接 QQ 前务必先读那一节。

---

## 6. 学习资源（按本项目实际需要排序）

| 主题 | 材料 |
|---|---|
| Go 基础语法与惯例 | [A Tour of Go](https://go.dev/tour/) → [Effective Go](https://go.dev/doc/effective_go) |
| 错误处理 | [Working with Errors in Go 1.13](https://go.dev/blog/error-handling-and-go)（`%w` / `errors.Is` / `errors.As`） |
| 并发 | [Go Concurrency Patterns: Pipelines and cancellation](https://go.dev/blog/pipelines)（对应 Step 4 的超时模式） |
| 测试 | [Table-Driven Tests](https://go.dev/wiki/TableDrivenTests) |
| 密码学 | `crypto/subtle` 文档里关于常量时间比较的说明 |
| SSH | `golang.org/x/crypto/ssh` 的 `ClientConfig` 与 `knownhosts` 包文档 |
| 审计链思想 | 搜索 "hash chain / tamper-evident log"（Merkle 树是它的进阶形态） |
| OneBot 协议 | [OneBot 11 规范](https://github.com/botuniverse/onebot-11)（M1 再读） |
