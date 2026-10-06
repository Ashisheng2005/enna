// Package cli 实现 enna 的命令行入口。
//
// 设计依据：
//   - FR-OP-01：CLI 提供设备管理、技能执行、策略查看、票据吊销、审计校验、紧急停止
//   - FR-CH-09：CLI 能力不低于 QQ 通道（QQ 被封时靠它接管）
//   - FR-OP-03：离线恢复（后续步骤实现）
//
// M0 阶段刻意只用标准库 flag，不引入 cobra。
// 理由：flag 只有几百行，能完整读懂，理解"命令行解析"的本质；
// 子命令的本质就是"各自独立的 FlagSet"。知道底座再上框架，遇到诡异行为才不是猜。
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Ashisheng2005/enna/internal/audit"
	"github.com/Ashisheng2005/enna/internal/config"
)

// Version 是版本号，构建时可用 -ldflags 覆盖。
var Version = "0.1.0-m0"

const usageText = `enna —— 设备管理员 Agent（M0）

用法:
  enna config check  --config <path>            校验配置文件
  enna audit verify  --config <path>            校验审计哈希链
  enna audit show    --config <path> --tail 20  查看最近若干条审计记录
  enna version                                  打印版本

说明:
  M0 只包含确定性内核：配置 / 审计 / 策略 / 技能 / SSH / CLI。
  不含 QQ 通道与 LLM 推理层 —— 它们是在这之上的加法。
`

// Run 是唯一入口。
//
// 返回退出码而不是直接 os.Exit：这样测试可以直接调用 Run 并断言退出码，
// 不必真的把测试进程杀掉。
func Run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) < 2 {
		fmt.Fprint(stderr, usageText)
		return 2
	}

	var err error
	switch argv[1] {
	case "config":
		err = runConfig(argv[2:], stdout)
	case "audit":
		err = runAudit(argv[2:], stdout)
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "enna %s\n", Version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		fmt.Fprintf(stderr, "未知子命令: %s\n\n", argv[1])
		fmt.Fprint(stderr, usageText)
		return 2
	}

	if err != nil {
		fmt.Fprintf(stderr, "错误: %v\n", err)
		return 1
	}
	return 0
}

// ── enna config ──

func runConfig(argv []string, stdout io.Writer) error {
	if len(argv) == 0 {
		return errors.New("用法: enna config check --config <path>")
	}
	switch argv[0] {
	case "check":
		fs := flag.NewFlagSet("config check", flag.ContinueOnError)
		path := fs.String("config", "configs/enna.yaml", "配置文件路径")
		if err := fs.Parse(argv[1:]); err != nil {
			return err
		}
		cfg, err := config.Load(*path)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "配置有效: %s\n", *path)
		fmt.Fprintf(stdout, "  数据目录: %s\n", cfg.DataDir)
		fmt.Fprintf(stdout, "  审计文件: %s\n", cfg.Audit.Path)
		fmt.Fprintf(stdout, "  设备:     %d 台\n", len(cfg.Devices))
		fmt.Fprintf(stdout, "  主体:     %d 个\n", len(cfg.Policy.Principals))
		for _, d := range cfg.Devices {
			fmt.Fprintf(stdout, "    - %-12s %s  受管单元 %d 个  允许路径 %d 个\n",
				d.ID, d.Addr, len(d.ManagedUnits), len(d.AllowedPaths))
		}
		return nil
	default:
		return fmt.Errorf("未知的 config 子命令: %s", argv[0])
	}
}

// ── enna audit ──

func runAudit(argv []string, stdout io.Writer) error {
	if len(argv) == 0 {
		return errors.New("用法: enna audit <verify|show> [flags]")
	}
	switch argv[0] {
	case "verify":
		path, err := parseAuditFlags("audit verify", argv[1:], stdout)
		if err != nil {
			return err
		}
		vr, err := audit.Verify(path)
		if err != nil {
			return err
		}
		if !vr.OK() {
			// 链断裂是安全事件，必须非零退出 —— 否则脚本无法据此告警
			return fmt.Errorf("%w: 第 %d 条 (seq=%d): %s",
				audit.ErrChainBroken, vr.FirstBad, vr.BadSeq, vr.Reason)
		}
		fmt.Fprintf(stdout, "审计链完整\n")
		fmt.Fprintf(stdout, "  文件:   %s\n", path)
		fmt.Fprintf(stdout, "  记录数: %d\n", vr.Records)
		fmt.Fprintf(stdout, "  链尾:   seq=%d hash=%s\n", vr.LastSeq, shortHash(vr.LastHash))
		return nil

	case "show":
		fs := flag.NewFlagSet("audit show", flag.ContinueOnError)
		cfgPath := fs.String("config", "configs/enna.yaml", "配置文件路径")
		file := fs.String("file", "", "直接指定审计文件（覆盖配置文件）")
		tail := fs.Int("tail", 20, "只显示最后 N 条；<=0 表示全部")
		if err := fs.Parse(argv[1:]); err != nil {
			return err
		}
		path, err := resolveAuditPath(*cfgPath, *file)
		if err != nil {
			return err
		}
		recs, err := audit.Read(path, *tail)
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			fmt.Fprintf(stdout, "（无审计记录）%s\n", path)
			return nil
		}
		w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SEQ\t时间\t主体\t设备\t技能\t风险\t判定\t退出码")
		for _, r := range recs {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
				r.Seq,
				r.TS.Local().Format("01-02 15:04:05"),
				dash(r.Principal),
				dash(r.DeviceID),
				dash(r.Skill),
				dash(r.Risk),
				dash(r.Decision),
				r.ExitCode,
			)
		}
		return w.Flush()

	default:
		return fmt.Errorf("未知的 audit 子命令: %s", argv[0])
	}
}

// parseAuditFlags 解析只带 --config/--file 的审计子命令。
func parseAuditFlags(name string, argv []string, _ io.Writer) (string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	cfgPath := fs.String("config", "configs/enna.yaml", "配置文件路径")
	file := fs.String("file", "", "直接指定审计文件（覆盖配置文件）")
	if err := fs.Parse(argv); err != nil {
		return "", err
	}
	return resolveAuditPath(*cfgPath, *file)
}

// resolveAuditPath 优先用显式 --file；否则从配置里取 audit.path。
func resolveAuditPath(cfgPath, file string) (string, error) {
	if file != "" {
		return file, nil
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return "", err
	}
	return cfg.Audit.Path, nil
}

func shortHash(h string) string {
	if len(h) <= 16 {
		return h
	}
	return h[:16] + "…"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
