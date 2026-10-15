package frontend

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

var issue29322ExecuteMu sync.Mutex
var issue29322Executes = map[uint32]struct {
	UUID  string
	Count uint64
}{}

func issue29322ObserveExecute(connection uint32, statement, text string, binary bool) {
	if os.Getenv("MO_ISSUE29322_ATTRIBUTION10M") != "true" || !binary || !strings.Contains(strings.ToLower(strings.Join(strings.Fields(text), " ")), "from sbtest1") {
		return
	}
	issue29322ExecuteMu.Lock()
	defer issue29322ExecuteMu.Unlock()
	state, exists := issue29322Executes[connection]
	if exists && state.UUID == statement {
		return
	}
	if !exists && len(issue29322Executes) >= 4096 {
		fmt.Println("ISSUE29322_PROBE_TRUNCATED execution")
		return
	}
	data, _ := json.Marshal(map[string]any{"connection_id": connection, "connection_ordinal": state.Count, "statement_id": statement})
	fmt.Println("ISSUE29322_EXECUTION " + string(data))
	issue29322Executes[connection] = struct {
		UUID  string
		Count uint64
	}{statement, state.Count + 1}
}
