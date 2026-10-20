package sessions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pi-bridge-go/internal/protocol"
)

// 默认的 FileBytes 上限必须容得下真实存在的会话。
//
// 这条测试的来源是一个真实故障：本机有一个 86.8 MB 的 CLI 会话（跑了几周），
// 点开只显示「历史文件超过体积上限」——完全无法浏览。而实测扫描它只需要
// 125 ms、堆 +4.6 MB（见 DefaultLimits 的注释），把 64 MiB 当作硬墙属于
// 过度保守：代价是用户彻底看不到内容，换来的只是几十毫秒。
//
// 允许的上限对应「打开时要等多久」：262 MB 的会话冷扫描 393 ms，
// 所以 256 MiB 是「不误伤真实会话」与「不拖垮桥」之间的分界。
func Test默认上限容得下真实大会话(t *testing.T) {
	const realLargest = 91_084_270 // 本机实际最大的会话（86.9 MiB）
	// FileBytes 在 2026-10-21 重设计后**只**约束全扫路径（深翻页退化、
	// 分支树、导出）：打开会话是尾部读取，与文件总体积无关，不再检查。
	// 所以它的取值不再决定「能不能打开」，只决定「深翻页最坏要等多久」。
	if DefaultLimits().FileBytes < realLargest {
		t.Fatalf("FileBytes=%s 容不下真实存在的 %s 会话", humanBytes(DefaultLimits().FileBytes), humanBytes(realLargest))
	}
	// 2 GiB 的全扫约需数秒，但只发生在用户显式「加载更早」且文件确实
	// 巨大时；首页与相邻翻页永远落在尾部窗口内，不受此值影响。
	if limit := DefaultLimits().FileBytes; limit != 2<<30 {
		t.Fatalf("FileBytes 应为 2 GiB（全扫天花板，见 store.go 注释），实际 %s", humanBytes(limit))
	}
	// 每页字节预算提到 8 MiB：预算按条累计、一次交付，提到 8 MiB 是为了
	// 让「大工具输出密集」的会话一页能装下更多条；常规会话远用不满。
	if got := DefaultLimits().PageBytes; got != 8<<20 {
		t.Fatalf("PageBytes 应为 8 MiB，实际 %s", humanBytes(int64(got)))
	}
}

// 全扫路径（导出）超限时错误必须报出实际大小与上限。
//
// 只说「超过体积上限」时用户无从判断：是略微超一点，还是大了几十倍？
// 两个数字都给出才有可操作性。（History/lazy/metadata 已不再拒开——
// 那是尾部读取路径，与文件总体积无关；错误契约由仍然全扫的导出钉住。）
func Test超限错误报出实际大小与上限(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	// 造一个 3 条记录的正常会话，把上限压到装不下它。
	id := writeSession(t, dir, "big", cwd,
		entryRole("u1", "", "user"),
		entryRole("a1", "u1", "assistant"),
		entryRole("u2", "a1", "user"),
	)

	limits := DefaultLimits()
	limits.FileBytes = 64 // 故意压到远小于真实文件
	store, err := New(dir, mustPolicy(t, cwd), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, err = store.ExportDocument(context.Background(), id, DefaultExportLimits())
	if err == nil {
		t.Fatal("全扫路径超过 FileBytes 必须被拒绝")
	}
	msg := err.Error()
	if !strings.Contains(msg, humanBytes(limits.FileBytes)) {
		t.Fatalf("错误里应出现上限数值 %s，实际: %s", humanBytes(limits.FileBytes), msg)
	}
	if strings.Count(msg, "B") < 2 && !strings.Contains(msg, "KiB") && !strings.Contains(msg, "MiB") {
		t.Fatalf("错误里应同时出现实际大小与上限，实际: %s", msg)
	}
	// 错误码必须保持稳定：前端与协议按它分支，不能因为文案变具体就改码。
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != "limit_exceeded" {
		t.Fatalf("错误码应保持 limit_exceeded，实际 %v", err)
	}
}

// humanBytes 的输出是给用户看的，边界要稳。
func Test字节格式化边界(t *testing.T) {
	cases := map[int64]string{
		0:          "0 B",
		1023:       "1023 B",
		1024:       "1.0 KiB",
		64 << 20:   "64.0 MiB",
		256 << 20:  "256.0 MiB",
		91_094_270: "86.9 MiB", // 真实大会话的字节数（91 084 270）
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Fatalf("humanBytes(%d) = %q，期望 %q", in, got, want)
		}
	}
}
