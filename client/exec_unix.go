//go:build linux || darwin

package main

import (
	"os/exec"
	"syscall"
)

// detach 让子进程脱离本程序的会话，客户端窗口关掉也不会把被测服务带走
func detach(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.Setsid = true
}
