//go:build linux && amd64

package readutil

import (
	"testing"
	"unsafe"
)

func TestIssue29322MatcherHardwareLinuxABI(t *testing.T) {
	if unsafe.Sizeof(matcherPerfAttr{}) != 64 || matcherPerfEnable != 0x2400 || matcherPerfDisable != 0x2401 || matcherPerfID != 0x80082407 {
		t.Fatal("Linux VER0/group ioctl ABI differs")
	}
}
