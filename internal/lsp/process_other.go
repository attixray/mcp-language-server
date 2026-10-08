//go:build !windows

package lsp

import (
	"os/exec"
	"sync"
	"syscall"
)

type processTree struct {
	pid  int
	once sync.Once
}

func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func attachProcessTree(cmd *exec.Cmd) (*processTree, error) {
	return &processTree{pid: cmd.Process.Pid}, nil
}
func (p *processTree) kill()  { p.once.Do(func() { _ = syscall.Kill(-p.pid, syscall.SIGKILL) }) }
func (p *processTree) close() { p.kill() }
