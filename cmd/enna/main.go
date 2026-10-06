// Command enna 是 Enna 设备的唯一可执行入口。
//
// M0 阶段三个角色（qqgw / executor / CLI）都在同一个二进制里，
// 但它们分属不同 package，依赖单向 —— 将来拆进程时内核代码一行都不用改。
// 详见 docs/01-architecture.md §5.2 依赖纪律。
package main

import (
	"os"

	"github.com/Ashisheng2005/enna/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args, os.Stdout, os.Stderr))
}
