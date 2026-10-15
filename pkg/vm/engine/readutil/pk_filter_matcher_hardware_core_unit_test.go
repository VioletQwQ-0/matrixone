package readutil

import (
	"encoding/binary"
	"errors"
	"testing"
)

type fakeMatcherPerf struct {
	opens, locks, unlocks, reads, closes           int
	failOpen, failID                               int
	failEnable, failDisable, failBefore, failAfter bool
	after                                          matcherPerfSnapshot
	releaseSequence                                []string
	groups                                         []int
}

func (f *fakeMatcherPerf) backend() matcherPerfBackend {
	return matcherPerfBackend{
		lock: func() { f.locks++ }, unlock: func() { f.unlocks++; f.releaseSequence = append(f.releaseSequence, "unlock") }, tid: func() int { return 7 },
		open: func(config uint64, group int) (int, error) {
			f.groups = append(f.groups, group)
			f.opens++
			if f.opens == f.failOpen {
				return -1, errors.New("OPEN_DENIED")
			}
			return f.opens - 1, nil
		},
		id: func(fd int) (uint64, error) {
			if fd+1 == f.failID {
				return 0, errors.New("ID_FAILED")
			}
			return uint64(fd + 1), nil
		},
		enable: func(int) error {
			if f.failEnable {
				return errors.New("ENABLE_FAILED")
			}
			return nil
		},
		disable: func(int) error {
			if f.failDisable {
				return errors.New("DISABLE_FAILED")
			}
			return nil
		},
		read: func(int) (matcherPerfSnapshot, error) {
			f.reads++
			if f.reads == 1 {
				if f.failBefore {
					return matcherPerfSnapshot{}, errors.New("BEFORE_FAILED")
				}
				return matcherPerfSnapshot{values: map[uint64]uint64{1: 0, 2: 0, 3: 0, 4: 0, 5: 0}}, nil
			}
			if f.failAfter {
				return matcherPerfSnapshot{}, errors.New("AFTER_FAILED")
			}
			return f.after, nil
		},
		close: func(int) error { f.closes++; f.releaseSequence = append(f.releaseSequence, "close"); return nil },
	}
}
func goodMatcherPerf() *fakeMatcherPerf {
	return &fakeMatcherPerf{after: matcherPerfSnapshot{enabled: 100, running: 100, values: map[uint64]uint64{1: 200, 2: 300, 3: 100, 4: 0, 5: 2}}}
}

func TestIssue29322MatcherHardwareCloseAndNoDoubleFinish(t *testing.T) {
	f := goodMatcherPerf()
	finish := startMatcherPerfWindow(f.backend())
	r := finish()
	if r["status"] != "AVAILABLE" || r["process_wide"] != false || r["running_fraction"] != float64(1) {
		t.Fatal("wrong qualified scope or time")
	}
	if f.locks != 1 || f.unlocks != 1 || f.closes != 5 || f.groups[0] != -1 || f.groups[1] != 0 {
		t.Fatal("fd0/group lifecycle wrong")
	}
	if len(f.releaseSequence) != 6 || f.releaseSequence[5] != "unlock" {
		t.Fatal("thread unlocked before descriptor release")
	}
	if finish()["status"] != "NOT_AVAILABLE" || f.closes != 5 || f.unlocks != 1 {
		t.Fatal("duplicate close/unlock")
	}
}
func TestIssue29322MatcherHardwarePartialFailure(t *testing.T) {
	for _, stage := range []string{"open", "id", "enable", "before", "disable", "after"} {
		t.Run(stage, func(t *testing.T) {
			f := goodMatcherPerf()
			want := 5
			switch stage {
			case "open":
				f.failOpen = 3
				want = 2
			case "id":
				f.failID = 3
				want = 3
			case "enable":
				f.failEnable = true
			case "before":
				f.failBefore = true
			case "disable":
				f.failDisable = true
			case "after":
				f.failAfter = true
			}
			r := startMatcherPerfWindow(f.backend())()
			if r["status"] != "NOT_AVAILABLE" || f.closes != want || f.unlocks != 1 {
				t.Fatal("partial failure leaked or falsely available")
			}
		})
	}
}
func TestIssue29322MatcherHardwareNoSyntheticCounterRepair(t *testing.T) {
	for _, kind := range []string{"not-scheduled", "time-decrease", "missing-id", "different-id"} {
		t.Run(kind, func(t *testing.T) {
			f := goodMatcherPerf()
			switch kind {
			case "not-scheduled":
				f.after.running = 0
			case "time-decrease":
				f.after.enabled = 0
			case "missing-id":
				delete(f.after.values, 5)
			case "different-id":
				delete(f.after.values, 5)
				f.after.values[6] = 2
			}
			r := startMatcherPerfWindow(f.backend())()
			if r["status"] != "NOT_AVAILABLE" || f.closes != 5 || f.unlocks != 1 {
				t.Fatal("invalid counters passed or leaked")
			}
		})
	}
	f := goodMatcherPerf()
	f.after.running = 50
	r := startMatcherPerfWindow(f.backend())()
	if r["status"] != "AVAILABLE" || r["running_fraction"] != float64(.5) || r["scaled"] != false {
		t.Fatal("multiplex was normalized or hidden")
	}
}
func TestIssue29322MatcherHardwareReadBinding(t *testing.T) {
	data := make([]byte, (3+2*len(matcherPerfEvents))*8)
	binary.LittleEndian.PutUint64(data, uint64(len(matcherPerfEvents)))
	binary.LittleEndian.PutUint64(data[8:], 100)
	binary.LittleEndian.PutUint64(data[16:], 90)
	for i := range matcherPerfEvents {
		binary.LittleEndian.PutUint64(data[24+i*16:], uint64(i*10))
		binary.LittleEndian.PutUint64(data[32+i*16:], uint64(i+1))
	}
	r, err := decodeMatcherPerfRead(data)
	if err != nil || len(r.values) != 5 || r.values[5] != 40 {
		t.Fatal("group binding failed")
	}
	for _, kind := range []string{"short", "count", "duplicate-id", "zero-id", "running"} {
		t.Run(kind, func(t *testing.T) {
			bad := append([]byte(nil), data...)
			switch kind {
			case "short":
				bad = bad[:len(bad)-1]
			case "count":
				binary.LittleEndian.PutUint64(bad, 6)
			case "duplicate-id":
				binary.LittleEndian.PutUint64(bad[48:], 1)
			case "zero-id":
				binary.LittleEndian.PutUint64(bad[32:], 0)
			case "running":
				binary.LittleEndian.PutUint64(bad[16:], 101)
			}
			if _, err := decodeMatcherPerfRead(bad); err == nil {
				t.Fatal("malformed counter group accepted")
			}
		})
	}
}
