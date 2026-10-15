package frontend

import (
	"os"
	"sync"
	"testing"
)

func TestIssue29322ExecuteProbeIdentity(t *testing.T) {
	t.Setenv("MO_ISSUE29322_ATTRIBUTION10M", "true")
	old := os.Stdout
	out, err := os.CreateTemp(t.TempDir(), "probe")
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = out
	defer func() { os.Stdout = old; out.Close() }()
	issue29322Executes = map[uint32]struct {
		UUID  string
		Count uint64
	}{}
	issue29322ObserveExecute(1, "first", "SELECT id FROM sbtest1", false)
	issue29322ObserveExecute(1, "first", "SELECT id FROM sbtest1", true)
	issue29322ObserveExecute(1, "first", "SELECT id FROM sbtest1", true)
	issue29322ObserveExecute(1, "second", "SELECT id FROM sbtest1", true)
	issue29322ObserveExecute(2, "separate", "SELECT id FROM sbtest1", true)
	if issue29322Executes[1].Count != 2 || issue29322Executes[2].Count != 1 {
		t.Fatal("connection generations not independent")
	}
	var wg sync.WaitGroup
	for i := uint32(3); i < 35; i++ {
		wg.Add(1)
		go func(id uint32) { defer wg.Done(); issue29322ObserveExecute(id, "uuid", "SELECT id FROM sbtest1", true) }(i)
	}
	wg.Wait()
	if len(issue29322Executes) != 34 {
		t.Fatal("concurrent generation capture incomplete")
	}
	t.Setenv("MO_ISSUE29322_ATTRIBUTION10M", "false")
	issue29322ObserveExecute(99, "disabled", "SELECT id FROM sbtest1", true)
	if len(issue29322Executes) != 34 {
		t.Fatal("disabled probe mutated state")
	}
}
