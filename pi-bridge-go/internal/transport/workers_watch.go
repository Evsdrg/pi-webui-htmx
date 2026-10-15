package transport

import (
	"time"

	"pi-bridge-go/internal/protocol"
	run "pi-bridge-go/internal/runtime"
)

// workersWatchInterval 是忙闲快照的比对间隔。
//
// 侧栏的「运行中」转圈要在别的会话、别的浏览器里也看得见，而 worker 状态
// 只存在于桥的内存里，不会随会话索引落盘。做法不是让每个浏览器各自轮询
// （N 个标签页 = N 倍请求，且各page 抖动不同步），而是桥周期性比对一次
// 忙闲集合，**只在变化时**向所有已连接浏览器广播一个小控制帧。
// 2 秒是「够及时」与「几乎没有代价」之间的折中：一次比对只是读几个布尔量。
const workersWatchInterval = 2 * time.Second

// busySessions 从工作进程快照里取出当前忙碌的会话 ID 集合。
// 只取 busy 为真的：空闲但存活的工作进程不该在侧栏转圈（它会闲置到被回收）。
func busySessions(infos []run.Info) map[string]bool {
	out := make(map[string]bool, len(infos))
	for _, info := range infos {
		if info.Busy {
			out[info.SessionID] = true
		}
	}
	return out
}

// diffBusy 比较两份忙闲快照。相同返回 false，调用方据此跳过广播。
func diffBusy(prev, next map[string]bool) bool {
	if len(prev) != len(next) {
		return true
	}
	for id := range next {
		if !prev[id] {
			return true
		}
	}
	return false
}

// watchWorkers 周期比对忙闲集合，变化时广播 bridge.workers_changed。
// 随 manager.Context() 退出（桥关闭时一并停下）。
func (s *Server) watchWorkers() {
	ticker := time.NewTicker(workersWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.manager.Context().Done():
			return
		case <-ticker.C:
			next := busySessions(s.manager.List())
			s.workersMu.Lock()
			changed := diffBusy(s.lastBusy, next)
			if changed {
				s.lastBusy = next
			}
			s.workersMu.Unlock()
			if !changed {
				continue
			}
			s.broadcastWorkers(next)
		}
	}
}

// broadcastWorkers 向所有已接入的 WebSocket 连接推送忙会话快照。
//
// 用 list 而不是 set：前端按它直接增删行上的 data-running，顺序无关但数组
// 是更自然的线上形状，也与其它事件的数组字段一致。用 trySend 而不是 send：
// 这是可重放的冗余状态，队列满就丢弃（下次变化补上），绝不因为它断开连接。
func (s *Server) broadcastWorkers(running map[string]bool) {
	ids := make([]string, 0, len(running))
	for id := range running {
		ids = append(ids, id)
	}
	s.connMu.Lock()
	conns := make([]*connection, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.connMu.Unlock()
	message := protocol.Message{Version: 1, Kind: "control", Event: "bridge.workers_changed", Data: map[string]any{"running": ids}}
	for _, c := range conns {
		c.trySend(message)
	}
}

// trackConn 登记一条 WebSocket 连接；连接关闭时用 dropConn 移除。
func (s *Server) trackConn(c *connection) {
	s.connMu.Lock()
	s.conns[c] = struct{}{}
	s.connMu.Unlock()
}

func (s *Server) dropConn(c *connection) {
	s.connMu.Lock()
	delete(s.conns, c)
	s.connMu.Unlock()
}
