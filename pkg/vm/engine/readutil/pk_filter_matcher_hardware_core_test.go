package readutil

import (
	"encoding/binary"
	"fmt"
)

// Only the benchmark's locked OS thread is observed. Counts exclude kernel and
// hypervisor execution; they are not process-wide or physical read counters.
var matcherPerfEvents = []struct {
	name   string
	config uint64
}{
	{"cycles", 0}, {"instructions", 1}, {"reference_cycles", 9}, {"branch_misses", 5}, {"cache_misses", 3},
}

type matcherPerfSnapshot struct {
	enabled, running uint64
	values           map[uint64]uint64
}
type matcherPerfBackend struct {
	lock, unlock    func()
	tid             func() int
	open            func(uint64, int) (int, error)
	id              func(int) (uint64, error)
	enable, disable func(int) error
	read            func(int) (matcherPerfSnapshot, error)
	close           func(int) error
}

func matcherPerfUnavailable(reason string) map[string]any {
	return map[string]any{"status": "NOT_AVAILABLE", "scope": "BENCHMARK_OS_THREAD_USERSPACE",
		"reason": reason, "process_wide": false}
}

func decodeMatcherPerfRead(data []byte) (matcherPerfSnapshot, error) {
	var out matcherPerfSnapshot
	if len(data) != (3+2*len(matcherPerfEvents))*8 || binary.LittleEndian.Uint64(data) != uint64(len(matcherPerfEvents)) {
		return out, fmt.Errorf("hardware group read shape")
	}
	out.enabled = binary.LittleEndian.Uint64(data[8:])
	out.running = binary.LittleEndian.Uint64(data[16:])
	if out.running > out.enabled {
		return out, fmt.Errorf("hardware group time invalid")
	}
	out.values = make(map[uint64]uint64, len(matcherPerfEvents))
	for i := range matcherPerfEvents {
		value := binary.LittleEndian.Uint64(data[24+i*16:])
		id := binary.LittleEndian.Uint64(data[32+i*16:])
		if _, seen := out.values[id]; id == 0 || seen {
			return out, fmt.Errorf("hardware group id invalid")
		}
		out.values[id] = value
	}
	return out, nil
}

func startMatcherPerfWindow(b matcherPerfBackend) func() map[string]any {
	b.lock()
	tid := b.tid()
	fds := make([]int, 0, len(matcherPerfEvents))
	ids := make([]uint64, 0, len(matcherPerfEvents))
	closeAll := func() {
		for i := len(fds) - 1; i >= 0; i-- {
			_ = b.close(fds[i])
		}
	}
	fail := func(err error) func() map[string]any {
		closeAll()
		b.unlock()
		return func() map[string]any { return matcherPerfUnavailable(err.Error()) }
	}
	if tid <= 0 {
		return fail(fmt.Errorf("hardware thread id invalid"))
	}
	for _, event := range matcherPerfEvents {
		group := -1
		if len(fds) > 0 {
			group = fds[0]
		}
		fd, err := b.open(event.config, group)
		if err != nil {
			return fail(err)
		}
		if fd < 0 {
			return fail(fmt.Errorf("hardware descriptor invalid"))
		}
		fds = append(fds, fd)
		id, err := b.id(fd)
		if err != nil {
			return fail(err)
		}
		for _, old := range ids {
			if old == id {
				return fail(fmt.Errorf("hardware id duplicated"))
			}
		}
		if id == 0 {
			return fail(fmt.Errorf("hardware id missing"))
		}
		ids = append(ids, id)
	}
	before, err := b.read(fds[0])
	if err != nil {
		return fail(err)
	}
	if len(before.values) != len(ids) {
		return fail(fmt.Errorf("hardware baseline incomplete"))
	}
	for _, id := range ids {
		if _, ok := before.values[id]; !ok {
			return fail(fmt.Errorf("hardware baseline id differs"))
		}
	}
	if err := b.enable(fds[0]); err != nil {
		return fail(err)
	}
	finished := false
	return func() map[string]any {
		if finished {
			return matcherPerfUnavailable("WINDOW_ALREADY_FINISHED")
		}
		finished = true
		defer b.unlock()
		defer closeAll()
		if b.tid() != tid {
			return matcherPerfUnavailable("BENCHMARK_THREAD_CHANGED")
		}
		if err := b.disable(fds[0]); err != nil {
			return matcherPerfUnavailable(err.Error())
		}
		after, err := b.read(fds[0])
		if err != nil {
			return matcherPerfUnavailable(err.Error())
		}
		if after.enabled < before.enabled || after.running < before.running || len(after.values) != len(ids) {
			return matcherPerfUnavailable("COUNTER_OR_TIME_DECREASED")
		}
		enabled, running := after.enabled-before.enabled, after.running-before.running
		if enabled == 0 || running == 0 || running > enabled {
			return matcherPerfUnavailable("COUNTER_NOT_SCHEDULED")
		}
		counts := make(map[string]uint64, len(ids))
		for i, id := range ids {
			value, ok := after.values[id]
			if !ok || value < before.values[id] {
				return matcherPerfUnavailable("COUNTER_ID_OR_VALUE_CHANGED")
			}
			counts[matcherPerfEvents[i].name] = value - before.values[id]
		}
		return map[string]any{"status": "AVAILABLE", "scope": "BENCHMARK_OS_THREAD_USERSPACE", "process_wide": false,
			"thread_id": tid, "raw_counts": counts, "time_enabled_ns": enabled, "time_running_ns": running,
			"running_fraction": float64(running) / float64(enabled), "scaled": false,
			"quality": "TIMED_LOOP_AND_GO_TIMER_BOOKKEEPING;NOT_PROCESS_CPU_OR_FREQUENCY_HZ"}
	}
}
