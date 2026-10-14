//go:build !unix

package runner

import (
	"os/exec"
	"time"
)

func configureCmd(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
}
