// Copyright 2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package search

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/matrixorigin/matrixone/pkg/fulltext2"
	ft2plan "github.com/matrixorigin/matrixone/pkg/fulltext2/plugin/plan"
	"github.com/matrixorigin/matrixone/pkg/testutil"
	"github.com/matrixorigin/matrixone/pkg/vm/process"
	"github.com/stretchr/testify/require"
)

func TestDiagnosticGateRejectsInvalidOrUnselectedPayloads(t *testing.T) {
	hash := sha256.Sum256([]byte("alpha"))
	encoded := hex.EncodeToString(hash[:])
	for _, value := range []string{"", "1", encoded + ",", encoded + "," + encoded, strings.Repeat("0", 64) + "," + strings.Repeat("x", 64)} {
		require.Nil(t, parseDiagnosticGate(value))
	}
	hashes := make([]string, 0, 5)
	for i := range 5 {
		sum := sha256.Sum256([]byte{byte(i)})
		hashes = append(hashes, hex.EncodeToString(sum[:]))
	}
	require.NotNil(t, parseDiagnosticGate(strings.Join(hashes[:4], ",")))
	require.Nil(t, parseDiagnosticGate(strings.Join(hashes, ",")))
	g := parseDiagnosticGate(encoded)
	require.NotNil(t, g)
	_, n := g.claim([]byte("beta"))
	require.Zero(t, n)
	for i := uint32(1); i <= 3; i++ {
		got, n := g.claim([]byte("alpha"))
		require.Equal(t, encoded, got)
		require.Equal(t, i, n)
	}
	_, n = g.claim([]byte("alpha"))
	require.Zero(t, n)
}

func TestDiagnosticGateConcurrentBound(t *testing.T) {
	hash := sha256.Sum256([]byte("alpha"))
	g := parseDiagnosticGate(hex.EncodeToString(hash[:]))
	var wg sync.WaitGroup
	var claimed atomic.Uint32
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, n := g.claim([]byte("alpha")); n != 0 {
				claimed.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 3, claimed.Load())
}

func TestDiagnosticSelectorPublishesBoundedImmutableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload-hashes")
	s := &diagnosticSelector{source: "@" + path}
	require.Nil(t, s.load(), "early readiness queries cannot consume an absent configuration")
	require.Nil(t, (&diagnosticSelector{source: "@" + filepath.Dir(path)}).load())
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("a", 261)), 0600))
	require.Nil(t, s.load(), "oversized configuration is rejected")
	hash := sha256.Sum256([]byte("alpha"))
	encoded := hex.EncodeToString(hash[:])
	require.NoError(t, os.WriteFile(path, []byte(encoded), 0600))
	g := s.load()
	require.NotNil(t, g)
	require.NoError(t, os.WriteFile(path, []byte("invalid"), 0600))
	require.Same(t, g, s.load(), "the published generation is never reloaded")
	_, n := g.claim([]byte("alpha"))
	require.EqualValues(t, 1, n)
}

func TestSearchDiagnosticBindingAndTerminalStatus(t *testing.T) {
	proc := testutil.NewProc(t)
	proc.SetQueryId("query-one")
	statementID := uuid.MustParse("f1c24306-16b6-4dc1-b74f-645a3de95f11")
	proc.SetStmtProfile(process.NewStmtProfile(uuid.Nil, statementID))
	proc.GetStmtProfile().SetSqlOfStmt("SELECT synthetic")
	u := &reader{proc: proc, spec: scanSpec(t, ft2plan.ScanOptions{}), plannedLimit: 200, limit: 200}
	for _, tc := range []struct {
		name      string
		err       error
		cancelled bool
		limit     uint64
		status    string
	}{
		{"complete", nil, false, 200, "COMPLETE"},
		{"error", errors.New("test failure"), false, 200, "ERROR"},
		{"cancelled", nil, true, 200, "CANCELLED"},
		{"limit cleared", nil, false, 0, "INCONCLUSIVE"},
		{"missing statement", nil, false, 200, "INCONCLUSIVE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc.GetStmtProfile().SetStmtId(statementID)
			if tc.name == "missing statement" {
				proc.GetStmtProfile().SetStmtId(uuid.Nil)
			}
			hash := sha256.Sum256([]byte("private pattern"))
			g := parseDiagnosticGate(hex.EncodeToString(hash[:]))
			q := fulltext2.Fulltext2Query{Pattern: []byte("private pattern"), IncludePredsJSON: []byte(`[{"col":0,"op":"is_null"}]`)}
			u.limit = tc.limit
			r := u.beginSearchDiagnosticWithGate(&q, g)
			require.NotNil(t, r)
			require.Equal(t, "query-one", r.QueryID)
			if tc.name != "missing statement" {
				require.Equal(t, statementID.String(), r.StatementID)
			}
			sqlSum := sha256.Sum256([]byte("SELECT synthetic"))
			require.Equal(t, hex.EncodeToString(sqlSum[:]), r.SQLSHA256)
			require.Len(t, r.ScanSHA256, 64)
			require.Len(t, r.IncludeSHA256, 64)
			if tc.limit == 0 {
				require.Nil(t, q.Diagnostics)
				require.Equal(t, "stream", r.Path)
			} else {
				require.Same(t, r.Work, q.Diagnostics)
			}
			r.finish(tc.err, tc.cancelled)
			require.Equal(t, tc.status, r.Status)
			encoded, err := json.Marshal(r)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "private pattern")
			require.NotContains(t, string(encoded), "test failure")
		})
	}
}
