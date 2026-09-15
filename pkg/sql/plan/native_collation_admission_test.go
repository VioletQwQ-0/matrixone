package plan

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matrixorigin/matrixone/pkg/container/types"
	planpb "github.com/matrixorigin/matrixone/pkg/pb/plan"
	"github.com/matrixorigin/matrixone/pkg/sql/parsers"
	"github.com/matrixorigin/matrixone/pkg/sql/parsers/dialect"
)

// Existing planner tests exercise the candidate native implementation without
// a running cluster.  This switch exists only in test binaries; production has
// no way to turn the admission fence on before the durable rollout gate lands.
func init() {
	native0900TestAdmission.Store(true)
}

func native0900AdmissionDisabled(t *testing.T) {
	previous := native0900TestAdmission.Swap(false)
	t.Cleanup(func() { native0900TestAdmission.Store(previous) })
}

func TestBuildPlanRejectsNativeCollationByDefault(t *testing.T) {
	native0900AdmissionDisabled(t)
	ctx := NewMockCompilerContext(true)
	stmt, err := parsers.ParseOne(ctx.GetContext(), dialect.MYSQL,
		"select 'Alpha' collate utf8mb4_0900_ai_ci", 1)
	require.NoError(t, err)
	_, err = BuildPlan(ctx, stmt, false)
	require.ErrorContains(t, err, native0900AdmissionError)
}

func TestNativeCollationRelationFormatIsRejectedByDefault(t *testing.T) {
	native0900AdmissionDisabled(t)
	p := &planpb.Plan{Plan: &planpb.Plan_Query{Query: &planpb.Query{
		Nodes: []*planpb.Node{{TableDef: &planpb.TableDef{
			KeyFormat: uint32(types.PADSpaceKeyV1),
		}}},
	}}}
	require.ErrorContains(t,
		requireNative0900PlanAdmission(context.Background(), nil, p),
		native0900AdmissionError)
}
