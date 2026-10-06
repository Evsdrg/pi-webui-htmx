package transport

import (
	"net/http"
	"strings"

	"pi-bridge-go/internal/presentation"
	"pi-bridge-go/internal/protocol"
)

// serveModelProbe 只接受已认证的 POST；秘密从不进入 URL 或响应。
func (s *Server) serveModelProbe(w http.ResponseWriter, r *http.Request, enc presentation.Encoding) {
	if s.ui == nil {
		writeError(w, enc, 404, protocol.E("not_found", "未配置 UI 包"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	s.renderFragment(w, enc, func() (string, error) {
		select {
		case s.operations <- struct{}{}:
			defer func() { <-s.operations }()
		default:
			return "", protocol.E("busy", "桥的在途操作已满，请稍后重试")
		}
		if err := r.ParseForm(); err != nil {
			return "", protocol.E("invalid_params", "查询参数过大或无效")
		}
		headers := map[string]string{}
		for _, line := range strings.Split(r.PostForm.Get("headers"), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(key) == "" {
				return "", protocol.E("invalid_params", "自定义头部格式应为名称: 值")
			}
			value = strings.TrimSpace(value)
			if value == "***" {
				return "", protocol.E("invalid_params", "测试请填写明确凭据，打码值不能作为真实凭据发送")
			}
			headers[strings.TrimSpace(key)] = value
		}
		key := r.PostForm.Get("apiKey")
		if key == "***" || strings.HasPrefix(strings.TrimSpace(key), "!") {
			return "", protocol.E("invalid_params", "测试请填写明确凭据；不执行命令型凭据")
		}
		base, api := r.PostForm.Get("baseUrl"), r.PostForm.Get("api")
		if strings.HasSuffix(r.URL.Path, "/test") {
			data, err := s.piConfig.TestConnection(r.Context(), base, api, key, headers, s.discovery)
			if err != nil {
				return "", err
			}
			return s.ui.RenderNote(probeNote(data))
		}
		models, err := s.piConfig.Discover(r.Context(), base, api, key, headers, s.discovery)
		if err != nil {
			return "", err
		}
		return s.ui.RenderDiscoveredModels(models)
	})
}

// probeNote 把测试连接的结果转成一句可读提示。
// TestConnection 的返回体只有 ok/modelsListed/firstModelId，没有 message；
// 原来固定读 message 会让成功时渲染出一个空的提示段落。
func probeNote(data map[string]any) string {
	if message, _ := data["message"].(string); message != "" {
		return message
	}
	if listed, _ := data["modelsListed"].(bool); listed {
		if id, _ := data["firstModelId"].(string); id != "" {
			return "连接成功，模型列表可读取，例如 " + id + "。"
		}
		return "连接成功，模型列表可读取。"
	}
	return "连接成功，但模型列表为空；请确认该供应商是否支持列出模型。"
}
