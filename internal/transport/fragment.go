package transport

import (
	"errors"
	"html"
	"net/http"

	"pi-bridge-go/internal/protocol"
)

// fragmentIssue 用于「htmx 会交换的片段端点」的前置状态。
//
// 关键区别：fetch/导航类端点（/ui/file-text、/ui/file-image、/ui/exports/*、
// lazy 加载）必须保留真实状态码——调用方是 JS 的 response.ok 或浏览器导航。
// 而 htmx 默认不交换 4xx/5xx，按错误返回会让面板停在旧内容上、没有任何解释。
//
// 因此片段端点把状态当作内容渲染（200 + 一段说明），错误原因仍原样带出。
// 真正的程序性错误（库写入失败之类）仍走 500 JSON，那是故障不是状态。
func (s *Server) fragmentIssue(w http.ResponseWriter, encoding string, err error) {
	// 协议错误的 Error() 会带上机器码前缀（如 worker_not_running: …），
	// 那对排查有用、对读者是噪音。片段是给人看的，只取可读消息。
	message := err.Error()
	var protocolErr *protocol.Error
	if errors.As(err, &protocolErr) && protocolErr.Message != "" {
		message = protocolErr.Message
	}
	// UI 层可缺席（未配 --ui-dir）：正常路径上 /ui/* 已被更外层的守卫
	// 拦掉，这里再判一次是为了让「将来新增的片段调用点」不会踩到 panic——
	// 渲染器不再为 nil 接收者容错。
	if s.ui == nil {
		writeHTML(w, encoding, `<p class="empty-note">`+html.EscapeString(message)+`</p>`)
		return
	}
	if note, rerr := s.ui.RenderNote(message); rerr == nil {
		writeHTML(w, encoding, note)
		return
	}
	// UI 包不可用时至少给一句可读文本，不要退回 JSON。
	writeHTML(w, encoding, `<p class="empty-note">`+html.EscapeString(message)+`</p>`)
}
