package transport

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"pi-bridge-go/internal/management"
	"pi-bridge-go/internal/presentation"
	"pi-bridge-go/internal/protocol"
)

// catalogTTL 是模型目录的进程内缓存时长。
// 目录来自 models.dev，一天内的变化对我们「按型号补全参数」这个用途
// 没有意义；但每次按键都打一次公网请求更不合适，所以取中间值。
const catalogTTL = 10 * time.Minute

// catalogMatches 是单次搜索返回的候选上限。
// 目录有 2000 条，一次全渲染约 240 KB HTML——按需搜索既省带宽，
// 也让「输入即过滤」这条交互不需要把整份目录塞进页面（B70）。
const catalogMatches = 50

// catalogCache 缓存一份目录快照。
// 目录是只读的公网数据，缓存安全；失败不写缓存，下一次请求会重试。
type catalogCache struct {
	mu    sync.Mutex
	items []management.DiscoveredModel
	at    time.Time
}

// itemsFor 返回目录条目：命中缓存直接返回，否则拉取并写入缓存。
func (s *Server) itemsFor(ctx context.Context) ([]management.DiscoveredModel, error) {
	cache := &s.catalog
	cache.mu.Lock()
	if len(cache.items) > 0 && time.Since(cache.at) < catalogTTL {
		items := cache.items
		cache.mu.Unlock()
		return items, nil
	}
	cache.mu.Unlock()

	// 拉取时不持锁：网络请求可能几十秒，持锁会把所有目录请求串起来。
	items, err := s.piConfig.Catalog(ctx, s.discovery)
	if err != nil {
		return nil, err
	}
	cache.mu.Lock()
	cache.items, cache.at = items, time.Now()
	cache.mu.Unlock()
	return items, nil
}

// serveModelCatalog 返回模型目录片段（按查询过滤）。
// 与 discover/test 一样只接受已认证的 POST，参数不进 URL。
func (s *Server) serveModelCatalog(w http.ResponseWriter, r *http.Request, enc presentation.Encoding) {
	if s.ui == nil {
		writeError(w, enc, 404, protocol.E("not_found", "未配置 UI 包"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
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
		items, err := s.itemsFor(r.Context())
		if err != nil {
			return "", err
		}
		query := strings.TrimSpace(r.PostForm.Get("q"))
		matched := matchCatalog(items, query)
		return s.ui.RenderCatalog(matched, len(items), query)
	})
}

// matchCatalog 按 ID 与显示名做不区分大小写的子串匹配。
// 空查询返回前 catalogMatches 条：让面板在没输入时也有内容可点。
func matchCatalog(items []management.DiscoveredModel, query string) []management.DiscoveredModel {
	needle := strings.ToLower(query)
	out := make([]management.DiscoveredModel, 0, catalogMatches)
	for _, item := range items {
		if needle != "" &&
			!strings.Contains(strings.ToLower(item.ID), needle) &&
			!strings.Contains(strings.ToLower(item.Name), needle) {
			continue
		}
		out = append(out, item)
		if len(out) >= catalogMatches {
			break
		}
	}
	return out
}
