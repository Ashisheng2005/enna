package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── 测试辅助 ──

func newLog(t *testing.T) (*Log, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(p)
	if err != nil {
		t.Fatalf("Open(%s): %v", p, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, p
}

func appendN(t *testing.T, l *Log, n int) []Record {
	t.Helper()
	out := make([]Record, 0, n)
	for i := 0; i < n; i++ {
		r, err := l.Append(Record{
			Principal: "10001",
			DeviceID:  "lab01",
			Skill:     "host.disk",
			Risk:      "L0",
			Decision:  "ALLOW",
			ExitCode:  0,
		})
		if err != nil {
			t.Fatalf("Append #%d: %v", i+1, err)
		}
		out = append(out, r)
	}
	return out
}

// rewriteLine 把文件第 n 行（1-based）解析成 map、交给 mut 修改、再写回。
//
// 注意这里故意用 map 重写整行：字段顺序会变，但 Verify 是重新解析成 struct
// 再计算哈希的，所以顺序变化不影响校验结果 —— 这正好验证了
// "哈希绑定的是语义，不是文件字节"这个设计意图。
func rewriteLine(t *testing.T, path string, n int, mut func(map[string]any)) {
	t.Helper()
	lines := readLines(t, path)
	if n < 1 || n > len(lines) {
		t.Fatalf("rewriteLine: 行号 %d 越界（共 %d 行）", n, len(lines))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[n-1]), &m); err != nil {
		t.Fatalf("解析第 %d 行: %v", n, err)
	}
	mut(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("序列化第 %d 行: %v", n, err)
	}
	lines[n-1] = string(b)
	writeLines(t, path, lines)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 %s: %v", path, err)
	}
}

// ── 哈希语义 ──

func TestComputeHashIgnoresOwnHashField(t *testing.T) {
	base := Record{Skill: "host.disk", PrevHash: "prev", Decision: "ALLOW"}
	a := base.ComputeHash()

	base.Hash = "deadbeef" // 伪造 Hash 字段不应影响计算结果
	b := base.ComputeHash()

	if a != b {
		t.Fatalf("ComputeHash 把 Hash 字段算进去了：%s != %s", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("期望 sha256 十六进制长度 64，实际 %d（%q）", len(a), a)
	}
}

func TestComputeHashIsSensitiveToEveryField(t *testing.T) {
	base := Record{
		Seq: 1, Principal: "10001", BotID: 10001, Channel: "PRIVATE",
		DeviceID: "lab01", Skill: "host.disk", ArgsHash: "aaa",
		Risk: "L0", Decision: "ALLOW", DenyCode: "", ExitCode: 0,
		StdoutHash: "bbb", Redactions: 0, DurationMS: 12,
		TicketID: "", PrevHash: "ppp",
	}
	want := base.ComputeHash()

	mutations := map[string]func(r *Record){
		"Seq":        func(r *Record) { r.Seq = 2 },
		"Principal":  func(r *Record) { r.Principal = "10002" },
		"BotID":      func(r *Record) { r.BotID = 10002 },
		"Channel":    func(r *Record) { r.Channel = "GROUP" },
		"DeviceID":   func(r *Record) { r.DeviceID = "lab02" },
		"Skill":      func(r *Record) { r.Skill = "service.restart" },
		"ArgsHash":   func(r *Record) { r.ArgsHash = "zzz" },
		"Risk":       func(r *Record) { r.Risk = "L2" },
		"Decision":   func(r *Record) { r.Decision = "DENY" },
		"DenyCode":   func(r *Record) { r.DenyCode = "NO_DEVICE_ACL" },
		"ExitCode":   func(r *Record) { r.ExitCode = 1 },
		"StdoutHash": func(r *Record) { r.StdoutHash = "ccc" },
		"Redactions": func(r *Record) { r.Redactions = 3 },
		"DurationMS": func(r *Record) { r.DurationMS = 99 },
		"TicketID":   func(r *Record) { r.TicketID = "t-1" },
		"PrevHash":   func(r *Record) { r.PrevHash = "qqq" },
	}

	for name, mut := range mutations {
		t.Run(name, func(t *testing.T) {
			r := base
			mut(&r)
			if got := r.ComputeHash(); got == want {
				t.Fatalf("改动 %s 后哈希未变化（%s）—— 该字段没有进入哈希承诺范围", name, got)
			}
		})
	}
}

// ── 链的构建与校验 ──

func TestAppendBuildsChain(t *testing.T) {
	l, p := newLog(t)
	recs := appendN(t, l, 3)

	for i, r := range recs {
		if r.Seq != uint64(i+1) {
			t.Errorf("第 %d 条 Seq = %d，期望 %d", i+1, r.Seq, i+1)
		}
		if r.TS.IsZero() {
			t.Errorf("第 %d 条 TS 未填充", i+1)
		}
		wantPrev := ""
		if i > 0 {
			wantPrev = recs[i-1].Hash
		}
		if r.PrevHash != wantPrev {
			t.Errorf("第 %d 条 PrevHash = %q，期望 %q", i+1, r.PrevHash, wantPrev)
		}
		if r.Hash != r.ComputeHash() {
			t.Errorf("第 %d 条 Hash 与重算值不一致", i+1)
		}
	}

	vr, err := Verify(p)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !vr.OK() {
		t.Fatalf("链应为完整，实际第 %d 条出错: %s", vr.FirstBad, vr.Reason)
	}
	if vr.Records != 3 {
		t.Fatalf("Records = %d，期望 3", vr.Records)
	}
	if vr.LastSeq != 3 || vr.LastHash != recs[2].Hash {
		t.Fatalf("链尾状态错误: seq=%d hash=%s", vr.LastSeq, vr.LastHash)
	}
}

func TestVerifyDetectsTamperedContent(t *testing.T) {
	l, p := newLog(t)
	appendN(t, l, 3)

	rewriteLine(t, p, 2, func(m map[string]any) {
		m["exit_code"] = float64(1) // 把第 2 条改成"执行失败"
	})

	vr, err := Verify(p)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if vr.OK() {
		t.Fatal("内容被篡改，Verify 却报告链完整")
	}
	if vr.FirstBad != 2 {
		t.Fatalf("应定位到第 2 条，实际第 %d 条（%s）", vr.FirstBad, vr.Reason)
	}
}

func TestVerifyDetectsRemovedRecord(t *testing.T) {
	l, p := newLog(t)
	appendN(t, l, 3)

	lines := readLines(t, p)
	if len(lines) != 3 {
		t.Fatalf("前置条件：期望 3 行，实际 %d 行", len(lines))
	}
	writeLines(t, p, []string{lines[0], lines[2]}) // 抽掉中间那条

	vr, err := Verify(p)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if vr.OK() {
		t.Fatal("记录被删除，Verify 却报告链完整 —— prev_hash 校验没起作用")
	}
	if vr.FirstBad != 2 {
		t.Fatalf("应定位到第 2 条，实际第 %d 条（%s）", vr.FirstBad, vr.Reason)
	}
	if !strings.Contains(vr.Reason, "prev_hash") {
		t.Fatalf("原因应指向 prev_hash 断裂，实际: %s", vr.Reason)
	}
}

func TestReopenContinuesChain(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")

	l1, err := Open(p)
	if err != nil {
		t.Fatalf("首次 Open: %v", err)
	}
	first := appendN(t, l1, 2)
	if err := l1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重开：seq 与 prev_hash 必须从磁盘恢复，而不是从 0 重新开始
	l2, err := Open(p)
	if err != nil {
		t.Fatalf("重新 Open: %v", err)
	}
	defer l2.Close()
	third, err := l2.Append(Record{Decision: "ALLOW"})
	if err != nil {
		t.Fatalf("重开后的 Append: %v", err)
	}

	if third.Seq != 3 {
		t.Fatalf("重开后 Seq = %d，期望 3（说明没有从磁盘恢复状态）", third.Seq)
	}
	if third.PrevHash != first[1].Hash {
		t.Fatalf("重开后 PrevHash = %q，期望 %q", third.PrevHash, first[1].Hash)
	}

	vr, err := Verify(p)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !vr.OK() {
		t.Fatalf("重开后链应完整，实际第 %d 条出错: %s", vr.FirstBad, vr.Reason)
	}
}

func TestOpenRefusesTamperedLog(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")

	l, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	appendN(t, l, 2)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rewriteLine(t, p, 1, func(m map[string]any) { m["skill"] = "shell.run" })

	if _, err := Open(p); err == nil {
		t.Fatal("历史被篡改时 Open 应当失败（fail-closed），实际成功了")
	}
}

func TestAppendAfterCloseFails(t *testing.T) {
	l, _ := newLog(t)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := l.Append(Record{Decision: "ALLOW"}); err == nil {
		t.Fatal("Close 之后 Append 应当返回错误")
	}
}

func TestVerifyEmptyFileIsOK(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatalf("准备空文件: %v", err)
	}
	vr, err := Verify(p)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !vr.OK() || vr.Records != 0 {
		t.Fatalf("空文件应校验通过且 0 条，实际 OK=%v Records=%d", vr.OK(), vr.Records)
	}
}

// ── 读取 ──

func TestReadTail(t *testing.T) {
	l, p := newLog(t)
	appendN(t, l, 5)

	all, err := Read(p, 0)
	if err != nil {
		t.Fatalf("Read(0): %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("Read(0) 返回 %d 条，期望 5", len(all))
	}

	tail, err := Read(p, 2)
	if err != nil {
		t.Fatalf("Read(2): %v", err)
	}
	if len(tail) != 2 {
		t.Fatalf("Read(2) 返回 %d 条，期望 2", len(tail))
	}
	if tail[0].Seq != 4 || tail[1].Seq != 5 {
		t.Fatalf("Read(2) 应返回 seq 4,5，实际 %d,%d", tail[0].Seq, tail[1].Seq)
	}
}

// ── 脱敏 ──

func TestRedact(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantOut   string
		wantHits  int
	}{
		{
			name:     "命令行密码",
			in:       "mysql -u root --password=hunter2 -e 'select 1'",
			wantOut:  "mysql -u root --password=[REDACTED] -e 'select 1'",
			wantHits: 1,
		},
		{
			name:     "ENV 风格变量（下划线前不能加 \\b）",
			in:       "DB_PASSWORD=hunter2",
			wantOut:  "DB_PASSWORD=[REDACTED]",
			wantHits: 1,
		},
		{
			name:     "逗号分隔的 token",
			in:       "token: abc123",
			wantOut:  "token=[REDACTED]",
			wantHits: 1,
		},
		{
			name:     "api_key 变体",
			in:       "api_key=xyz",
			wantOut:  "api_key=[REDACTED]",
			wantHits: 1,
		},
		{
			name:     "Bearer 头",
			in:       "Authorization: Bearer eyJhbGciOi",
			wantOut:  "Authorization: Bearer [REDACTED]",
			wantHits: 1,
		},
		{
			name: "私钥块",
			in: "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----",
			wantOut:  "[REDACTED PRIVATE KEY]",
			wantHits: 1,
		},
		{
			name:     "普通文本不动",
			in:       "nginx is active (running)",
			wantOut:  "nginx is active (running)",
			wantHits: 0,
		},
		{
			name:     "tokenizer 不应误伤",
			in:       "tokenizer=standard",
			wantOut:  "tokenizer=standard",
			wantHits: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, hits := Redact([]byte(tt.in))
			if string(got) != tt.wantOut {
				t.Fatalf("Redact 输出不符\n输入: %s\n实际: %s\n期望: %s", tt.in, got, tt.wantOut)
			}
			if hits != tt.wantHits {
				t.Fatalf("命中次数 = %d，期望 %d", hits, tt.wantHits)
			}
		})
	}
}

func TestRedactCountsMultipleHits(t *testing.T) {
	in := "password=a\npassword=b\nsecret=c"
	_, hits := Redact([]byte(in))
	if hits != 3 {
		t.Fatalf("命中次数 = %d，期望 3", hits)
	}
}
