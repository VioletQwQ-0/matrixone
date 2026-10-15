// Experiment-only reader association. Never a production or TPS subject.
package disttae

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/matrixorigin/matrixone/pkg/catalog"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/pb/plan"
	"github.com/matrixorigin/matrixone/pkg/vm/process"
)

var issue29322AssociationRecords atomic.Uint64

func issue29322AssociationTarget(table *plan.TableDef) bool {
	if table == nil {
		return false
	}
	if table.Name == "sbtest1" && table.Pkey != nil && catalog.IsFakePkName(table.Pkey.PkeyColName) {
		return true
	}
	if !strings.HasPrefix(table.Name, "__mo_index_secondary_") {
		return false
	}
	if os.Getenv("MO_ISSUE29322_ATTRIBUTION10M") == "true" {
		return true
	}
	names := strings.Split(os.Getenv("MO_ISSUE29322_SECONDARY_TABLES"), ",")
	if len(names) > 2 {
		return false
	}
	for _, name := range names {
		if name != "" && name == table.Name {
			return true
		}
	}
	return false
}

// Only copies the bounded textual parameter image. This is not a claim about
// the wire's type or the evaluation domain of a nested CAST.
func issue29322ReaderParameterImage(params *vector.Vector) (string, []any, bool) {
	if params == nil {
		return "ABSENT", nil, true
	}
	oid := params.GetType().Oid.String()
	if !params.GetType().Oid.IsMySQLString() || params.Length() > 128 {
		return oid, nil, false
	}
	image := make([]any, params.Length())
	for i := range image {
		if params.IsNull(uint64(i)) {
			continue
		}
		value := params.GetRawBytesAt(i)
		if len(value) > 64 {
			return oid, nil, false
		}
		image[i] = hex.EncodeToString(value)
	}
	return oid, image, true
}

func issue29322ObserveReaderAssociation(proc *process.Process, table *plan.TableDef, reader any) {
	if os.Getenv("MO_ISSUE29322_READER_PROBE") != "true" || proc == nil ||
		!issue29322AssociationTarget(table) {
		return
	}
	if issue29322AssociationRecords.Add(1) > 1048576 {
		fmt.Println("ISSUE29322_PROBE_TRUNCATED association")
		return
	}
	statementID := ""
	if profile := proc.GetStmtProfile(); profile != nil {
		statementID = profile.GetStmtId().String()
	}
	var ids []uint64
	if p, ok := reader.(interface{ Issue29322ProbeIDs() []uint64 }); ok {
		ids = p.Issue29322ProbeIDs()
	}
	oid, image, complete := issue29322ReaderParameterImage(proc.GetPrepareParams())
	data, err := json.Marshal(map[string]any{
		"kind": "association", "reader_probe_ids": ids, "reader_pointer": fmt.Sprintf("%p", reader),
		"statement_id": statementID, "connection_id": proc.GetSessionInfo().ConnectionID, "table": table.Name,
		"phase":                os.Getenv("MO_ISSUE29322_QUERY_PHASE"),
		"parameter_vector_oid": oid, "parameter_raw_hex": image, "parameter_image_complete": complete,
	})
	if err == nil {
		fmt.Println("ISSUE29322_READER_ASSOCIATION " + string(data))
	}
}
