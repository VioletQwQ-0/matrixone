//go:build !linux || !amd64

package readutil

import "os"

func matcherBenchHardwareBegin() func() map[string]any {
	if os.Getenv("MATCHER_BENCH_HARDWARE") != "true" {
		return nil
	}
	return func() map[string]any { return matcherPerfUnavailable("PLATFORM_NOT_LINUX_AMD64") }
}
