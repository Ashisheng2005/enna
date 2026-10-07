package audit

import "regexp"

// redactPattern 是一条脱敏规则。
type redactPattern struct {
	name string
	re   *regexp.Regexp
	// repl 用 Go 的 ReplaceAll 语法，$1 之类的分组引用可用
	repl string
}

// 脱敏规则（FR-AU-04）。
//
// 两处刻意的设计决定：
//
//  1. 第一组**不加 \b 前缀**。因为 DB_PASSWORD=xxx 里的 PASSWORD 前面是下划线，
//     而 \b 在"下划线→字母"之间不算边界，加了 \b 反而会漏掉最常见的
//     ENV 风格变量名。去掉 \b 后靠后面的 \s*[=:] 来限定必须是赋值/键值形式
//     （所以 tokenizer=standard 不会被误伤）。
//
//  2. 统一替换为 key=[REDACTED]，把 : 归一成 = 是刻意的 ——
//     不保留原分隔符，避免"看起来一样但实际不同"的绕过。
var redactPatterns = []redactPattern{
	{
		"key-value",
		regexp.MustCompile(`(?i)(password|passwd|pwd|token|secret|api[_-]?key)\s*[=:]\s*\S+`),
		`${1}=[REDACTED]`,
	},
	{
		"private-key",
		regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
		`[REDACTED PRIVATE KEY]`,
	},
	{
		"bearer",
		regexp.MustCompile(`(?i)authorization:\s*bearer\s+\S+`),
		`Authorization: Bearer [REDACTED]`,
	},
	{
		"ssh-passphrase",
		regexp.MustCompile(`(?i)passphrase\s*[=:]\s*\S+`),
		`passphrase=[REDACTED]`,
	},
}

// Redact 对内容做脱敏，返回脱敏后的内容与命中次数。
//
// # 两个"为什么"
//
//  1. 为什么要返回命中次数？
//     次数要写进审计（Record.Redactions）。它能在事后暴露
//     "某个技能总是吐出敏感内容"这个模式，从而发现设计缺陷 ——
//     只是一次性脱敏的话，这个信号就丢了。
//
//  2. 为什么调用方必须在【落盘前】和【进入 LLM 上下文前】各调用一次？
//     只做一处等于没做：另一条路径会绕过脱敏。
//     两条路径的读者不同（磁盘 vs 模型供应商），泄露后果也不同。
//
// 实现上必须先计数再替换：替换之后原来的匹配就不存在了，数不出来。
func Redact(b []byte) ([]byte, int) {
	out := b
	total := 0
	for _, p := range redactPatterns {
		n := len(p.re.FindAllIndex(out, -1))
		if n == 0 {
			continue
		}
		total += n
		out = p.re.ReplaceAll(out, []byte(p.repl))
	}
	return out, total
}
