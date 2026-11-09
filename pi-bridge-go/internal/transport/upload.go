package transport

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"pi-bridge-go/internal/presentation"
	"pi-bridge-go/internal/protocol"
)

// 上传端点是桥里**唯一**会写入用户文件树的入口。
//
// 为什么单独一个文件而不是塞进 serveUI：这条路径的失效模式与所有只读
// 端点都不同——写错一个路径不是「看到别的文件」，而是覆盖用户的源码。
// 因此它带着三层额外的守卫（目标不得已存在、受保护目录、体积上限），
// 值得有自己的位置与测试。
//
// 为什么走 HTTP 而不是 WS：multipart 天然是流式的，且不受 WS 单帧预算
// 约束；WS 那边 97 MiB 的读限是给图片附件的，拿来传任意文件会把背压的
// 语义搅乱（每片都要过 claims 指纹与 intent 落盘）。
const (
	// uploadField 是 multipart 里承载文件内容的字段名。
	uploadField = "file"
	// uploadPathField 是可选的显式目标路径（绝对路径）。
	uploadPathField = "path"
	// uploadDirQuery 是没有 path 字段时的目标目录（必须已在授权根内）。
	uploadDirQuery = "dir"
	// uploadBaseField 是固定目标模式的基路径（必须是启动方批准的白名单项）。
	uploadBaseField = "base"
	// uploadSessionField 是固定目标模式要拼进路径的会话 id。
	uploadSessionField = "sessionId"
	// uploadSessionQuery 是 sessionId 的查询参数形式（与表单字段同义）。
	uploadSessionQuery = "sessionId"
	// uploadMaxHeaderBytes 限制 multipart 头部的体积，避免用超长文件名
	// 或巨量字段做内存放大。文件本体另由 workspace 的体积上限约束。
	uploadMaxHeaderBytes = 64 << 10
)

// serveUpload 处理 POST /ui/files/upload（multipart/form-data）。
//
// 目标路径的两种给法：
//   - 表单字段 path：完整目标路径，必须落在授权根内且父目录已存在。
//   - 查询参数 dir + 表单里的文件名：落到 dir/<清洗后的文件名>。
//
// 不提供「自动建目录」：上传是用户从浏览器扔一个文件过来，静默创建
// 目录树会让误传的位置更难发现。
func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request, enc presentation.Encoding) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		writeError(w, enc, http.StatusUnsupportedMediaType, protocol.E("invalid_request", "上传必须是 multipart/form-data"))
		return
	}
	boundary := params["boundary"]
	if boundary == "" {
		writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_params", "multipart 缺少 boundary"))
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_params", "multipart 解析失败"))
		return
	}

	var (
		targetPath    string
		targetDir     string
		targetBase    string
		targetSession string
		fileName      string
		uploaded      bool
		written       int64
	)
	// 文件内容先缓冲再落盘，而不是边读边写。
	//
	// 原因是 multipart 的 part 顺序由客户端决定：path 字段完全可能排在
	// 文件之后。若边读边写，遇到「文件在前」的表单就只能要么报错、要么
	// 把内容丢掉——两者都是把可用的请求判成失败。缓冲的代价可控：上限
	// 与读取上限同源（默认 4 MiB），而 workspace.Upload 内部本来也要
	// 整体读入才能保证「超限时不留半截文件」。
	var (
		buf bytes.Buffer
	)
	for {
		part, perr := reader.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(perr, &tooLarge) {
				writeError(w, enc, http.StatusRequestEntityTooLarge, protocol.E("limit_exceeded", "上传超过体积上限"))
				return
			}
			writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_params", "multipart 读取失败"))
			return
		}
		switch part.FormName() {
		case uploadPathField:
			b, rerr := io.ReadAll(io.LimitReader(part, uploadMaxHeaderBytes))
			_ = part.Close()
			if rerr != nil {
				writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_params", "读取 path 字段失败"))
				return
			}
			targetPath = strings.TrimSpace(string(b))
		case uploadDirQuery:
			// dir 同时接受表单字段与查询参数。两种写法都在真实调用里出现过
			// （curl -F 与 URL query），只认一种会让另一种静默失败。
			b, rerr := io.ReadAll(io.LimitReader(part, uploadMaxHeaderBytes))
			_ = part.Close()
			if rerr != nil {
				writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_params", "读取 dir 字段失败"))
				return
			}
			targetDir = strings.TrimSpace(string(b))
		case uploadBaseField:
			b, rerr := io.ReadAll(io.LimitReader(part, uploadMaxHeaderBytes))
			_ = part.Close()
			if rerr != nil {
				writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_params", "读取 base 字段失败"))
				return
			}
			targetBase = strings.TrimSpace(string(b))
		case uploadSessionField:
			b, rerr := io.ReadAll(io.LimitReader(part, uploadMaxHeaderBytes))
			_ = part.Close()
			if rerr != nil {
				writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_params", "读取 sessionId 字段失败"))
				return
			}
			targetSession = strings.TrimSpace(string(b))
		case uploadField:
			if uploaded {
				_ = part.Close()
				writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_params", "一次只能上传一个文件"))
				return
			}
			fileName = part.FileName()
			// 多读一个字节：内容超过上限时 workspace.Upload 会据此拒绝。
			_, rerr := io.Copy(&buf, io.LimitReader(part, s.files.MaxReadBytes()+1))
			_ = part.Close()
			if rerr != nil {
				writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_request", "读取上传内容失败"))
				return
			}
			uploaded = true
		default:
			_ = part.Close()
		}
	}
	if !uploaded {
		writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_params", "请求里没有文件内容"))
		return
	}
	// 三种目标模式，优先级从高到低：
	//   1) base + sessionId  → 固定目标 <base>/piwebui/<会话id>/<文件名>
	//   2) path              → 明确指定的完整路径（仍须在工作区根内）
	//   3) dir + 文件名       → dir/<文件名>
	// 模式 1 会写到工作区根之外，因此 base 必须是启动方批准的白名单项。
	if targetBase != "" {
		sessionID := targetSession
		if sessionID == "" {
			sessionID = strings.TrimSpace(r.URL.Query().Get(uploadSessionQuery))
		}
		name := fileName
		if targetPath != "" {
			name = filepath.Base(targetPath)
		}
		saved, serr := s.files.UploadScratchNamed(bytes.NewReader(buf.Bytes()), targetBase, sessionID, name)
		if serr != nil {
			writeUploadError(w, enc, serr)
			return
		}
		writeJSON(w, enc, http.StatusOK, map[string]any{"path": saved, "size": len(buf.Bytes())})
		return
	}
	if targetPath == "" {
		base, ok := sanitizeUploadName(fileName)
		if !ok {
			writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_params", "文件名不合法"))
			return
		}
		dir := targetDir
		if dir == "" {
			dir = strings.TrimSpace(r.URL.Query().Get(uploadDirQuery))
		}
		if dir == "" {
			writeError(w, enc, http.StatusBadRequest, protocol.E("invalid_params", "缺少 path 字段或 dir 参数"))
			return
		}
		targetPath = filepath.Join(dir, base)
	}
	written, err = s.files.Upload(bytes.NewReader(buf.Bytes()), targetPath, 0o644)
	if err != nil {
		writeUploadError(w, enc, err)
		return
	}
	writeJSON(w, enc, http.StatusOK, map[string]any{"path": targetPath, "size": written})
}

// writeUploadError 把 workspace 的错误映射成合适的状态码。
// 上传的失败原因对用户有区别：越界是「你选错了地方」，已存在是「换个名字」，
// 超限是「文件太大」——三者给的下一步动作不同，不能统一成 400。
func writeUploadError(w http.ResponseWriter, enc presentation.Encoding, err error) {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case "forbidden":
			writeError(w, enc, http.StatusForbidden, err)
			return
		case "limit_exceeded":
			writeError(w, enc, http.StatusRequestEntityTooLarge, err)
			return
		case "conflict":
			writeError(w, enc, http.StatusConflict, err)
			return
		case "not_found":
			writeError(w, enc, http.StatusNotFound, err)
			return
		}
	}
	writeError(w, enc, http.StatusBadRequest, err)
}

// sanitizeUploadName 清洗客户端给的文件名。
//
// 浏览器在 multipart 里可以塞任意 FileName（包括 ../../evil.txt），
// 因此只取最后一段并拒绝 . 与 ..；清洗失败就明确报错，不静默改名——
// 静默改名会让用户以为传到了 A，实际在 B。
func sanitizeUploadName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	// 有的浏览器会带完整 Windows 路径。
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." {
		return "", false
	}
	if strings.ContainsRune(name, 0) {
		return "", false
	}
	// 基名必须与自身一致：这一步挡掉任何残留的路径成分。
	if name != filepath.Base(name) {
		return "", false
	}
	return name, true
}
