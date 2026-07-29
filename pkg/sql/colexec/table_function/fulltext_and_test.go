// Copyright 2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package table_function

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matrixorigin/matrixone/pkg/common/moerr"
	"github.com/matrixorigin/matrixone/pkg/common/mpool"
	"github.com/matrixorigin/matrixone/pkg/container/batch"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/fulltext"
	"github.com/matrixorigin/matrixone/pkg/sql/parsers/tree"
	"github.com/matrixorigin/matrixone/pkg/util/executor"
	"github.com/matrixorigin/matrixone/pkg/vectorindex/sqlexec"
	"github.com/matrixorigin/matrixone/pkg/vm"
	"github.com/matrixorigin/matrixone/pkg/vm/process"
	"github.com/stretchr/testify/require"
)

func TestFulltextAndCandidatesIntegerWidths(t *testing.T) {
	cases := []struct {
		name   string
		typ    types.Type
		values []any
	}{
		{"int8", types.T_int8.ToType(), []any{int8(-1), int8(2), int8(-1)}},
		{"int16", types.T_int16.ToType(), []any{int16(-1), int16(2), int16(-1)}},
		{"int32", types.T_int32.ToType(), []any{int32(-1), int32(2), int32(-1)}},
		{"int64", types.T_int64.ToType(), []any{int64(-1), int64(2), int64(-1)}},
		{"uint8", types.T_uint8.ToType(), []any{uint8(1), uint8(2), uint8(1)}},
		{"uint16", types.T_uint16.ToType(), []any{uint16(1), uint16(2), uint16(1)}},
		{"uint32", types.T_uint32.ToType(), []any{uint32(1), uint32(2), uint32(1)}},
		{"uint64", types.T_uint64.ToType(), []any{uint64(1), uint64(2), uint64(1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mp := mpool.MustNewZero()
			src := vector.NewVec(tc.typ)
			defer src.Free(mp)
			for _, value := range tc.values {
				require.NoError(t, vector.AppendAny(src, value, false, mp))
			}
			candidates := &fulltextAndCandidates{}
			defer candidates.free(mp)
			require.NoError(t, candidates.addDriverVector(src, mp))
			require.Equal(t, 2, candidates.vec.Length())
			require.Equal(t, uint64(2), candidates.hm.GroupCount())
		})
	}
}

func makePostingResult(procMP *mpool.MPool, ids ...int32) executor.Result {
	bat := batch.NewWithSize(1)
	bat.Vecs[0] = vector.NewVec(types.T_int32.ToType())
	for _, id := range ids {
		_ = vector.AppendFixed[int32](bat.Vecs[0], id, false, procMP)
	}
	bat.SetRowCount(len(ids))
	return executor.Result{Mp: procMP, Batches: []*batch.Batch{bat}}
}

func TestRunFulltextAndIntersectionDeduplicatesAndFilters(t *testing.T) {
	ut := newFTTestCase(t, mpool.MustNewZero(), ftdefaultAttrs, fulltext.ALGO_TFIDF, 100)
	ut.arg.OpAnalyzer = process.NewAnalyzer(0, false, false, "fulltext_index_scan")
	state := &fulltextState{strictAndTerms: []string{"rare", "common"}, limit: 100}
	s := &fulltext.SearchAccum{TblName: "index_table"}

	previous := ft_runSql_streaming
	defer func() { ft_runSql_streaming = previous }()
	var calls int
	ft_runSql_streaming = func(
		ctx context.Context,
		sqlProc *sqlexec.SqlProcess,
		sql string,
		ch chan executor.Result,
		errCh chan error,
	) (executor.Result, error) {
		calls++
		if strings.Contains(sql, "'rare'") {
			require.Empty(t, sqlProc.FulltextMembershipFilter)
			ch <- makePostingResult(sqlProc.Proc.Mp(), 1, 1, 2, 3, 4)
		} else {
			require.NotEmpty(t, sqlProc.FulltextMembershipFilter)
			// 5 is deliberately outside the driver set. The runtime must verify
			// membership even if a test executor does not apply the reader hint.
			ch <- makePostingResult(sqlProc.Proc.Mp(), 2, 2, 4, 5)
		}
		return executor.Result{}, nil
	}

	fast, err := runFulltextAndIntersection(state, ut.proc, ut.arg, s)
	require.NoError(t, err)
	require.True(t, fast)
	require.Equal(t, 2, calls)
	require.Len(t, state.resbuf, 2)
	require.Equal(t, int32(2), state.resbuf[0].Id)
	require.Equal(t, int32(4), state.resbuf[1].Id)
	extra := ut.arg.OpAnalyzer.GetOpStats().ExtraStats
	require.Equal(t, int64(1), extra["FulltextFastPathCount"])
	require.Equal(t, int64(4), extra["FulltextDriverDocs"])
	require.Equal(t, int64(9), extra["FulltextPostingRows"])
}

func TestFulltextIndexMatchUsesIntersectionWithoutCountStar(t *testing.T) {
	ut := newFTTestCase(t, mpool.MustNewZero(), ftdefaultAttrs, fulltext.ALGO_TFIDF, 2)
	ut.arg.Params = []byte(`{"filter_only_and":true}`)
	ut.arg.Args = makeConstInputExprsFTWithPattern("+Matrix +Origin", int64(tree.FULLTEXT_BOOLEAN))
	inbat := makeBatchFT(ut.proc)
	defer inbat.Clean(ut.proc.Mp())
	require.NoError(t, ut.arg.Prepare(ut.proc))
	for i := range ut.arg.ctr.executorsForArgs {
		var err error
		ut.arg.ctr.argVecs[i], err = ut.arg.ctr.executorsForArgs[i].Eval(ut.proc, []*batch.Batch{inbat}, nil)
		require.NoError(t, err)
	}

	previousEstimate := ftEstimateAndOrderFulltextTerms
	previousRunSQL := ft_runSql
	previousStreaming := ft_runSql_streaming
	defer func() {
		ftEstimateAndOrderFulltextTerms = previousEstimate
		ft_runSql = previousRunSQL
		ft_runSql_streaming = previousStreaming
	}()
	ftEstimateAndOrderFulltextTerms = func(*process.Process, process.Analyzer, string, []string) ([]string, uint64, int64, error) {
		return []string{"origin", "matrix"}, 2, 0, nil
	}
	ft_runSql = func(*sqlexec.SqlProcess, string) (executor.Result, error) {
		t.Fatal("strict-AND fast path must not execute runCountStar")
		return executor.Result{}, nil
	}
	ft_runSql_streaming = func(ctx context.Context, sqlProc *sqlexec.SqlProcess, sql string, ch chan executor.Result, errCh chan error) (executor.Result, error) {
		if strings.Contains(sql, "'origin'") {
			ch <- makePostingResult(sqlProc.Proc.Mp(), 1, 2, 3)
		} else {
			ch <- makePostingResult(sqlProc.Proc.Mp(), 2, 3)
		}
		return executor.Result{}, nil
	}

	require.NoError(t, ut.arg.ctr.state.start(ut.arg, ut.proc, 0, nil))
	result, err := ut.arg.ctr.state.call(ut.arg, ut.proc)
	require.NoError(t, err)
	require.Equal(t, vm.ExecNext, result.Status)
	require.Equal(t, 2, result.Batch.RowCount())
	result, err = ut.arg.ctr.state.call(ut.arg, ut.proc)
	require.NoError(t, err)
	require.Equal(t, vm.ExecStop, result.Status)
	requireStateFreeReturns(t, ut.arg.ctr.state, ut.arg, ut.proc)
}

func TestRunFulltextAndIntersectionEstimateFallbackDoesNotStartSQL(t *testing.T) {
	ut := newFTTestCase(t, mpool.MustNewZero(), ftdefaultAttrs, fulltext.ALGO_TFIDF, 100)
	ut.arg.OpAnalyzer = process.NewAnalyzer(0, false, false, "fulltext_index_scan")
	state := &fulltextState{
		strictAndTerms:  []string{"rare", "common"},
		limit:           100,
		estimatedDriver: maxFulltextAndCandidates + 1,
	}
	previous := ft_runSql_streaming
	defer func() { ft_runSql_streaming = previous }()
	ft_runSql_streaming = func(context.Context, *sqlexec.SqlProcess, string, chan executor.Result, chan error) (executor.Result, error) {
		t.Fatal("posting SQL must not start after estimated capacity overflow")
		return executor.Result{}, nil
	}
	fast, err := runFulltextAndIntersection(state, ut.proc, ut.arg, &fulltext.SearchAccum{TblName: "index_table"})
	require.NoError(t, err)
	require.False(t, fast)
	require.Equal(t, int64(1), ut.arg.OpAnalyzer.GetOpStats().ExtraStats["FulltextFallbackCount"])
}

func TestRunFulltextAndIntersectionActualCapacityFallbackEmitsNothing(t *testing.T) {
	ut := newFTTestCase(t, mpool.MustNewZero(), ftdefaultAttrs, fulltext.ALGO_TFIDF, 100)
	ut.arg.OpAnalyzer = process.NewAnalyzer(0, false, false, "fulltext_index_scan")
	state := &fulltextState{strictAndTerms: []string{"driver", "other"}, limit: 100}
	previous := ft_runSql_streaming
	defer func() { ft_runSql_streaming = previous }()
	ft_runSql_streaming = func(ctx context.Context, sqlProc *sqlexec.SqlProcess, sql string, ch chan executor.Result, errCh chan error) (executor.Result, error) {
		for base := uint64(0); base <= maxFulltextAndCandidates; base += 8192 {
			count := int(min(uint64(8192), maxFulltextAndCandidates+1-base))
			ids := make([]int32, count)
			for i := range ids {
				ids[i] = int32(base) + int32(i)
			}
			select {
			case ch <- makePostingResult(sqlProc.Proc.Mp(), ids...):
			case <-ctx.Done():
				return executor.Result{}, context.Cause(ctx)
			}
		}
		return executor.Result{}, nil
	}

	fast, err := runFulltextAndIntersection(state, ut.proc, ut.arg, &fulltext.SearchAccum{TblName: "index_table"})
	require.NoError(t, err)
	require.False(t, fast)
	require.Empty(t, state.resbuf)
	require.Equal(t, int64(1), ut.arg.OpAnalyzer.GetOpStats().ExtraStats["FulltextFallbackCount"])
}

func TestRunFulltextAndIntersectionPropagatesStreamError(t *testing.T) {
	ut := newFTTestCase(t, mpool.MustNewZero(), ftdefaultAttrs, fulltext.ALGO_TFIDF, 100)
	ut.arg.OpAnalyzer = process.NewAnalyzer(0, false, false, "fulltext_index_scan")
	state := &fulltextState{strictAndTerms: []string{"rare", "common"}, limit: 100}
	previous := ft_runSql_streaming
	defer func() { ft_runSql_streaming = previous }()
	ft_runSql_streaming = func(ctx context.Context, sqlProc *sqlexec.SqlProcess, sql string, ch chan executor.Result, errCh chan error) (executor.Result, error) {
		return executor.Result{}, moerr.NewInternalError(ctx, "posting failure")
	}
	fast, err := runFulltextAndIntersection(state, ut.proc, ut.arg, &fulltext.SearchAccum{TblName: "index_table"})
	require.ErrorContains(t, err, "posting failure")
	require.False(t, fast)
	require.Empty(t, state.resbuf)
}

func TestStreamFulltextPostingConsumerErrorCancelsAndDrains(t *testing.T) {
	ut := newFTTestCase(t, mpool.MustNewZero(), ftdefaultAttrs, fulltext.ALGO_TFIDF, 100)
	previous := ft_runSql_streaming
	defer func() { ft_runSql_streaming = previous }()
	cancelObserved := make(chan struct{})
	ft_runSql_streaming = func(ctx context.Context, sqlProc *sqlexec.SqlProcess, sql string, ch chan executor.Result, errCh chan error) (executor.Result, error) {
		ch <- makePostingResult(sqlProc.Proc.Mp(), 1, 2)
		<-ctx.Done()
		close(cancelObserved)
		return executor.Result{}, context.Cause(ctx)
	}
	wantErr := moerr.NewInternalError(ut.proc.Ctx, "consumer failure")
	_, err := streamFulltextPosting(ut.proc, "SELECT doc_id", nil, func(*vector.Vector) error {
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
	select {
	case <-cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("stream producer did not observe cancellation")
	}
}
