package plan

import (
	"context"
	"sync/atomic"

	"github.com/matrixorigin/matrixone/pkg/common/moerr"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	planpb "github.com/matrixorigin/matrixone/pkg/pb/plan"
	"github.com/matrixorigin/matrixone/pkg/vm/process"
)

const native0900AdmissionError = "utf8mb4_0900 collation keys are disabled until all cluster nodes support the persisted key format"

// This switch is set only by the package's _test.go file.  Production builds
// have no test escape hatch; in particular, an empty Process service identity
// is not treated as evidence that a native plan is safe to execute.
var native0900TestAdmission atomic.Bool

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

// native0900AdmissionAllowed is deliberately test-only in this phase.  The
// protocol number is not a durable catalog/recovery gate, so a production
// service must not enable native 0900 merely by advertising a version.
func native0900AdmissionAllowed(proc *process.Process) bool {
	return native0900TestAdmission.Load()
}

func requireNative0900Admission(ctx context.Context, proc *process.Process, table *TableDef) error {
	if !tableUsesNative0900(table) || native0900AdmissionAllowed(proc) {
		return nil
	}
	return moerr.NewNotSupportedNoCtx(native0900AdmissionError)
}

// requireNative0900PlanAdmission is the final local planner fence.  DDL
// checks protect new catalog objects, while this check also rejects a query
// which only carries an explicit COLLATE expression or reaches a relation
// whose native key format survives optimization without a string expression.
func requireNative0900PlanAdmission(ctx context.Context, proc *process.Process, p *planpb.Plan) error {
	if p == nil || native0900AdmissionAllowed(proc) {
		return nil
	}
	features, err := planpb.RequiredRemoteExpressionFeatures(p)
	if err != nil {
		return err
	}
	if !features.NativeCollationV1 {
		return nil
	}
	return moerr.NewNotSupportedNoCtx(native0900AdmissionError)
}
