package logger

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

// ANSI color codes — work on Linux SSH and Windows 10+ CMD/PowerShell
// For older Windows, we enable virtual terminal processing via init()
const (
	reset   = "\033[0m"
	bold    = "\033[1m"
	dim     = "\033[2m"
	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	blue    = "\033[34m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
	white   = "\033[37m"
	gray    = "\033[90m"
)

func init() {
	enableColor()
}

// Step logs a step transition: ▶ [email] step name
func Step(email, step string) {
	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(os.Stderr, "%s%s%s %s▶%s %s[%s]%s %s%s%s\n",
		gray, ts, reset,
		cyan, reset,
		white, shortEmail(email), reset,
		bold+green, step, reset)
}

// Info logs an info message: ℹ [email] message
func Info(email, msg string) {
	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(os.Stderr, "%s%s%s %s·%s %s[%s]%s %s\n",
		gray, ts, reset,
		gray, reset,
		gray, shortEmail(email), reset,
		msg)
}

// OK logs a success: ✓ [email] message
func OK(email, msg string) {
	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(os.Stderr, "%s%s%s %s✓%s %s[%s]%s %s%s%s\n",
		gray, ts, reset,
		green, reset,
		white, shortEmail(email), reset,
		green, msg, reset)
}

// Warn logs a warning: ⚠ [email] message
func Warn(email, msg string) {
	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(os.Stderr, "%s%s%s %s!%s %s[%s]%s %s%s%s\n",
		gray, ts, reset,
		yellow, reset,
		white, shortEmail(email), reset,
		yellow, msg, reset)
}

// Fail logs a failure: ✗ [email] message
func Fail(email, msg string) {
	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(os.Stderr, "%s%s%s %s✗%s %s[%s]%s %s%s%s\n",
		gray, ts, reset,
		red, reset,
		white, shortEmail(email), reset,
		red, msg, reset)
}

// Banner logs a batch-level banner: === message ===
func Banner(msg string) {
	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(os.Stderr, "%s%s%s %s━━━ %s%s%s %s━━━%s\n",
		gray, ts, reset,
		magenta, bold+white, msg, reset,
		magenta, reset)
}

// Sys logs system-level messages (non-account specific)
func Sys(msg string) {
	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(os.Stderr, "%s%s%s %s%s%s\n",
		gray, ts, reset,
		blue, msg, reset)
}

// shortEmail returns the part before @
func shortEmail(email string) string {
	for i, c := range email {
		if c == '@' {
			return email[:i]
		}
	}
	return email
}

// enableColor enables ANSI on Windows
func enableColor() {
	if runtime.GOOS != "windows" {
		return
	}
	enableWindowsVT()
}
