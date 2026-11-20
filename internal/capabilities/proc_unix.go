//go:build !windows

package capabilities

import "os/exec"

func setSysProcAttrHide(_ *exec.Cmd) {}
