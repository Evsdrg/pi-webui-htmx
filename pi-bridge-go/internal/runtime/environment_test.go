package runtime

import (
	"context"
	"testing"
)

func Test工作进程不继承桥凭据(t *testing.T) {
	t.Setenv("PI_BRIDGE_TOKEN", "fixture-secret")
	t.Setenv("PI_RELAY_KEY", "fixture-secret")
	t.Setenv("TEST_PI_API_KEY", "business-key")
	manager, cwd := newTestManager(t, func(cfg *Config) {
		cfg.Env = append(cfg.Env, "PI_BRIDGE_DEVICE_TOKEN=fixture-secret", "FAKE_PI_FORBID_ENV=PI_BRIDGE_TOKEN,PI_BRIDGE_DEVICE_TOKEN,PI_RELAY_KEY", "FAKE_PI_REQUIRE_ENV=TEST_PI_API_KEY")
	})
	if _, err := manager.Start(context.Background(), "", cwd); err != nil {
		t.Fatalf("子进程环境断言失败：%v", err)
	}
}
