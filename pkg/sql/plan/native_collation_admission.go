package plan

import (
	"context"

	"github.com/matrixorigin/matrixone/pkg/common/moerr"
	"github.com/matrixorigin/matrixone/pkg/common/runtime"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/defines"
	"github.com/matrixorigin/matrixone/pkg/vm/process"
)

// Native 0900 semantics are compiled and tested in this change, but are not
// admitted by a production service until every participant understands the
// persisted key format and plan metadata.  The protocol value is deliberately
// above the current latest value; rollout code must raise it only after the
// durable catalog/recovery gate is complete.
const native0900AdmissionProtocol = defines.MORPCVersionNativeCollation

func tableUsesNative0900(table *TableDef) bool {
	if table == nil {
		return false
	}
	if table.KeyFormat == uint32(types.PADSpaceKeyV1) {
		return true
	}
	for _, col := range table.Cols {
		if col != nil && types.IsNative0900Collation(uint8(col.Typ.Charset)) {
			return true
		}
	}
	for _, index := range table.Indexes {
		if index != nil && index.KeyFormat == uint32(types.PADSpaceKeyV1) {
			return true
		}
	}
	return false
}

// native0900AdmissionAllowed intentionally treats the empty service used by
// planner unit tests as an offline compiler.  Real CN/TN processes have a
// service identity and must have an explicit rollout protocol value.
func native0900AdmissionAllowed(proc *process.Process) bool {
	if proc == nil || proc.GetService() == "" {
		return true
	}
	rt := runtime.ServiceRuntime(proc.GetService())
	if rt == nil {
		return false
	}
	value, ok := rt.GetGlobalVariables(runtime.MOProtocolVersion)
	version, valid := value.(int64)
	return ok && valid && version >= native0900AdmissionProtocol
}

func requireNative0900Admission(ctx context.Context, proc *process.Process, table *TableDef) error {
	if !tableUsesNative0900(table) || native0900AdmissionAllowed(proc) {
		return nil
	}
	return moerr.NewNotSupportedNoCtx(
		"utf8mb4_0900 collation keys are disabled until all cluster nodes support the persisted key format",
	)
}
