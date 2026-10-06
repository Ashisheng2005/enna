package audit

// ══════════════════════════════════════════════════════════════════════
//
//	📝 练习题：Read 的实现已注释掉。
//	自测：go test ./internal/audit/ -run TestReadTail -v
//
// ══════════════════════════════════════════════════════════════════════

// 提示：实现 Read 时需要这些包。
// "bufio"          // 手动分行（与 Verify 同样的理由）
// "encoding/json"  // 解析每一行
// "fmt"            // 错误包装
// "os"             // 打开文件

// Read 读取审计文件，返回记录切片。
//
// tail > 0 时只返回最后 tail 条（用于 `enna audit show --tail N`）；
// tail <= 0 时返回全部。
//
// 刻意不复用 Verify：Verify 关注"链是否完整"，Read 关注"内容是什么"。
// 两者查的东西不同，混在一起会让错误处理含糊。
func Read(path string, tail int) ([]Record, error) {
	// ┌── 参考实现 ────────────────────────────────────────────────────
	// │ f, err := os.Open(path)
	// │ if err != nil {
	// │ 	return nil, fmt.Errorf("audit: 打开 %s: %w", path, err)
	// │ }
	// │ defer f.Close()
	// │
	// │ var all []Record
	// │ br := bufio.NewReaderSize(f, 256*1024)
	// │ for {
	// │ 	line, readErr := br.ReadBytes('\n')
	// │ 	if len(line) > 0 {
	// │ 		var r Record
	// │ 		// 用 Unmarshal 的返回值判断是否为空行，避免手工裁剪出错
	// │ 		if err := json.Unmarshal(line, &r); err == nil && r.Seq != 0 {
	// │ 			all = append(all, r)
	// │ 		}
	// │ 	}
	// │ 	if readErr != nil {
	// │ 		break
	// │ 	}
	// │ }
	// │ if tail > 0 && len(all) > tail {
	// │ 	all = all[len(all)-tail:]
	// │ }
	// │ return all, nil
	// └───────────────────────────────────────────────────────────────
	// 注意：切片截取 all[len(all)-tail:] 是共享底层数组的视图，不是拷贝。
	// 对本函数无害（马上就返回了），但你要知道这个语义。
	panic("TODO(enna): audit.Read 尚未实现")
}
