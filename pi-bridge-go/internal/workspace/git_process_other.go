//go:build !linux

package workspace

import (
	"os/exec"
	"pi-bridge-go/internal/protocol"
)

func prepareGitCommand(*exec.Cmd) error {
	return protocol.E("pi_error", "受控 Git 进程监督仅支持 Linux")
}
