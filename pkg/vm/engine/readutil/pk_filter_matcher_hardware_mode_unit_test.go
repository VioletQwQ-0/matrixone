package readutil

import "testing"

func TestIssue29322MatcherHardwareDefaultOff(t *testing.T) {
	for _, value := range []string{"", "false", "1", "yes"} {
		t.Setenv("MATCHER_BENCH_HARDWARE", value)
		if matcherBenchHardwareBegin() != nil {
			t.Fatal("hardware observation is not default-off")
		}
	}
}
