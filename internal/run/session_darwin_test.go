//go:build darwin

package run

import "syscall"

func supervisorSessionID(pid int) (int, error) {
	return syscall.Getsid(pid)
}
