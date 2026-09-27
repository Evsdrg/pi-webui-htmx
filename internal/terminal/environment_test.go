package terminal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func Test终端只继承业务环境(t *testing.T) {
	t.Setenv("PI_BRIDGE_TOKEN", "fixture-secret")
	t.Setenv("PI_RELAY_KEY", "fixture-secret")
	t.Setenv("TEST_PI_API_KEY", "business-key")
	m := NewManager(Defaults())
	defer m.Close()
	term, err := m.Open(t.TempDir(), "/bin/sh", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := term.Subscribe(64, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := term.Write([]byte("printf 'RESULT:%s:%s:%s\\n' \"${PI_BRIDGE_TOKEN+x}\" \"${PI_RELAY_KEY+x}\" \"${TEST_PI_API_KEY+x}\"; exit\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out strings.Builder
	for {
		chunk, err := sub.Next(ctx)
		if err != nil {
			break
		}
		out.Write(chunk)
		if strings.Contains(out.String(), "RESULT:::x") {
			return
		}
	}
	t.Fatal("终端未移除服务凭据或丢失业务环境")
}
