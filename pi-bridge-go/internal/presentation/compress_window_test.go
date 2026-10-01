package presentation

import (
	"bytes"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
)

// sampleFragment 生成确定性的、接近真实响应特征的样本：模板文本与 JSONL
// 内容混合的形态（部分重复结构 + 部分高熵载荷）。
func sampleFragment(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	out := make([]byte, 0, n+64)
	fragments := [][]byte{
		[]byte(`<div class="tool-call" data-ok="true"><summary>bash</summary><pre>`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"`),
		[]byte(`· 1s</span></div></details>`),
		[]byte(`\u4e2d\u6587\u6b63\u6587\u4e0e\u6807\u70b9\uff0c\u4ee5\u53ca 12345 `),
	}
	for len(out) < n {
		if r.Intn(5) == 0 {
			b := make([]byte, 1+r.Intn(40))
			for i := range b {
				b[i] = byte(32 + r.Intn(95))
			}
			out = append(out, b...)
		} else {
			out = append(out, fragments[r.Intn(len(fragments))]...)
		}
	}
	return out[:n]
}

// 桥的实际响应尺寸内（外壳 32 KB、历史片段 200–250 KB、记忆面板 37 KB），
// 统一窗口的压缩输出不得比库默认窗口差。这条锁住「为省内存而牺牲流量」的回退。
func TestBrotliWindowMatchesDefaultOnResponseSizes(t *testing.T) {
	for _, size := range []int{32 << 10, 200 << 10, 480 << 10} {
		raw := sampleFragment(size, int64(size))
		var got bytes.Buffer
		if _, err := Compress(&got, raw, EncBrotli); err != nil {
			t.Fatal(err)
		}
		var want bytes.Buffer
		ref := brotli.NewWriterOptions(&want, brotli.WriterOptions{Quality: 4, LGWin: 22})
		if _, err := ref.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := ref.Close(); err != nil {
			t.Fatal(err)
		}
		if got.Len() > want.Len() {
			t.Errorf("样本 %d KB：统一窗口输出 %d 字节，大于默认窗口的 %d 字节",
				size>>10, got.Len(), want.Len())
		}
	}
}

// 池化写入器的常驻内存必须显著小于库默认窗口。
//
// 为什么测「一个活着的 writer」而不是直接测池：sync.Pool 在两次 GC 后就会
// 清空，池里留存的量测不稳；而池保留的正是这个对象，它的常驻即峰值 RSS 的
// 单位成本（并发几个请求就留几份）。默认窗口（22）实测约 9.8 MiB，
// 统一窗口（19）约 2.8 MiB，阈值取 0.6 倍足以分辨，也不受测量噪声影响。
func TestBrotliPoolWriterFootprint(t *testing.T) {
	// 不并行：这条断言的是堆常驻量，与别的测试并行时 GC 压力互相干扰，
	// 测量值会失真（实测并行下判定被翻转）。
	footprint := func(lg int) float64 {
		best := 1e9
		for i := 0; i < 3; i++ {
			runtime.GC()
			runtime.GC()
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			base := ms.HeapAlloc
			w := brotli.NewWriterOptions(&bytes.Buffer{}, brotli.WriterOptions{Quality: 4, LGWin: lg})
			if _, err := w.Write(sampleFragment(300<<10, 11)); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&ms)
			runtime.KeepAlive(w)
			if v := float64(ms.HeapAlloc-base) / (1 << 20); v < best {
				best = v
			}
		}
		return best
	}
	small := footprint(brotliLGWin)
	def := footprint(22)
	if small > def*0.6 {
		t.Errorf("统一窗口（lgwin=%d）单写入器常驻 %.1f MiB，默认窗口（22）为 %.1f MiB；"+
			"前者应显著更小，否则池会按并发倍数放大常驻内存", brotliLGWin, small, def)
	}
	if small < 0.2 {
		t.Errorf("单写入器常驻仅 %.1f MiB，测量可能失效（预期约 2–3 MiB）", small)
	}
	if testing.Verbose() {
		fmt.Printf("单写入器常驻：lgwin=%d → %.2f MiB，lgwin=22 → %.2f MiB\n", brotliLGWin, small, def)
	}
}

// 静态资源压缩必须串行：并发的压缩峰值会叠加（实测三个大 chunk 并发把
// RSS 从 19 MB 推到 121 MB），而静态资源压缩结果永久缓存，串行的代价只是
// 首次加载多等一会儿。
//
// 用相对时序判定：串行时 N 个任务的总耗时接近 N 倍单次，并发时接近 1 倍。
// 阈值取 2 倍以容忍调度噪声；单核机器上并行也接近串行，属于可接受的假阴性。
func TestStaticAssetCompressionIsSerialized(t *testing.T) {
	// 不并行：这条靠相对时序判断串行化，必须独占运行。
	sample := sampleFragment(256<<10, 23)
	start := time.Now()
	if _, err := compressBytes(sample, EncBrotli); err != nil {
		t.Fatal(err)
	}
	single := time.Since(start)

	const workers = 4
	var wg sync.WaitGroup
	start = time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out bytes.Buffer
			if _, err := compressBytes(sample, EncBrotli); err != nil {
				t.Errorf("并发压缩失败：%v", err)
			}
			_ = out
		}()
	}
	wg.Wait()
	total := time.Since(start)

	if testing.Verbose() {
		fmt.Printf("单次 %v，%d 个并发总耗时 %v（比值 %.2f）\n",
			single, workers, total, float64(total)/float64(single))
	}
	if total < single*2 {
		t.Errorf("%d 个并发压缩总耗时 %v 仅为单次 %v 的 %.2f 倍，未串行化；"+
			"并发时压缩峰值会叠加，需要恢复 staticCompressGate 的容量 1",
			workers, total, single, float64(total)/float64(single))
	}
	if cap(staticCompressGate) != 1 {
		t.Errorf("staticCompressGate 容量为 %d，串行化设计应为 1", cap(staticCompressGate))
	}
}

// 静态资源按大小选压缩档位：小资源（首屏入口 58 KB）走最高压缩比，
// 大 chunk（≥256 KiB）走低档。
//
// 为什么这样分，见 staticQuality 的注释（HQ 压 1.4 MB chunk 的堆峰值实测
// 38 MiB，quality 7 只有 18 MiB，代价是输出 +13%）。这里只锁住**选择规则**
// ——档位本身由那个纯函数决定，改错立刻红，不需要在测试里重复测内存峰值
// （那是机器相关的测量，既慢又不稳）。
func Test静态资源按大小选压缩档位(t *testing.T) {
	cases := []struct {
		name string
		size int
		want int
	}{
		{"首屏入口 58 KB → 最高压缩比", 58 << 10, brotli.BestCompression},
		{"分界点下沿 255 KB → 最高压缩比", largeAssetBytes - 1, brotli.BestCompression},
		{"分界点 256 KB → 低档", largeAssetBytes, brotliLowQuality},
		{"按需加载的 1 MB chunk → 低档", 1 << 20, brotliLowQuality},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := staticQuality(c.size); got != c.want {
				t.Errorf("staticQuality(%d) = %d，应为 %d", c.size, got, c.want)
			}
		})
	}
}
