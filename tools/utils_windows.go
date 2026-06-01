//go:build windows

package tools

const (
	winFileFlagOpenReparsePoint = 0x00200000
)

func applySecurityFlags(flags int) int {
	return flags | winFileFlagOpenReparsePoint
}
