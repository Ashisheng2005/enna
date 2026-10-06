package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// baseTemplate 是一份**在 Windows 与 Linux 上都合法**的配置。
// 路径全部由 __DIR__ 派生，避免用 /var/lib 这类只在 Linux 上绝对的前缀。
const baseTemplate = `
data_dir: '__DIR__/data'
audit:
  path: '__DIR__/audit/audit.jsonl'
ssh:
  known_hosts: '__DIR__/known_hosts'
  default_user: enna-ops
policy:
  forbidden_skills: [shell.run]
  risk_overrides:
    service.reload: L1
  max_risk_by_channel:
    private: L2
    group: L0
  principals:
    - qq_id: 10001
      display: 老王
      role: OWNER
      bot_scope: [10001, 10002]
      acl:
        - group: lab
          max_risk: L3
devices:
  - id: lab01
    addr: '127.0.0.1:22'
    key_path: '__DIR__/id_ed25519'
    groups: [lab]
    tags: [test]
    managed_units: [nginx]
    allowed_paths: ['__DIR__/var/log', '__DIR__/tmp']
`

// renderTemplate 把模板里的 __DIR__ 换成当前测试的临时目录。
//
// 顺序很重要：**先让用例改模板，再做路径替换**。
// 若反过来（先替换再 mutate），用例就得知道临时目录的真实路径，
// 既啰嗦又易错 —— 而且临时目录每次调用都不同。
func renderTemplate(t *testing.T, tmpl string) string {
	t.Helper()
	// ToSlash：Windows 的反斜杠在 YAML 双引号串里是转义符，正斜杠两边都合法
	return strings.ReplaceAll(tmpl, "__DIR__", filepath.ToSlash(t.TempDir()))
}

// render 返回未做任何修改的合法配置内容。
func render(t *testing.T) string {
	t.Helper()
	return renderTemplate(t, baseTemplate)
}

// loadString 把内容写入临时文件并加载，返回错误。
func loadString(t *testing.T, content string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "enna.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试配置: %v", err)
	}
	return Load(p)
}

// miniYAML 生成一份最小合法配置，供"结构重复"类用例使用。
func miniYAML(t *testing.T, devices, principals string) string {
	t.Helper()
	dir := filepath.ToSlash(t.TempDir())
	body := `
data_dir: '` + dir + `/data'
audit:
  path: '` + dir + `/audit.jsonl'
ssh:
  known_hosts: '` + dir + `/known_hosts'
  default_user: enna-ops
policy:
  principals:
` + principals + `
devices:
` + devices
	return body
}

// ── 正向 ──

func TestLoadValidConfig(t *testing.T) {
	cfg, err := loadString(t, render(t))
	if err != nil {
		t.Fatalf("合法配置应加载成功，实际报错: %v", err)
	}
	if len(cfg.Devices) != 1 || cfg.Devices[0].ID != "lab01" {
		t.Fatalf("设备解析错误: %+v", cfg.Devices)
	}
	if len(cfg.Policy.Principals) != 1 || cfg.Policy.Principals[0].QQID != 10001 {
		t.Fatalf("主体解析错误: %+v", cfg.Policy.Principals)
	}
	if cfg.Policy.Principals[0].ACL[0].MaxRisk != RiskL3 {
		t.Fatalf("ACL 解析错误: %+v", cfg.Policy.Principals[0].ACL)
	}
}

func TestDefaultsAreApplied(t *testing.T) {
	cfg, err := loadString(t, render(t))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.SSH.DialTimeout != defaultDialTimeout {
		t.Errorf("DialTimeout = %v，期望默认 %v", cfg.SSH.DialTimeout, defaultDialTimeout)
	}
	if cfg.SSH.CommandTimeout != defaultCommandTimeout {
		t.Errorf("CommandTimeout = %v，期望默认 %v", cfg.SSH.CommandTimeout, defaultCommandTimeout)
	}
	// 设备未声明 user，应继承 ssh.default_user
	if cfg.Devices[0].User != "enna-ops" {
		t.Errorf("设备 user = %q，期望继承 enna-ops", cfg.Devices[0].User)
	}
}

func TestExplicitTimeoutsWin(t *testing.T) {
	s := strings.Replace(render(t), "known_hosts:", "dial_timeout: 9s\n  command_timeout: 42s\n  known_hosts:", 1)
	cfg, err := loadString(t, s)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.SSH.DialTimeout != 9*time.Second || cfg.SSH.CommandTimeout != 42*time.Second {
		t.Fatalf("显式超时被覆盖: %v / %v", cfg.SSH.DialTimeout, cfg.SSH.CommandTimeout)
	}
}

// ManagedUnits 为空是**合法**的：语义是"这台机器不允许任何 service 操作"，
// 属于 fail-closed 的刻意设计，不是配置错误。
func TestEmptyManagedUnitsIsAllowed(t *testing.T) {
	s := strings.Replace(render(t), "    managed_units: [nginx]\n", "", 1)
	cfg, err := loadString(t, s)
	if err != nil {
		t.Fatalf("managed_units 为空应当合法，实际报错: %v", err)
	}
	if len(cfg.Devices[0].ManagedUnits) != 0 {
		t.Fatalf("ManagedUnits 应为空，实际 %v", cfg.Devices[0].ManagedUnits)
	}
}

func TestFindHelpers(t *testing.T) {
	cfg, err := loadString(t, render(t))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if d, ok := cfg.FindDevice("lab01"); !ok || d.Addr != "127.0.0.1:22" {
		t.Fatalf("FindDevice(lab01) = %+v, %v", d, ok)
	}
	if _, ok := cfg.FindDevice("nope"); ok {
		t.Fatal("FindDevice 对不存在的 id 应返回 false")
	}
	if p, ok := cfg.FindPrincipal(10001); !ok || p.Role != RoleOwner {
		t.Fatalf("FindPrincipal(10001) = %+v, %v", p, ok)
	}
	if _, ok := cfg.FindPrincipal(999); ok {
		t.Fatal("FindPrincipal 对不存在的 qq_id 应返回 false")
	}
}

// ── 解析严格性 ──

func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := loadString(t, render(t)+"\nnonsense: 1\n")
	if err == nil {
		t.Fatal("未知字段应当报错（KnownFields(true) 没起作用）")
	}
	if !strings.Contains(err.Error(), "nonsense") {
		t.Fatalf("错误信息应指出字段名，实际: %v", err)
	}
}

// ── 校验规则（表驱动）──

func TestValidateErrors(t *testing.T) {
	root := filepath.ToSlash(filepath.VolumeName(t.TempDir()) + string(filepath.Separator))

	tests := []struct {
		name    string
		mutate  func(s string) string
		wantSub string
	}{
		{
			name:    "data_dir 为空",
			mutate:  func(s string) string { return strings.Replace(s, "data_dir: '", "data_dir: '' # '", 1) },
			wantSub: "data_dir",
		},
		{
			name: "data_dir 为相对路径",
			mutate: func(s string) string {
				return strings.Replace(s, "data_dir: '__DIR__/data'", "data_dir: 'relative/data'", 1)
			},
			wantSub: "必须是绝对路径",
		},
		{
			name: "audit.path 为空",
			mutate: func(s string) string {
				return strings.Replace(s, "path: '__DIR__/audit/audit.jsonl'", "path: ''", 1)
			},
			wantSub: "audit.path 不能为空",
		},
		{
			name: "ssh.known_hosts 为空",
			mutate: func(s string) string {
				return strings.Replace(s, "known_hosts: '__DIR__/known_hosts'", "known_hosts: ''", 1)
			},
			wantSub: "ssh.known_hosts 不能为空",
		},
		{
			name:    "角色非法",
			mutate:  func(s string) string { return strings.Replace(s, "role: OWNER", "role: SUPERUSER", 1) },
			wantSub: "非法角色",
		},
		{
			name:    "群聊上限被放宽",
			mutate:  func(s string) string { return strings.Replace(s, "group: L0", "group: L2", 1) },
			wantSub: "群聊硬限",
		},
		{
			name:    "风险覆盖等级非法",
			mutate:  func(s string) string { return strings.Replace(s, "service.reload: L1", "service.reload: L9", 1) },
			wantSub: "非法等级",
		},
		{
			name:    "ACL 等级非法",
			mutate:  func(s string) string { return strings.Replace(s, "max_risk: L3", "max_risk: L9", 1) },
			wantSub: "非法等级",
		},
		{
			name:    "设备地址缺端口",
			mutate:  func(s string) string { return strings.Replace(s, "'127.0.0.1:22'", "'127.0.0.1'", 1) },
			wantSub: "host:port",
		},
		{
			name:    "设备不属于任何组",
			mutate:  func(s string) string { return strings.Replace(s, "groups: [lab]", "groups: []", 1) },
			wantSub: "必须至少属于一个 groups",
		},
		{
			name: "key_path 为相对路径",
			mutate: func(s string) string {
				return strings.Replace(s, "key_path: '__DIR__/id_ed25519'", "key_path: 'id_ed25519'", 1)
			},
			wantSub: "必须是绝对路径",
		},
		{
			name: "allowed_paths 含卷根",
			mutate: func(s string) string {
				return strings.Replace(s, "'__DIR__/var/log'", "'"+root+"'", 1)
			},
			wantSub: "不允许卷根目录",
		},
		{
			name: "allowed_paths 相对路径",
			mutate: func(s string) string {
				return strings.Replace(s, "'__DIR__/var/log'", "'var/log'", 1)
			},
			wantSub: "必须是绝对路径",
		},
		{
			name: "主体 qq_id 为 0",
			mutate: func(s string) string {
				return strings.Replace(s, "qq_id: 10001", "qq_id: 0", 1)
			},
			wantSub: "qq_id 不能为 0",
		},
		{
			name: "display 为空",
			mutate: func(s string) string {
				return strings.Replace(s, "display: 老王", "display: ''", 1)
			},
			wantSub: "display 不能为空",
		},
		{
			name: "ACL group 为空",
			mutate: func(s string) string {
				return strings.Replace(s, "group: lab", "group: ''", 1)
			},
			wantSub: "group 不能为空",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := tt.mutate(baseTemplate)
			if mutated == baseTemplate {
				t.Fatal("用例没有真正改动配置（mutate 的匹配串没对上）")
			}
			_, err := loadString(t, renderTemplate(t, mutated))
			if err == nil {
				t.Fatal("应当校验失败，实际通过了")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("错误信息应包含 %q\n实际: %v", tt.wantSub, err)
			}
		})
	}
}

func TestValidateRejectsDuplicateDeviceID(t *testing.T) {
	devices := `  - id: lab01
    addr: '127.0.0.1:22'
    groups: [lab]
  - id: lab01
    addr: '127.0.0.1:23'
    groups: [lab]
`
	principals := `    - qq_id: 10001
      display: a
      role: OWNER
`
	_, err := loadString(t, miniYAML(t, devices, principals))
	if err == nil {
		t.Fatal("重复设备 id 应当报错")
	}
	if !strings.Contains(err.Error(), "id 重复") {
		t.Fatalf("错误信息应指出 id 重复，实际: %v", err)
	}
}

func TestValidateRejectsDuplicatePrincipal(t *testing.T) {
	devices := `  - id: lab01
    addr: '127.0.0.1:22'
    groups: [lab]
`
	principals := `    - qq_id: 10001
      display: a
      role: OWNER
    - qq_id: 10001
      display: b
      role: VIEWER
`
	_, err := loadString(t, miniYAML(t, devices, principals))
	if err == nil {
		t.Fatal("重复 qq_id 应当报错")
	}
	if !strings.Contains(err.Error(), "重复") {
		t.Fatalf("错误信息应指出重复，实际: %v", err)
	}
}

func TestValidateAcceptsPrivateRiskAboveL0(t *testing.T) {
	// group 硬限 L0，但 private 允许配到 L3 —— 不应误伤
	s := strings.Replace(render(t), "private: L2", "private: L3", 1)
	if _, err := loadString(t, s); err != nil {
		t.Fatalf("private 通道配 L3 应当合法，实际: %v", err)
	}
}

func TestMissingFileReturnsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml")); err == nil {
		t.Fatal("文件不存在时应当报错")
	}
}

// ── 示例配置 ──

// 示例配置使用 Linux 绝对路径（/var/lib/enna/...），因为部署目标是 Linux。
// 在 Windows 开发机上它不满足"必须绝对路径"的约束，所以跳过 —— 这不是缺陷，
// 而是刻意的说明：示例面向目标平台。
func TestExampleConfigIsValid(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("示例配置使用 Linux 绝对路径，仅在 Linux 上校验")
	}
	path := filepath.Join("..", "..", "configs", "enna.example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("示例配置不存在: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("示例配置应当合法，实际: %v", err)
	}
	if len(cfg.Devices) == 0 {
		t.Fatal("示例配置应当至少包含一台设备")
	}
}
