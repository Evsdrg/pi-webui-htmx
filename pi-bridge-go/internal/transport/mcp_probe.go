package transport

import (
	"net/http"
	"time"

	"pi-bridge-go/internal/management"
	"pi-bridge-go/internal/presentation"
	"pi-bridge-go/internal/protocol"
)

// serveMCPProbe 处理「探测 MCP 连接」：运行 `pi mcp list --json` 取回各服务器的
// 在线状态与工具数，再渲染带状态的清单。
//
// 这是网络类操作：它会真实连接每个启用的服务器（stdio 拉起子进程、http 发请求），
// 因此占全局操作槽、只接受已认证的 POST，并受探测超时约束。渲染前已对探测输出
// 做了已知秘密值的二次脱敏（见 management.ProbeMCP）。
func (s *Server) serveMCPProbe(w http.ResponseWriter, r *http.Request, enc presentation.Encoding) {
	if s.ui == nil {
		writeError(w, enc, 404, protocol.E("not_found", "未配置 UI 包"))
		return
	}
	// 探测可能跑满 20 秒，超过桥默认的 15 秒 WriteTimeout；把这条连接的写
	// 截止时间推到探测预算之上，否则处理器跑完了响应也写不出去（实测空正文）。
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(management.ProbeBudget() + 5*time.Second))
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	s.renderFragment(w, enc, func() (string, error) {
		select {
		case s.operations <- struct{}{}:
			defer func() { <-s.operations }()
		default:
			return "", protocol.E("busy", "桥的在途操作已满，请稍后重试")
		}
		reply, err := s.piConfig.MCP()
		if err != nil {
			return "", err
		}
		probe, err := s.piConfig.ProbeMCP(r.Context())
		if err != nil {
			return "", err
		}
		// 配置错误以 Pi 的探测结果为准（它可能包含项目级配置的说明），
		// 但保留桥自己解析出的错误，避免两者不一致时丢信息。
		errs := reply.Errors
		if len(probe.Errors) > 0 {
			errs = probe.Errors
		}
		note := reply.Note
		if probe.Note != "" {
			note = probe.Note
		}
		return s.ui.RenderMCP(presentation.MCPData{
			Servers: mcpRows(reply.Servers, &probe),
			Errors:  errs,
			Note:    note,
			Probed:  true,
		})
	})
}
