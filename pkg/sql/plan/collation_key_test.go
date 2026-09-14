// Copyright 2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plan

import (
	"testing"

	"github.com/matrixorigin/matrixone/pkg/container/batch"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	pb "github.com/matrixorigin/matrixone/pkg/pb/plan"
	"github.com/matrixorigin/matrixone/pkg/sql/colexec"
	"github.com/matrixorigin/matrixone/pkg/testutil"
	"github.com/stretchr/testify/require"
)

func TestCollationProbeExpressionMatchesStoredTuple(t *testing.T) {
	proc := testutil.NewProcess(t)
	defer proc.Free()
	col := pb.Type{Id: int32(types.T_varchar), Width: 5, Charset: uint32(types.CharsetUTF8)}
	for _, value := range []string{"Alpha", "alpha ", "a\x00", "a \x00", "a b", "a value longer than the column width", "中😀"} {
		input := makePlan2StringConstExprWithType(value)
		key, err := MakeCollationKeyExpr(proc.Ctx, input, col, types.PADSpaceKeyV1)
		require.NoError(t, err)
		require.Equal(t, uint32(types.CharsetBinary), key.Typ.Charset)
		again, err := MakeCollationKeyExpr(proc.Ctx, key, col, types.PADSpaceKeyV1)
		require.NoError(t, err)
		require.Same(t, key, again)
		serial, err := BindFuncExprImplByPlanExpr(proc.Ctx, "serial", []*pb.Expr{makePlan2Int64ConstExprWithType(42), key})
		require.NoError(t, err)
		// Exercise the persisted plan transport and the real vector executor.
		encoded, err := serial.Marshal()
		require.NoError(t, err)
		var restored pb.Expr
		require.NoError(t, restored.Unmarshal(encoded))
		exec, err := colexec.NewExpressionExecutor(proc, &restored)
		require.NoError(t, err)
		vec, err := exec.Eval(proc, []*batch.Batch{batch.EmptyForConstFoldBatch}, nil)
		require.NoError(t, err)
		part, err := types.ResolveStringKeyPart(types.NewWithCharset(types.T_varchar, 5, 0, types.CharsetUTF8), types.PADSpaceKeyV1)
		require.NoError(t, err)
		p := types.NewPacker()
		p.EncodeInt64(42)
		_, err = part.Encode(p, nil, []byte(value))
		require.NoError(t, err)
		require.Equal(t, p.GetBuf(), vec.GetBytesAt(0), "probe must not be truncated to column width")
		p.Close()
		exec.Free()
		legacy, err := MakeCollationKeyExpr(proc.Ctx, input, col, types.LegacyKeyFormat)
		require.NoError(t, err)
		require.Same(t, input, legacy)
	}
}
