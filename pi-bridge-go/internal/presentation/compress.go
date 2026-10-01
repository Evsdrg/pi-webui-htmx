package presentation

import (
	"bytes"
	"compress/gzip"
	"io"
	"sync"

	"github.com/andybalholm/brotli"
)

// compressBytes 一次性压缩整块内容，用于静态资源。
// 静态资源文件名带内容哈希，压缩结果可永久缓存，因此用最高压缩比。
func compressBytes(raw []byte, encoding Encoding) ([]byte, error) {
	// 静态资源压缩串行化。
	//
	// 为什么：BestCompression 压按需加载的大 chunk（实测 1.4 MB）时堆峰值约
	// 38 MiB，而浏览器会并发请求多个 chunk。实测三个并发把 RSS 推到 121 MB，
	// 串行后峰值就是确定的单次上限。压缩结果永久缓存，所以串行只让首次加载
	// 多等一会儿；后续访问直接命中缓存，不再压缩。
	//
	// 只挡静态资源：动态响应走小窗口与池（单份约 2.9 MiB），不能被它拖住。
	staticCompressGate <- struct{}{}
	defer func() { <-staticCompressGate }()

	var out bytes.Buffer
	switch encoding {
	case EncGzip:
		w, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(raw); err != nil {
			return nil, err
		}
		// 必须先 Close 再取字节：Close 才会写入压缩器尾部，
		// 而 Go 的返回实参在 return 语句里是从左到右求值的。
		if err := w.Close(); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	case EncBrotli:
		w := newBrotliWriter(&out, staticQuality(len(raw)))
		if _, err := w.Write(raw); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	default:
		return raw, nil
	}
}

// brotliLGWin 是桥里所有 brotli 写入器共用的窗口（log2，单位字节）。
//
// 必须显式指定，不能依赖库默认值：andybalholm/brotli 的 defaultWindow 是 22，
// 而环形缓冲按 1<<(lgwin+1) 分配，即 **8 MiB**——那是一个写入器的常驻内存，
// 不是峰值。它跟随 Writer 对象存活：Reset 会把 lgwin 重置回默认值 22，
// 所以只给“首次创建”设窗口没用，必须在 options 里定死。
//
// 为什么 19（窗口 512 KiB）：实测桥的响应与资源都在窗口之内，而内存是
// 1<<(19+1) = 1 MiB 环缓。用真实样本对照（227 KB 历史片段、38 KB 记忆面板、
// 33 KB 外壳）：lgwin=19 与 22 的压缩输出**逐字节相同**；按需加载的大 chunk
// （1.4 MB）只多 0.9%，而单次压缩峰值从 96 MiB 降到 38 MiB。
const brotliLGWin = 19

// staticCompressGate 是静态资源压缩的串行化信号量（容量 1）。
// 容量是可测试的设计决定：见 compressBytes 的注释与
// TestStaticAssetCompressionIsSerialized。
var staticCompressGate = make(chan struct{}, 1)

// largeAssetBytes 是「大资源」的分界（256 KiB）。
// 首屏入口文件（实测 58 KB JS / 53 KB CSS）在它以下，走最高压缩比；
// 按需加载的 chunk（终端 332 KB、图表 1.4 MB）在它以上，改用较低档位。
const largeAssetBytes = 256 << 10

// staticQuality 按资源大小选压缩档位。
//
// 为什么分档：brotli 的 HQ 模式（quality ≥ 10）压 1.4 MB 的 chunk 时堆峰值
// 实测 38 MiB，而 quality 7 只有 18 MiB——代价是输出 +13%（344 KB → 389 KB）。
// 这些 chunk 是**一次性下载 + 文件名带哈希的永久缓存**，多出的几十 KB 只在
// 冷缓存时付一次，而内存峰值在每次冷启动后首次加载时都会出现。
func staticQuality(size int) int {
	if size >= largeAssetBytes {
		return brotliLowQuality
	}
	return brotli.BestCompression
}

// brotliLowQuality 是大资源的压缩档位：在 HQ 与默认档之间取偏保守的一档。
const brotliLowQuality = 7

// newBrotliWriter 按统一窗口创建写入器：静态资源与动态响应共用同一个窗口，
// 两边都不会因为库默认的 8 MiB 环缓而把内存留在池里。
func newBrotliWriter(dst io.Writer, quality int) *brotli.Writer {
	return brotli.NewWriterOptions(dst, brotli.WriterOptions{Quality: quality, LGWin: brotliLGWin})
}

// compressThreshold 以下的响应不压缩：压缩收益抵不上一次分配的代价，
// 而小响应恰好是错误与确认回执的大多数。
const compressThreshold = 1024

// 两套写入器池，按协商到的编码选用。
// 动态 HTML/JSON 每个请求都要压缩，不复用会给 GC 添一笔稳定负担。
//
// 池会按“GC 间隔内的并发峰值”保留写入器（sync.Pool 在 GC 时清空），
// 所以单个写入器的常驻内存直接决定峰值 RSS：brotli 默认窗口会让一份就到
// 8 MiB（见 brotliLGWin），六个并发请求就是 48 MiB 常驻。gzip BestSpeed
// 的写入器只有 0.8 MiB，无需特别处理。
var (
	gzipPool = sync.Pool{New: func() any {
		w, err := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		if err != nil {
			return nil
		}
		return w
	}}
	brotliPool = sync.Pool{New: func() any {
		// 动态内容用较低档位：brotli 高档位的 CPU 成本在每请求场景不划算，
		// 而 gzip 兜底始终可用。
		return newBrotliWriter(io.Discard, 4)
	}}
)

// Compress 把内容写入 w，并按 encoding 压缩。
// 内容小于阈值或编码为空时原样写出；压缩失败也原样写出——
// 压缩是优化，不该让响应失败。
//
// 调用方必须把 Content-Encoding 设成同一个 encoding：这里按 encoding
// 选压缩器，写出去的字节能被该编码的客户端解开。
func Compress(w io.Writer, body []byte, encoding Encoding) (int, error) {
	if encoding == "" || len(body) < compressThreshold {
		return w.Write(body)
	}
	switch encoding {
	case EncGzip:
		pooled, _ := gzipPool.Get().(*gzip.Writer)
		if pooled == nil {
			return w.Write(body)
		}
		defer gzipPool.Put(pooled)
		pooled.Reset(w)
		if _, err := pooled.Write(body); err != nil {
			return w.Write(body)
		}
		if err := pooled.Close(); err != nil {
			return w.Write(body)
		}
		return len(body), nil
	case EncBrotli:
		pooled, _ := brotliPool.Get().(*brotli.Writer)
		if pooled == nil {
			return w.Write(body)
		}
		defer brotliPool.Put(pooled)
		pooled.Reset(w)
		if _, err := pooled.Write(body); err != nil {
			return w.Write(body)
		}
		if err := pooled.Close(); err != nil {
			return w.Write(body)
		}
		return len(body), nil
	default:
		return w.Write(body)
	}
}

// ShouldCompress 判断响应是否值得压缩。
func ShouldCompress(body []byte, encoding Encoding) bool {
	return encoding != "" && len(body) >= compressThreshold
}
