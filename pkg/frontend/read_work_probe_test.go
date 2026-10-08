// Copyright 2022 Matrix Origin
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

package frontend

import (
	"github.com/matrixorigin/matrixone/pkg/defines"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReadWorkProbeLabel(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"select 1", ""}, {"select /*mo-read-work:p01*/ 1", "p01"},
		{"/*mo-read-work:*/ select 1", ""}, {"/*mo-read-work:p01", ""},
		{"/*mo-read-work:x y*/ select 1", ""},
		{"/*mo-read-work:012345678901234567890123456789012*/ select 1", ""},
	} {
		if got := readWorkProbeLabel(tc.sql); got != tc.want {
			t.Fatalf("label=%q want=%q", got, tc.want)
		}
	}
}

func TestReadWorkProbeBinaryContextIsolation(t *testing.T) {
	for _, sql := range []string{"select /*mo-read-work:p01*/ 1", "select 1"} {
		ses, prepared, cw, execCtx := newPreparedExecuteEnvForSQL(t, 191, sql)
		func() {
			defer prepared.Close()
			_, _, _, _, _, err := initExecuteStmtParam(execCtx, ses, cw, nil, prepared.Name)
			require.NoError(t, err)
			probe, ok := execCtx.reqCtx.Value(defines.ReadWorkProbeKey{}).(defines.ReadWorkProbe)
			if sql == "select 1" {
				require.False(t, ok)
			} else {
				require.True(t, ok)
				require.Equal(t, "p01", probe.Label)
				require.NotEmpty(t, probe.Execution)
			}
		}()
	}
}
