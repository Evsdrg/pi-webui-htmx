package presentation

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/andybalholm/brotli"
)

// newUI 构造一个最小可加载的 UI 包，供压缩与资源测试使用。
func newUI(t *testing.T, assets map[string]string) *Renderer {
	t.Helper()
	dir := t.TempDir()
	must := func(rel, body string) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("src/templates/shell.html", `<html><body>{{.SessionID}}</body></html>`)
	must("ui-manifest.json", `{"protocolVersion":1,"requiredMethods":[],"templates":{"shell":"templates/shell.html"},"build":{"entry":"src/entry/app.ts"}}`)
	must("dist/.vite/manifest.json", `{"src/entry/app.ts":{"file":"assets/app-abc123.js","isEntry":true,"css":[]}}`)
	for name, body := range assets {
		must("dist/assets/"+name, body)
	}
	r, err := LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPickEncoding按客户端能力选择(t *testing.T) {
	cases := map[string]string{
		"":                     "",
		"gzip":                 "gzip",
		"gzip, deflate, br":    "br",
		"br;q=1.0, gzip;q=0.8": "br",
		"identity":             "",
		"deflate, br;q=0.5":    "br",
		"*":                    "",
		"GZIP":                 "gzip",
		"x-gzip, br":           "br",
		"gzip;q=0":             "gzip", // 不解析 qvalue：浏览器默认头部不需要
	}
	for accept, want := range cases {
		if got := PickEncoding(accept); got != want {
			t.Errorf("PickEncoding(%q) = %q, want %q", accept, got, want)
		}
	}
}

func Test静态资源双算法压缩且可缓存(t *testing.T) {
	r := newUI(t, map[string]string{
		"app-abc123.js":  strings.Repeat("const value = 12345;//\n", 400),
		"app-abc123.css": strings.Repeat(".a{color:#fff}\n", 400),
	})
	for _, name := range []string{"app-abc123.js", "app-abc123.css"} {
		raw, _, ok := r.Asset(name, "")
		if !ok {
			t.Fatalf("%s 原文缺失", name)
		}
		for _, encoding := range []string{"gzip", "br"} {
			body, _, ok := r.Asset(name, encoding)
			if !ok {
				t.Fatalf("%s/%s 压缩结果缺失", name, encoding)
			}
			if len(body) >= len(raw) {
				t.Fatalf("%s/%s 未压缩: %d >= %d", name, encoding, len(body), len(raw))
			}
			again, _, _ := r.Asset(name, encoding)
			if !bytes.Equal(body, again) {
				t.Fatalf("%s/%s 缓存结果不稳定", name, encoding)
			}
		}
	}
	// 解压回来必须与原文一致。
	raw, _, _ := r.Asset("app-abc123.js", "")
	body, _, _ := r.Asset("app-abc123.js", "gzip")
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("gzip 解压结果与原文不一致")
	}
	body, _, _ = r.Asset("app-abc123.js", "br")
	br := brotli.NewReader(bytes.NewReader(body))
	got, err = io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("brotli 解压结果与原文不一致")
	}
}

func TestAsset拒绝路径穿越且不因编码放宽(t *testing.T) {
	r := newUI(t, map[string]string{"app-abc123.js": "console.log(1)"})
	for _, name := range []string{"../ui-manifest.json", "a/b.js", "..", "", "sub/../app-abc123.js"} {
		if _, _, ok := r.Asset(name, "gzip"); ok {
			t.Errorf("Asset(%q) 应被拒绝", name)
		}
	}
}

func Test压缩缓存有界(t *testing.T) {
	assets := map[string]string{"app-abc123.js": "console.log(1)"}
	for i := 0; i < maxCompressedEntries+8; i++ {
		assets[assetName(i)] = strings.Repeat("x", 2048)
	}
	r := newUI(t, assets)
	for i := 0; i < maxCompressedEntries+8; i++ {
		if _, _, ok := r.Asset(assetName(i), "gzip"); !ok {
			t.Fatalf("资源 %d 读取失败", i)
		}
	}
	stats := r.CompressedStats()
	if entries := stats["entries"].(int); entries > maxCompressedEntries {
		t.Fatalf("缓存条目 %d 超过上限 %d", entries, maxCompressedEntries)
	}
	if size := stats["bytes"].(int); size > maxCompressedBytes {
		t.Fatalf("缓存字节 %d 超过上限 %d", size, maxCompressedBytes)
	}
}

func Test动态响应按阈值决定是否压缩(t *testing.T) {
	small := []byte(strings.Repeat("a", compressThreshold-1))
	large := []byte(strings.Repeat("a", compressThreshold+1))
	if ShouldCompress(small, "gzip") {
		t.Error("小于阈值的响应不应压缩")
	}
	if !ShouldCompress(large, "gzip") {
		t.Error("大于阈值的响应应压缩")
	}
	if ShouldCompress(large, "") {
		t.Error("未协商编码时不应压缩")
	}
	// 小响应原样写出，不设 Content-Encoding 也能正确解码。
	var out bytes.Buffer
	n, err := Compress(&out, small, "gzip")
	if err != nil || n != len(small) || !bytes.Equal(out.Bytes(), small) {
		t.Fatalf("小响应未原样写出: n=%d err=%v", n, err)
	}
	// 大响应写出的是压缩流。
	out.Reset()
	if _, err := Compress(&out, large, "gzip"); err != nil {
		t.Fatal(err)
	}
	if out.Len() >= len(large) {
		t.Fatalf("大响应未压缩: %d >= %d", out.Len(), len(large))
	}
	zr, err := gzip.NewReader(&out)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(got, large) {
		t.Fatal("压缩流解压结果与原文不一致")
	}
}

// TestCompress按协商编码选压缩器 是回归测试：曾把 gzip 流标成 br 发出，
// 浏览器侧全部解不开，而单测只覆盖了 gzip 所以没抓到。
func TestCompress按协商编码选压缩器(t *testing.T) {
	payload := []byte(strings.Repeat("<div>会话历史</div>", 400))
	for _, encoding := range []string{"gzip", "br"} {
		var out bytes.Buffer
		if _, err := Compress(&out, payload, encoding); err != nil {
			t.Fatal(err)
		}
		if out.Len() >= len(payload) {
			t.Fatalf("%s 未压缩: %d >= %d", encoding, out.Len(), len(payload))
		}
		var got []byte
		switch encoding {
		case "gzip":
			zr, err := gzip.NewReader(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("%s 流无法解析: %v", encoding, err)
			}
			got, err = io.ReadAll(zr)
			if err != nil {
				t.Fatalf("%s 解压失败: %v", encoding, err)
			}
		case "br":
			got, _ = io.ReadAll(brotli.NewReader(bytes.NewReader(out.Bytes())))
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("%s 解压结果与原文不一致", encoding)
		}
		// 用另一种编码解析必须失败，否则说明选错了压缩器。
		if encoding == "br" {
			if _, err := gzip.NewReader(bytes.NewReader(out.Bytes())); err == nil {
				t.Fatal("br 流被 gzip 解析成功，说明压缩器选择错误")
			}
		}
	}
}

func TestCompress并发安全且复用写入器(t *testing.T) {
	payload := []byte(strings.Repeat("payload-", 4096))
	for _, encoding := range []string{"gzip", "br"} {
		var wg sync.WaitGroup
		results := make([]int, 16)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				var out bytes.Buffer
				if _, err := Compress(&out, payload, encoding); err != nil {
					t.Error(err)
					return
				}
				results[index] = out.Len()
			}(i)
		}
		wg.Wait()
		for i, size := range results {
			if size <= 0 {
				t.Fatalf("%s 第 %d 个并发压缩结果为空", encoding, i)
			}
			if size != results[0] {
				t.Fatalf("%s 第 %d 个并发压缩结果大小不一致: %d != %d", encoding, i, size, results[0])
			}
		}
	}
}

func assetName(i int) string {
	const hex = "0123456789abcdef"
	return "chunk-" + string(hex[i%16]) + string(hex[(i/16)%16]) + "-" + strconv.Itoa(i) + ".js"
}
