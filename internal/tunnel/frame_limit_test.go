package tunnel

import (
	"testing"

	"pi-bridge-go/internal/protocol"
)

// Test隧道读上限覆盖relay转发的最大帧 覆盖 B53：
// relay 只给浏览器帧包一层路由封装就转过来，所以桥这一侧的读上限
// 必须能装下「桥自己允许的最大浏览器帧」——写死 1 MiB 时，图片附件
// 一超限就会让桥主动断开与 relay 的连接。
func Test隧道读上限覆盖relay转发的最大帧(t *testing.T) {
	want := protocol.BrowserFrameLimit + protocol.RelayEnvelopeBytes
	if maxFrame < want {
		t.Fatalf("隧道读上限 %d 小于桥允许的最大帧加封装 %d", maxFrame, want)
	}
}
