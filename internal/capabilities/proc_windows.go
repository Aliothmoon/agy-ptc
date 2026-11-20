//go:build windows

package capabilities

import (
	"os/exec"
	"syscall"
)

func setSysProcAttrHide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
