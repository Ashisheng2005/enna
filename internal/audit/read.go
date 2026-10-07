package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// Read 读取审计文件，返回记录切片。
//
// tail > 0 时只返回最后 tail 条（用于 `enna audit show --tail N`）；
// tail <= 0 时返回全部。
//
// 刻意不复用 Verify：Verify 关注"链是否完整"，Read 关注"内容是什么"。
// 两者查的东西不同，混在一起会让错误处理含糊 ——
// 比如"JSON 解析失败"在 Verify 里是安全事件，在 Read 里只是跳过一行。
func Read(path string, tail int) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("audit: 打开 %s: %w", path, err)
	}
	defer f.Close()

	var all []Record
	// 与 Verify 同理：不用 bufio.Scanner（64KB 静默截断）
	br := bufio.NewReaderSize(f, 256*1024)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			var r Record
			// 用 Unmarshal 的返回值判断是否为空行，避免手工裁剪出错
			if err := json.Unmarshal(line, &r); err == nil && r.Seq != 0 {
				all = append(all, r)
			}
		}
		if readErr != nil {
			break
		}
	}
	if tail > 0 && len(all) > tail {
		// 注意：切片截取是共享底层数组的视图，不是拷贝。
		// 对本函数无害（马上就返回了），但你要知道这个语义。
		all = all[len(all)-tail:]
	}
	return all, nil
}
