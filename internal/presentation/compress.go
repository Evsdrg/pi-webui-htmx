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
		w := brotli.NewWriterLevel(&out, brotli.BestCompression)
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

// compressThreshold 以下的响应不压缩：压缩收益抵不上一次分配的代价，
// 而小响应恰好是错误与确认回执的大多数。
const compressThreshold = 1024

// 两套写入器池，按协商到的编码选用。
// 动态 HTML/JSON 每个请求都要压缩，不复用会给 GC 添一笔稳定负担。
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
		return brotli.NewWriterLevel(io.Discard, 4)
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
