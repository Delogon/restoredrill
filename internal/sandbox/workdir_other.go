//go:build !linux && !darwin

package sandbox

import "fmt"

func freeBytes(path string) (uint64, error) {
	return 0, fmt.Errorf("free-space detection is not supported on this platform")
}