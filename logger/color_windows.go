//go:build windows

package logger

import (
	"os"
	"syscall"
	"unsafe"
)

func enableWindowsVT() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setConsoleMode := kernel32.NewProc("SetConsoleMode")
	getConsoleMode := kernel32.NewProc("GetConsoleMode")

	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		handle := f.Fd()
		var mode uint32
		r, _, _ := getConsoleMode.Call(handle, uintptr(unsafe.Pointer(&mode)))
		if r == 0 {
			continue
		}
		// ENABLE_VIRTUAL_TERMINAL_PROCESSING = 0x0004
		mode |= 0x0004
		setConsoleMode.Call(handle, uintptr(mode))
	}
}
