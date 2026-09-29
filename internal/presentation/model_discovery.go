package presentation

import "pi-bridge-go/internal/management"

// RenderDiscoveredModels 排版供应商返回的数据，不把列表渲染推给浏览器。
func (r *Renderer) RenderDiscoveredModels(rows []management.DiscoveredModel) (string, error) {
	return r.execute("model-discovery.html", rows)
}
