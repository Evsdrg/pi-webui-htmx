package childenv

import (
	"reflect"
	"testing"
)

func Test过滤服务环境并保留业务凭据(t *testing.T) {
	input := []string{"PI_BRIDGE_TOKEN=secret", "PI_BRIDGE_DEVICE_TOKEN=secret", "PI_RELAY_KEY=secret", "pi_bridge_token=secret", "API_KEY=api", "HTTPS_PROXY=proxy", "PATH=/usr/bin", "PI_CODING_AGENT_DIR=/agent"}
	want := []string{"API_KEY=api", "HTTPS_PROXY=proxy", "PATH=/usr/bin", "PI_CODING_AGENT_DIR=/agent"}
	before := append([]string(nil), input...)
	if got := Filter(input); !reflect.DeepEqual(got, want) {
		t.Fatal("环境过滤不正确")
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("过滤修改了调用方环境")
	}
}
