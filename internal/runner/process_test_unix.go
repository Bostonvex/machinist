//go:build darwin || linux

package runner

import "syscall"

func escapeProcessGroup() error {
	_, err := syscall.Setsid()
	return err
}
