package workspace

import "os/exec"

// execCommand 仅为测试提供统一入口。
func execCommand(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }
