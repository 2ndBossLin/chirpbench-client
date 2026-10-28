//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detach 在 Windows 上让子进程独立运行，不随客户端窗口关闭而退出
func detach(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.CreationFlags |= 0x00000008 // CREATE_NEW_PROCESS_GROUP
}
