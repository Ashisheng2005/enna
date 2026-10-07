// Package audit 实现 append-only 的哈希链审计。
//
// 设计依据（docs/00-requirements.md / docs/02-security-model.md §8）：
//   - FR-AU-01：所有工具调用写入哈希链审计
//   - FR-AU-02：写入必须 fsync；审计不可用时拒绝 L1 以上动作（fail-closed）
//   - FR-AU-03：审计链可校验
//   - FR-AU-07：记录不可删除，只可导出
//   - FR-AU-04：落盘前脱敏，并记录命中次数
//
// # 链式结构
//
//	hash_n = SHA256( canonical(record_n) ‖ hash_{n-1} )
//
// 它保证的是"每条记录都承诺了它之前的全部历史"。改动任何一条历史记录，
// 从那条开始的所有哈希都对不上 —— 篡改无法只藏在一处。
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Record 是一条审计记录。
//
// ⚠️ 关键约束：字段只能**追加到末尾**，不得插入、重命名或调整顺序。
//
// 原因：ComputeHash 依赖 encoding/json 对 struct 的**字段声明顺序**做稳定序列化。
// 若改用 map[string]any，encoding/json 会按键名排序，而字段顺序会随实现与版本变化，
// 导致历史记录的哈希全部失效 —— 审计链会自己把自己搞断。
//
// 用 struct = 用字段声明顺序当作稳定的序列化契约。
type Record struct {
	Seq       uint64    `json:"seq"`
	TS        time.Time `json:"ts"`
	TaskID    string    `json:"task_id,omitempty"`
	Principal string    `json:"principal,omitempty"`
	BotID     uint64    `json:"bot_id,omitempty"`
	Channel   string    `json:"channel,omitempty"`
	DeviceID  string    `json:"device_id,omitempty"`
	Skill     string    `json:"skill,omitempty"`
	// ArgsHash 是调用参数的摘要（原文不入链，见 artifacts）
	ArgsHash string `json:"args_hash,omitempty"`
	Risk     string `json:"risk,omitempty"`
	// Decision 是策略判定结果：ALLOW / NEED_APPROVAL / DENY / APPROVED / CONSUMED
	Decision string `json:"decision"`
	DenyCode string `json:"deny_code,omitempty"`
	// ExitCode 为 -1 表示未执行到远端（例如被拒绝或传输失败）
	ExitCode   int    `json:"exit_code"`
	StdoutHash string `json:"stdout_hash,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	// Redactions 是本次内容脱敏的命中次数，用于发现"某技能总吐敏感内容"这个模式
	Redactions int `json:"redactions,omitempty"`
	// TicketID 关联审批票据（L2/L3）
	TicketID string `json:"ticket_id,omitempty"`
	// PrevHash 指向前一条记录的 Hash；第一条为空串
	PrevHash string `json:"prev_hash"`
	// Hash 是本条记录的链式哈希
	Hash string `json:"hash"`
}

// ComputeHash 计算本条记录的链式哈希。
//
// 三个"为什么"，想清楚这三个 M0 的审计链就真的掌握了：
//
//  1. 为什么先把自己的 Hash 字段清空？
//     否则就是"用哈希算哈希"，永远算不出稳定值。
//     这也让 Verify 可以在不改动记录的前提下重算比对。
//
//  2. 为什么用 struct 而不是 map 来序列化？
//     encoding/json 对 map 的键**排序**，对 struct 按**字段声明顺序**输出。
//     用 map 的话，字段顺序会随实现/版本变化 → 历史哈希全部失效 → 审计链自断。
//
//  3. 为什么要把 PrevHash 写进哈希输入（而不是只存成字段）？
//     链式结构的本质是"每条记录承诺它前面的全部历史"。
//     只存字段而不参与计算，等于没有链：删掉中间一条，剩余记录的自身哈希仍然自洽。
func (r Record) ComputeHash() string {
	c := r
	c.Hash = "" // 关键 1：不能把 hash 算进自己
	b, err := json.Marshal(c)
	if err != nil {
		// Record 全是可序列化类型；走到这里说明有人加了不可序列化的字段
		panic("audit: record 无法序列化: " + err.Error())
	}
	h := sha256.New()
	h.Write(b)
	h.Write([]byte(r.PrevHash)) // 关键 3：把前序历史纳入承诺
	return hex.EncodeToString(h.Sum(nil))
}

// HashBytes 返回内容的 sha256 十六进制摘要，用于参数与输出的指纹。
//
// 链上只存摘要、全文另存 artifacts：兼顾可追溯与体积。
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
