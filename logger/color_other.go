//go:build !windows

package logger

func enableWindowsVT() {
	// no-op on non-Windows
}
