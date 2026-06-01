//go:build unix || linux || darwin

package tools

import (
	"syscall"
)

func applySecurityFlags(flags int) int {
	return flags | syscall.O_NOFOLLOW
}
