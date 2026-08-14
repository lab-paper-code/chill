//go:build !linux

package jetson

import "errors"

// restartNode is unavailable off Linux, where the actuator does not run.
func restartNode() error {
	return errors.New("restarting a Node is only implemented on Linux")
}
