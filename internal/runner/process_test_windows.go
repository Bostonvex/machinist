//go:build windows

package runner

import "errors"

func escapeProcessGroup() error {
	return errors.New("detached Unix process groups are unavailable on Windows")
}
