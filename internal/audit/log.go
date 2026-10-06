package audit

// ══════════════════════════════════════════════════════════════════════
//
//	📝 练习题：本文件的【核心逻辑已注释掉】，请自行重写。
//	重写：Open（状态恢复 + 拒绝被篡改的历史）、Append（fsync + 毒化）、
//	      Verify（逐行重算 + 前序链接校验）
//	已给：Path / LastHash / Close / VerifyResult / OK
//	自测：go test ./internal/audit/ -v
//
// ══════════════════════════════════════════════════════════════════════

import (
	"errors"
	"os" // Log.f 的字段类型是 *os.File，所以这个 import 必须留着
	"sync"

	// 提示：实现上面三个函数时你会需要下面这些包。
	// 先把它们注释掉，是为了让未实现的骨架也能编译通过。
	// "bufio"          // Verify：手动分行（不要用 Scanner，见 Verify 注释）
	// "bytes"          // Verify：裁掉行尾的 \r\n
	// "encoding/json"  // Append / Verify：序列化与解析
	// "fmt"            // 错误包装
	// "io"             // Verify：判断 EOF
	// "time"           // Append：填充时间戳
)

// ErrChainBroken 表示审计链校验失败。
var ErrChainBroken = errors.New("audit: 哈希链断裂")

// ErrLogPoisoned 表示日志已失效，后续写入一律拒绝。
var ErrLogPoisoned = errors.New("audit: 日志已失效")

// Log 是一个 append-only 的审计日志。
//
// 并发安全：Append 内部加锁。审计写入频率是运维操作级别（每秒个位数），
// 用一把互斥锁足够，不需要更复杂的结构。
type Log struct {
	mu       sync.Mutex
	f        *os.File
	path     string
	seq      uint64
	lastHash string

	// failed 一旦非 nil，后续 Append 一律拒绝。
	// 这是 fail-closed 的落点：写入失败后我们无法确定磁盘上到底有没有这条记录，
	// 继续写可能产生重复 seq 或断裂的 prev_hash —— 宁可停摆也不能污染审计链。
	failed error
}

// Open 打开（或创建）审计文件，并从现有内容恢复 seq 与 lastHash。
//
// # 要回答的三个"为什么"
//
//  1. 为什么要从磁盘恢复 seq 与 lastHash？
//     进程重启后若从 seq=0、prevHash="" 重新开始，新记录会与历史记录撞号，
//     审计链当场断裂。
//
//  2. 为什么打开前要先 Verify 既有内容？
//     若历史已被篡改，应当**拒绝启动**而不是继续往上叠。
//     继续叠加等于把新记录焊死在一段可疑的历史上，事后无法分辨哪些是干净的。
//
//  3. 为什么用 O_APPEND 和 0600？
//     O_APPEND 让内核保证追加写入的原子性，多进程同时写也不会互相覆盖；
//     0600 让审计文件只有本进程用户可读（内容含设备与操作信息）。
func Open(path string) (*Log, error) {
	// ┌── 参考实现 ────────────────────────────────────────────────────
	// │ l := &Log{path: path}
	// │
	// │ // 先校验既有内容，再追加
	// │ if st, err := os.Stat(path); err == nil && st.Size() > 0 {
	// │ 	vr, err := Verify(path)
	// │ 	if err != nil {
	// │ 		return nil, fmt.Errorf("audit: 打开前校验: %w", err)
	// │ 	}
	// │ 	if vr.FirstBad != 0 {
	// │ 		return nil, fmt.Errorf("%w: 第 %d 条 (seq=%d): %s",
	// │ 			ErrChainBroken, vr.FirstBad, vr.BadSeq, vr.Reason)
	// │ 	}
	// │ 	l.seq = vr.LastSeq
	// │ 	l.lastHash = vr.LastHash
	// │ } else if err != nil && !os.IsNotExist(err) {
	// │ 	return nil, fmt.Errorf("audit: 检查 %s: %w", path, err)
	// │ }
	// │
	// │ f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	// │ if err != nil {
	// │ 	return nil, fmt.Errorf("audit: 打开 %s: %w", path, err)
	// │ }
	// │ l.f = f
	// │ return l, nil
	// └───────────────────────────────────────────────────────────────
	panic("TODO(enna): audit.Open 尚未实现")
}

// Path 返回审计文件路径。
func (l *Log) Path() string { return l.path }

// LastHash 返回当前链头哈希（用于状态展示与测试）。
func (l *Log) LastHash() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastHash
}

// Append 追加一条记录，返回补齐了 Seq/TS/PrevHash/Hash 的完整记录。
//
// 成功返回的前提是数据已 fsync 落盘 —— 这是 fail-closed 的物理基础：
// 若只写进 OS 页缓存，断电后"记录过的操作"就在磁盘上不存在，审计失去意义。
//
// # 要回答的两个"为什么"
//
//  1. 为什么 Write 之后还要 Sync？
//     见上方注释。代价是每次操作一次 fsync（毫秒级），
//     对运维操作频率来说完全可接受。
//
//  2. 为什么写入或 Sync 失败后要把 l.failed 置位（毒化），而不是简单返回错误？
//     因为失败后我们**无法确定**那条记录到底有没有落盘：
//     若已落盘而我们回滚了 seq，下一次 Append 会重用同一个 seq、
//     并用旧的 lastHash 作为 PrevHash → 文件里出现重复 seq 或断链。
//     宁可整体停摆（fail-closed），也不能污染审计链。
func (l *Log) Append(r Record) (Record, error) {
	// ┌── 参考实现 ────────────────────────────────────────────────────
	// │ l.mu.Lock()
	// │ defer l.mu.Unlock()
	// │
	// │ if l.f == nil {
	// │ 	return Record{}, errors.New("audit: Log 已关闭")
	// │ }
	// │ if l.failed != nil {
	// │ 	return Record{}, fmt.Errorf("%w: %v", ErrLogPoisoned, l.failed)
	// │ }
	// │
	// │ r.Seq = l.seq + 1
	// │ if r.TS.IsZero() {
	// │ 	r.TS = time.Now().UTC()
	// │ }
	// │ r.PrevHash = l.lastHash
	// │ r.Hash = r.ComputeHash()
	// │
	// │ line, err := json.Marshal(r)
	// │ if err != nil {
	// │ 	return Record{}, fmt.Errorf("audit: 序列化: %w", err)
	// │ }
	// │
	// │ if _, err := l.f.Write(append(line, '\n')); err != nil {
	// │ 	l.failed = err
	// │ 	return Record{}, fmt.Errorf("audit: 写入: %w", err)
	// │ }
	// │ if err := l.f.Sync(); err != nil {
	// │ 	l.failed = err
	// │ 	return Record{}, fmt.Errorf("audit: fsync: %w", err)
	// │ }
	// │
	// │ l.seq = r.Seq
	// │ l.lastHash = r.Hash
	// │ return r, nil
	// └───────────────────────────────────────────────────────────────
	panic("TODO(enna): Log.Append 尚未实现")
}

// Close 关闭日志。
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// VerifyResult 描述一次链校验的结果。
type VerifyResult struct {
	// Records 是成功校验过的记录数
	Records int
	// FirstBad 是第一条出问题的记录序号（1-based）；0 表示链完整
	FirstBad int
	// BadSeq 是出问题那条记录自报的 Seq
	BadSeq uint64
	// Reason 是失败原因（人类可读）
	Reason string
	// LastSeq / LastHash 是链尾状态，供 Log.Open 恢复
	LastSeq  uint64
	LastHash string
}

// OK 表示链完整。
func (v VerifyResult) OK() bool { return v.FirstBad == 0 }

// Verify 逐行校验审计链。
//
// 两件事都要查，缺一不可：
//
//  1. 每条记录自身的 Hash 是否等于重算值  → 防**内容**被改
//  2. 每条记录的 PrevHash 是否等于上一条的 Hash → 防整条被**删除或调换**
//
// 只做第 1 条是不够的：删掉中间一条记录，剩余记录的自身哈希仍然自洽。
func Verify(path string) (VerifyResult, error) {
	// ┌── 参考实现 ────────────────────────────────────────────────────
	// │ f, err := os.Open(path)
	// │ if err != nil {
	// │ 	return VerifyResult{}, fmt.Errorf("audit: 打开 %s: %w", path, err)
	// │ }
	// │ defer f.Close()
	// │
	// │ var res VerifyResult
	// │ var prev string
	// │
	// │ // 不用 bufio.Scanner：它的默认 token 上限是 64KB，超长行会**静默停止**
	// │ // 扫描，结果是"校验通过"但只校验了前半段。用 ReadBytes 手动分行才可靠。
	// │ br := bufio.NewReaderSize(f, 256*1024)
	// │ for {
	// │ 	line, readErr := br.ReadBytes('\n')
	// │ 	trimmed := bytes.TrimRight(line, "\r\n")
	// │ 	if len(trimmed) > 0 {
	// │ 		res.Records++
	// │ 		n := res.Records
	// │
	// │ 		var r Record
	// │ 		if err := json.Unmarshal(trimmed, &r); err != nil {
	// │ 			res.FirstBad, res.Reason = n, "JSON 解析失败: "+err.Error()
	// │ 			return res, nil
	// │ 		}
	// │ 		if got := r.ComputeHash(); got != r.Hash {
	// │ 			res.FirstBad, res.BadSeq = n, r.Seq
	// │ 			res.Reason = fmt.Sprintf("哈希不匹配（记录自称 %s，重算得 %s）", r.Hash, got)
	// │ 			return res, nil
	// │ 		}
	// │ 		if r.PrevHash != prev {
	// │ 			res.FirstBad, res.BadSeq = n, r.Seq
	// │ 			res.Reason = fmt.Sprintf("prev_hash 断裂（记录为 %q，期望 %q）", r.PrevHash, prev)
	// │ 			return res, nil
	// │ 		}
	// │ 		prev = r.Hash
	// │ 		res.LastSeq, res.LastHash = r.Seq, r.Hash
	// │ 	}
	// │ 	if readErr == io.EOF {
	// │ 		break
	// │ 	}
	// │ 	if readErr != nil {
	// │ 		return res, fmt.Errorf("audit: 读取 %s: %w", path, readErr)
	// │ 	}
	// │ }
	// │ return res, nil
	// └───────────────────────────────────────────────────────────────
	panic("TODO(enna): audit.Verify 尚未实现")
}
