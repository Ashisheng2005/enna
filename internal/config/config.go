// Package config 负责加载与校验 enna 的运行时配置。
//
// 设计依据（docs/00-requirements.md）：
//   - NFR-MNT-03：配置错误必须启动即失败，绝不静默使用默认值
//   - FR-SK-05  ：设备级收口（受管服务清单、允许路径）在这里声明
//   - FR-AU-02  ：审计路径属安全配置，必填（审计不可用 = 不可执行变更）
//   - FR-ID-02  ：主体（角色 + 可交互 bot + 设备 ACL）以静态配置维护
//
// # 关于"默认值"的边界（刻意的取舍）
//
//   - 安全相关字段一律必填，缺一个就拒绝启动：
//     data_dir、audit.path、ssh.known_hosts、principals、devices
//   - 非安全的时间参数允许省略，由 applyDefaults 显式填充：
//     ssh.dial_timeout、ssh.command_timeout
//
// 之所以把 known_hosts 定为必填：它缺失时 SSH 无法校验主机密钥，
// 等于放弃防中间人（FR-SK-06）。这种情况必须响亮地失败。
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// 角色（FR-ID-03）
const (
	RoleOwner    = "OWNER"
	RoleOperator = "OPERATOR"
	RoleViewer   = "VIEWER"
)

// 风险等级（FR-PL-01）：由技能代码声明，配置只能收紧，不能放宽
const (
	RiskL0 = "L0" // 只读观测
	RiskL1 = "L1" // 低危写
	RiskL2 = "L2" // 可逆变更（单次确认）
	RiskL3 = "L3" // 高危变更（双签）
	RiskL4 = "L4" // 禁止
)

// 非安全时间参数的默认值
const (
	defaultDialTimeout    = 5 * time.Second
	defaultCommandTimeout = 15 * time.Second
)

var (
	validRoles = map[string]bool{RoleOwner: true, RoleOperator: true, RoleViewer: true}
	validRisks = map[string]bool{RiskL0: true, RiskL1: true, RiskL2: true, RiskL3: true, RiskL4: true}
)

// Config 是 enna.yaml 的根结构。
//
// M0 刻意使用**单文件配置**：把策略与设备清单内联，减少活动部件。
// 当设备规模增长到需要分组管理时（M3），再拆分为 policy.yaml / devices.yaml。
type Config struct {
	DataDir string       `yaml:"data_dir"`
	Audit   AuditSpec    `yaml:"audit"`
	SSH     SSHSpec      `yaml:"ssh"`
	Policy  PolicySpec   `yaml:"policy"`
	Devices []DeviceSpec `yaml:"devices"`
}

// AuditSpec 描述审计落盘位置。
type AuditSpec struct {
	// Path 是审计哈希链文件路径。必填、必须为绝对路径（FR-AU-02）。
	Path string `yaml:"path"`
}

// SSHSpec 是 SSH 传输层的公共配置。
type SSHSpec struct {
	// KnownHosts 是主机密钥校验文件。必填（FR-SK-06：禁止 TOFU）。
	KnownHosts string `yaml:"known_hosts"`
	// DefaultUser 是设备的默认登录账号；设备可单独覆盖。
	DefaultUser string `yaml:"default_user"`
	// DialTimeout / CommandTimeout 为零时由 applyDefaults 填充。
	DialTimeout    time.Duration `yaml:"dial_timeout"`
	CommandTimeout time.Duration `yaml:"command_timeout"`
}

// PolicySpec 是策略闸门的静态输入（FR-PL-*）。
type PolicySpec struct {
	// ForbiddenSkills 是配置层追加的禁止清单。
	// 注意：L4 的核心禁止项硬编码在代码里（FR-PL-05），这里只是额外补充。
	ForbiddenSkills []string `yaml:"forbidden_skills"`

	// RiskOverrides 用于**下调**技能风险等级。
	// 若某个 override 高于技能声明值，policy.Decide 会拒绝（FR-PL-02）。
	// 配置层只能校验取值合法，无法校验方向 —— 方向校验依赖技能注册表。
	RiskOverrides map[string]string `yaml:"risk_overrides"`

	// MaxRiskByChannel 是通道维度的风险上限。
	// private 可配，group 由代码硬限 L0（FR-ID-04）。
	MaxRiskByChannel map[string]string `yaml:"max_risk_by_channel"`

	Principals []PrincipalSpec `yaml:"principals"`
}

// PrincipalSpec 是一个主体（人）的权限声明。
type PrincipalSpec struct {
	QQID    uint64 `yaml:"qq_id"`
	Display string `yaml:"display"`
	Role    string `yaml:"role"`
	// BotScope 是该主体可通过哪些机器人账号交互（拓扑 b 下即"可管理哪些设备"）。
	// 为空表示不限；推荐显式列出（FR-ID-01：权限是 (bot_id, qq_id) 的函数）。
	BotScope []uint64  `yaml:"bot_scope"`
	ACL      []ACLSpec `yaml:"acl"`
}

// ACLSpec 是"设备组 → 该主体在该组内可执行的风险上限"。
type ACLSpec struct {
	Group   string `yaml:"group"`
	MaxRisk string `yaml:"max_risk"`
}

// DeviceSpec 是一台受管设备的声明（FR-SK-01：静态清单，不支持临时连主机）。
type DeviceSpec struct {
	ID       string   `yaml:"id"`
	Addr     string   `yaml:"addr"` // host:port
	User     string   `yaml:"user"` // 为空则继承 ssh.default_user
	KeyPath  string   `yaml:"key_path"`
	JumpHost string   `yaml:"jump_host"` // 可选跳板机，格式同 Addr
	Groups   []string `yaml:"groups"`
	Tags     []string `yaml:"tags"`

	// ManagedUnits 是 service.* 技能可操作的单元白名单。
	// ⚠️ 为空表示**拒绝一切** service 操作（fail-closed），而不是"允许全部"。
	ManagedUnits []string `yaml:"managed_units"`

	// AllowedPaths 是 file/log 类技能可触及的路径前缀白名单。
	// ⚠️ 同样为空表示拒绝一切（fail-closed）。
	AllowedPaths []string `yaml:"allowed_paths"`
}

// Load 读取并校验配置文件。
//
// 用 Decoder + KnownFields(true)：字段名拼错会报错，而不是被静默忽略。
// 这一行能省掉大量"为什么我的配置没生效"的排查时间 ——
// 它是本项目里性价比最高的一行代码。
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: 打开 %s: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: 解析 %s: %w", path, err)
	}
	if err := c.applyDefaults(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config: 校验 %s: %w", path, err)
	}
	return &c, nil
}

// applyDefaults 只填充非安全参数，并且是**显式**的一步 —— 便于日志说明与测试断言。
//
// 为什么单独抽一个函数，而不是在 Load 里内联或写在结构体 tag 里：
// "哪些字段有默认值、默认值是什么"本身就是需要被审视的设计信息。
// 集中在一处，评审时一眼能看完；散落在 tag 里就只能靠搜索。
func (c *Config) applyDefaults() error {
	if c.SSH.DialTimeout == 0 {
		c.SSH.DialTimeout = defaultDialTimeout
	}
	if c.SSH.CommandTimeout == 0 {
		c.SSH.CommandTimeout = defaultCommandTimeout
	}
	if c.SSH.DefaultUser == "" {
		c.SSH.DefaultUser = "enna-ops"
	}
	for i := range c.Devices {
		if c.Devices[i].User == "" {
			c.Devices[i].User = c.SSH.DefaultUser
		}
	}
	return nil
}

// Validate 集中所有约束。
//
// 独立于 Load 的好处：不碰文件系统就能测试全部校验规则。
//
// 用 errors.Join 一次报出全部问题，而不是遇到第一个就返回 ——
// 否则用户会陷入"改一个、跑一次、又报下一个"的循环。
func (c *Config) Validate() error {
	var errs []error

	// ── 顶层路径 ──
	if err := requireAbsDir("data_dir", c.DataDir); err != nil {
		errs = append(errs, err)
	}
	if err := requireAbsFile("audit.path", c.Audit.Path); err != nil {
		errs = append(errs, err)
	}

	// ── SSH ──
	if err := requireAbsFile("ssh.known_hosts", c.SSH.KnownHosts); err != nil {
		errs = append(errs, err)
	}
	if c.SSH.DefaultUser == "" {
		errs = append(errs, errors.New("ssh.default_user 不能为空"))
	}
	if c.SSH.DialTimeout <= 0 {
		errs = append(errs, errors.New("ssh.dial_timeout 必须为正"))
	}
	if c.SSH.CommandTimeout <= 0 {
		errs = append(errs, errors.New("ssh.command_timeout 必须为正"))
	}

	// ── 策略 / 设备 ──
	errs = append(errs, c.validatePolicy()...)
	errs = append(errs, c.validateDevices()...)

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// validatePolicy 校验 policy 段。
//
// 最关键的一条：group 通道的风险上限由代码硬限为 L0，配置试图放宽即拒绝启动（FR-ID-04）。
// 这是"配置不能提升权限"原则在配置解析层的第一次落地 ——
// 第二次落地是 policy.Decide 里的 min()。
func (c *Config) validatePolicy() []error {
	var errs []error

	for skill, risk := range c.Policy.RiskOverrides {
		if !validRisks[risk] {
			errs = append(errs, fmt.Errorf("policy.risk_overrides[%s]: 非法等级 %q", skill, risk))
		}
	}
	for ch, risk := range c.Policy.MaxRiskByChannel {
		if !validRisks[risk] {
			errs = append(errs, fmt.Errorf("policy.max_risk_by_channel[%s]: 非法等级 %q", ch, risk))
		}
	}
	// 群聊上限不可上调：群成员不可信，且多 bot 同群时消息互相可见
	if risk, ok := c.Policy.MaxRiskByChannel["group"]; ok && risk != RiskL0 {
		errs = append(errs, fmt.Errorf(
			"policy.max_risk_by_channel[group] 必须为 L0（群聊硬限，不可上调），实际 %q", risk))
	}

	seen := map[uint64]bool{}
	for i, p := range c.Policy.Principals {
		where := fmt.Sprintf("policy.principals[%d]", i)

		if p.QQID == 0 {
			errs = append(errs, fmt.Errorf("%s: qq_id 不能为 0", where))
		} else if seen[p.QQID] {
			errs = append(errs, fmt.Errorf("%s: qq_id %d 重复", where, p.QQID))
		}
		seen[p.QQID] = true

		if strings.TrimSpace(p.Display) == "" {
			errs = append(errs, fmt.Errorf("%s: display 不能为空", where))
		}
		if !validRoles[p.Role] {
			errs = append(errs, fmt.Errorf("%s: 非法角色 %q（可选 OWNER/OPERATOR/VIEWER）", where, p.Role))
		}
		for j, a := range p.ACL {
			if strings.TrimSpace(a.Group) == "" {
				errs = append(errs, fmt.Errorf("%s.acl[%d]: group 不能为空", where, j))
			}
			if !validRisks[a.MaxRisk] {
				errs = append(errs, fmt.Errorf("%s.acl[%d]: 非法等级 %q", where, j, a.MaxRisk))
			}
		}
	}
	return errs
}

// validateDevices 校验 devices 段。
func (c *Config) validateDevices() []error {
	var errs []error
	seen := map[string]bool{}

	for i, d := range c.Devices {
		where := fmt.Sprintf("devices[%d]", i)

		if strings.TrimSpace(d.ID) == "" {
			errs = append(errs, fmt.Errorf("%s: id 不能为空", where))
		} else {
			where = fmt.Sprintf("devices[%d](%s)", i, d.ID)
			if seen[d.ID] {
				errs = append(errs, fmt.Errorf("%s: id 重复", where))
			}
			seen[d.ID] = true
		}

		if _, _, err := net.SplitHostPort(d.Addr); err != nil {
			errs = append(errs, fmt.Errorf("%s: addr 必须是 host:port 形式，实际 %q", where, d.Addr))
		}
		if d.JumpHost != "" {
			if _, _, err := net.SplitHostPort(d.JumpHost); err != nil {
				errs = append(errs, fmt.Errorf("%s: jump_host 必须是 host:port 形式，实际 %q", where, d.JumpHost))
			}
		}
		if d.KeyPath != "" && !filepath.IsAbs(d.KeyPath) {
			errs = append(errs, fmt.Errorf("%s: key_path 必须是绝对路径，实际 %q", where, d.KeyPath))
		}
		if len(d.Groups) == 0 {
			errs = append(errs, fmt.Errorf("%s: 必须至少属于一个 groups（ACL 按组判定）", where))
		}

		for j, p := range d.AllowedPaths {
			if !filepath.IsAbs(p) {
				errs = append(errs, fmt.Errorf("%s.allowed_paths[%d]: 必须是绝对路径，实际 %q", where, j, p))
				continue
			}
			clean := filepath.Clean(p)
			// 允许卷根（Linux 的 "/"、Windows 的 `C:\`）等于允许全盘，直接拒绝。
			// 用 VolumeName 拼接而不写死 "/"，否则在 Windows 上会漏判。
			root := filepath.VolumeName(clean) + string(filepath.Separator)
			if clean == root {
				errs = append(errs, fmt.Errorf("%s.allowed_paths[%d]: 不允许卷根目录 %q", where, j, p))
			}
		}
		for j, u := range d.ManagedUnits {
			if strings.TrimSpace(u) == "" {
				errs = append(errs, fmt.Errorf("%s.managed_units[%d]: 不能为空字符串", where, j))
			}
		}
	}
	return errs
}

// ── 校验辅助 ──

func requireAbsDir(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s 不能为空", field)
	}
	if !filepath.IsAbs(v) {
		return fmt.Errorf("%s 必须是绝对路径，实际 %q", field, v)
	}
	return nil
}

func requireAbsFile(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s 不能为空", field)
	}
	if !filepath.IsAbs(v) {
		return fmt.Errorf("%s 必须是绝对路径，实际 %q", field, v)
	}
	return nil
}

// FindDevice 按 id 查找设备。
func (c *Config) FindDevice(id string) (DeviceSpec, bool) {
	for _, d := range c.Devices {
		if d.ID == id {
			return d, true
		}
	}
	return DeviceSpec{}, false
}

// FindPrincipal 按 QQ 号查找主体。
func (c *Config) FindPrincipal(qqID uint64) (PrincipalSpec, bool) {
	for _, p := range c.Policy.Principals {
		if p.QQID == qqID {
			return p, true
		}
	}
	return PrincipalSpec{}, false
}
