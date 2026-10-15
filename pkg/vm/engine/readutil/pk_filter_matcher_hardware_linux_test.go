//go:build linux && amd64

package readutil

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// perf_event_attr VER0, 64 bytes. No foreign PID, inheritance, sampling, mmap,
// counter reset, sysctl or capability change is supported.
type matcherPerfAttr struct {
	typ, size                                     uint32
	config, period, sampleType, readFormat, flags uint64
	wakeup, bpType                                uint32
	config1                                       uint64
}

const matcherPerfEnable = 0x2400
const matcherPerfDisable = 0x2401
const matcherPerfID = 0x80082407

func matcherPerfIOCTL(fd int, request uintptr, arg uintptr) error {
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, arg)
	if err != 0 {
		return err
	}
	return nil
}
func matcherBenchHardwareBegin() func() map[string]any {
	if os.Getenv("MATCHER_BENCH_HARDWARE") != "true" {
		return nil
	}
	b := matcherPerfBackend{lock: runtime.LockOSThread, unlock: runtime.UnlockOSThread, tid: syscall.Gettid, close: syscall.Close}
	b.open = func(config uint64, group int) (int, error) {
		attr := matcherPerfAttr{typ: 0, size: uint32(unsafe.Sizeof(matcherPerfAttr{})), config: config, readFormat: 15, flags: 96}
		if group < 0 {
			attr.flags |= 1
		}
		fd, _, err := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&attr)), 0, ^uintptr(0), uintptr(group), 8, 0)
		runtime.KeepAlive(&attr)
		if err != 0 {
			return -1, err
		}
		return int(fd), nil
	}
	b.id = func(fd int) (uint64, error) {
		var id uint64
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), matcherPerfID, uintptr(unsafe.Pointer(&id)))
		runtime.KeepAlive(&id)
		if errno != 0 {
			return 0, errno
		}
		return id, nil
	}
	b.enable = func(fd int) error { return matcherPerfIOCTL(fd, matcherPerfEnable, 1) }
	b.disable = func(fd int) error { return matcherPerfIOCTL(fd, matcherPerfDisable, 1) }
	b.read = func(fd int) (matcherPerfSnapshot, error) {
		data := make([]byte, (3+2*len(matcherPerfEvents))*8)
		n, err := syscall.Read(fd, data)
		if err != nil {
			return matcherPerfSnapshot{}, err
		}
		if n != len(data) {
			return matcherPerfSnapshot{}, fmt.Errorf("hardware read incomplete")
		}
		return decodeMatcherPerfRead(data)
	}
	return startMatcherPerfWindow(b)
}
