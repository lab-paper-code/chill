//go:build linux

package jetson

import "syscall"

// restartNode restarts the Node this process runs on. It returns only when the
// restart did not happen, so a caller may treat a return as failure.
func restartNode() error {
	syscall.Sync()
	return syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
}
