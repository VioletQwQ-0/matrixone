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

package readutil

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"testing"

	"github.com/matrixorigin/matrixone/pkg/common/mpool"
	"github.com/matrixorigin/matrixone/pkg/container/batch"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/defines"
	"github.com/matrixorigin/matrixone/pkg/fileservice"
	"github.com/matrixorigin/matrixone/pkg/objectio"
	"github.com/matrixorigin/matrixone/pkg/objectio/ioutil"
	"github.com/matrixorigin/matrixone/pkg/pb/timestamp"
	"github.com/matrixorigin/matrixone/pkg/sql/plan/function"
	"github.com/matrixorigin/matrixone/pkg/util/toml"
	"github.com/matrixorigin/matrixone/pkg/vm/engine"
	"github.com/matrixorigin/matrixone/pkg/vm/engine/tae/blockio"
	"github.com/matrixorigin/matrixone/pkg/vm/engine/tae/containers"
	"github.com/stretchr/testify/require"
)

// This prototype is installed only by tests after ordinary filter construction.
// It retains the baseline fallback and never mutates or caches a candidate subset.
func matcherBenchRangeSearch(values []uint64, fallback objectio.ReadFilterSearchFuncType) objectio.ReadFilterRangeSearchFuncType {
	ordered := slices.IsSorted(values)
	return func(vectors containers.Vectors, bounds objectio.Uint64Range) []int64 {
		if !ordered || !bounds.Valid || bounds.Min > bounds.Max || len(vectors) != 1 ||
			vectors[0].GetType().Oid != types.T_uint64 {
			return fallback(vectors)
		}
		first := sort.Search(len(values), func(i int) bool { return values[i] >= bounds.Min })
		end := sort.Search(len(values), func(i int) bool { return values[i] > bounds.Max })
		if first == end {
			return nil
		}
		// Keep the generic matching implementation. Its factory/subset cost is
		// deliberately inside the per-block call, not removed from measurement.
		return vector.OrderedLinearSearchOffsetByValFactory(values[first:end], nil)(&vectors[0])
	}
}

func TestIssue29322MatcherRangeOracle(t *testing.T) {
	mp := mpool.MustNewZero()
	t.Cleanup(func() { require.Zero(t, mp.CurrNB()); mpool.DeleteMPool(mp) })
	rows := []uint64{7, 3, 7, 9, math.MaxUint64, 0}
	input := matcherBenchVector(t, mp, rows)
	for _, values := range [][]uint64{nil, {7}, {3, 7, 7, 9}, {0, 3, 7, math.MaxUint64}, {9, 3, 7}, {1, 2}} {
		before := slices.Clone(values)
		full := vector.OrderedLinearSearchOffsetByValFactory(values, nil)
		fallback := func(v containers.Vectors) []int64 { return full(&v[0]) }
		search := matcherBenchRangeSearch(values, fallback)
		for _, bounds := range []objectio.Uint64Range{{}, {Valid: true, Min: 0, Max: math.MaxUint64}, {Valid: true, Min: 1, Max: 0}} {
			require.Equal(t, matcherBenchOracle(values, rows), search(containers.Vectors{*input}, bounds))
		}
		require.Equal(t, before, values)
	}
	// Two different block ranges must not share a retained narrowed slice.
	values := []uint64{1, 3, 7, 19, 999}
	full := vector.OrderedLinearSearchOffsetByValFactory(values, nil)
	search := matcherBenchRangeSearch(values, func(v containers.Vectors) []int64 { return full(&v[0]) })
	for _, blockRows := range [][]uint64{{19, 7, 19}, {3, 1, 3}, {7, 3, 19}} {
		v := matcherBenchVector(t, mp, blockRows)
		bounds := objectio.Uint64Range{Min: slices.Min(blockRows), Max: slices.Max(blockRows), Valid: true}
		require.Equal(t, matcherBenchOracle(values, blockRows), search(containers.Vectors{*v}, bounds))
	}
	require.Equal(t, []uint64{1, 3, 7, 19, 999}, values)
}

func TestIssue29322MatcherRangeResetAndTypeGuard(t *testing.T) {
	mp := mpool.MustNewZero()
	t.Cleanup(func() { require.Zero(t, mp.CurrNB()); mpool.DeleteMPool(mp) })
	input := matcherBenchVector(t, mp, []uint64{3, 1, 2})
	var readers [2]withFilterMixin
	var cleanups [2]int
	for i := range readers {
		candidate := matcherBenchVector(t, mp, []uint64{uint64(i + 1)})
		base := BasePKFilter{Valid: true, Op: function.IN, Oid: types.T_uint64, Vec: candidate,
			cleanup: &basePKFilterCleanup{fns: []func(){func() { cleanups[i]++ }}}}
		filter, err := ConstructBlockPKFilter(true, base, nil)
		require.NoError(t, err)
		filter.UnSortedSearchFuncWithRange = matcherBenchRangeSearch(vector.MustFixedColWithTypeCheck[uint64](candidate), filter.UnSortedSearchFunc)
		readers[i].filterState.filter = filter
		t.Cleanup(readers[i].reset)
	}
	bounds := objectio.Uint64Range{Min: 1, Max: 3, Valid: true}
	require.Equal(t, []int64{1}, readers[0].filterState.filter.UnSortedSearchFuncWithRange(containers.Vectors{*input}, bounds))
	readers[0].reset()
	readers[0].reset()
	require.Nil(t, readers[0].filterState.filter.UnSortedSearchFuncWithRange)
	require.Equal(t, [2]int{1, 0}, cleanups)
	require.Equal(t, []int64{2}, readers[1].filterState.filter.UnSortedSearchFuncWithRange(containers.Vectors{*input}, bounds))
	wrong := vector.NewOffHeapVecWithType(types.T_int32.ToType())
	t.Cleanup(func() { wrong.Free(mp) })
	require.NoError(t, vector.AppendFixedList(wrong, []int32{7, 9}, nil, mp))
	// Wrong typed input keeps the original conservative offset fallback.
	require.Equal(t, []int64{0, 1}, readers[1].filterState.filter.UnSortedSearchFuncWithRange(containers.Vectors{*wrong}, bounds))
	readers[1].reset()
	require.Equal(t, [2]int{1, 1}, cleanups)
	source := &matcherRangeBlockSource{}
	r := &reader{withFilterMixin: readers[1], source: source}
	require.NoError(t, r.Close())
	require.Equal(t, 1, source.closed)
	require.Equal(t, [2]int{1, 1}, cleanups, "Close after Reset cannot repeat owner cleanup")
}

type matcherRangeBlockSource struct {
	engine.DataSource
	deleted []int64
	err     error
	closed  int
}

func (s *matcherRangeBlockSource) Close() { s.closed++ }

func (s *matcherRangeBlockSource) ApplyTombstones(_ context.Context, _ *objectio.Blockid, rows []int64, _ engine.TombstoneApplyPolicy) ([]int64, error) {
	if s.err != nil {
		return nil, s.err
	}
	return slices.DeleteFunc(rows, func(row int64) bool { return slices.Contains(s.deleted, row) }), nil
}

func TestIssue29322MatcherRangePersistedConsumer(t *testing.T) {
	ctx := context.Background()
	mp := mpool.MustNewZero()
	t.Cleanup(func() { require.Zero(t, mp.CurrNB()); mpool.DeleteMPool(mp) })
	capacity := toml.ByteSize(1 << 20)
	fs, err := fileservice.NewLocalFS2(ctx, defines.SharedFileServiceName, t.TempDir(), fileservice.CacheConfig{MemoryCapacity: &capacity}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { fs.Close(ctx) })
	fs.SetAsyncUpdate(false)
	input := batch.NewWithSize(2)
	input.Vecs[0] = matcherBenchVector(t, mp, []uint64{1000, 1001, 1002, 1003, 1004})
	input.Vecs[1] = matcherBenchVector(t, mp, []uint64{19, 3, 13, 3, 8})
	input.SetRowCount(5)
	writer := ioutil.ConstructWriter(0, []uint16{2, 5}, -1, false, false, fs)
	_, err = writer.WriteBatch(input)
	require.NoError(t, err)
	_, _, err = writer.Sync(ctx)
	require.NoError(t, err)
	stats := writer.GetObjectStats()
	info := stats.ConstructBlockInfo(0)
	columns, columnTypes := []uint16{5}, []types.Type{types.T_uint64.ToType()}
	for _, tc := range []struct {
		name    string
		values  []uint64
		deleted []int64
		want    []uint64
		err     error
	}{
		{"range subset", []uint64{0, 3, 9, 19, 999}, nil, []uint64{19, 3, 3}, nil},
		{"duplicates", []uint64{3, 3, 19}, nil, []uint64{19, 3, 3}, nil},
		{"no overlap", []uint64{0, 999}, nil, nil, nil},
		{"no reduction", []uint64{3, 8, 19}, nil, []uint64{19, 3, 3, 8}, nil},
		{"unordered candidates", []uint64{999, 19, 3}, nil, []uint64{19, 3, 3}, nil},
		{"deletes", []uint64{3, 19}, []int64{0, 3}, []uint64{3}, nil},
		{"tombstone error", []uint64{3, 19}, nil, nil, errors.New("range tombstone sentinel")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := matcherBenchVector(t, mp, tc.values)
			filter, err := ConstructBlockPKFilter(true, BasePKFilter{Valid: true, Op: function.IN, Oid: types.T_uint64, Vec: candidate}, nil)
			require.NoError(t, err)
			require.Nil(t, filter.UnSortedSearchFuncWithRange, "public constructor must leave the experiment off")
			t.Cleanup(func() {
				if filter.Cleanup != nil {
					filter.Cleanup()
				}
			})
			calls := 0
			search := matcherBenchRangeSearch(vector.MustFixedColWithTypeCheck[uint64](candidate), filter.UnSortedSearchFunc)
			filter.UnSortedSearchFuncWithRange = func(v containers.Vectors, bounds objectio.Uint64Range) []int64 {
				calls++
				require.Equal(t, objectio.Uint64Range{Min: 3, Max: 19, Valid: true}, bounds)
				return search(v, bounds)
			}
			output := batch.NewWithSize(1)
			output.Vecs[0] = vector.NewOffHeapVecWithType(columnTypes[0])
			t.Cleanup(func() { output.Clean(mp) })
			cache := containers.NewVectors(2)
			t.Cleanup(func() { cache.Free(mp) })
			source := &matcherRangeBlockSource{deleted: tc.deleted, err: tc.err}
			err = blockio.BlockDataRead(ctx, &info, source, columns, columnTypes, -1, timestamp.Timestamp{}, columns, columnTypes, filter, nil, 0, "range-consumer", output, cache, mp, fs)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, calls)
			require.Equal(t, len(tc.want), output.RowCount())
			if len(tc.want) > 0 {
				require.Equal(t, tc.want, vector.MustFixedColWithTypeCheck[uint64](output.Vecs[0]))
			}
		})
	}
	for _, mode := range []string{"missing object", "cancelled read", "appendable read failure", "real PK guard"} {
		t.Run(mode, func(t *testing.T) {
			readCtx := ctx
			readInfo := info
			if mode != "real PK guard" {
				readInfo.SetMetaLocation(objectio.BuildLocation(objectio.NewRandomLocation(0, 5).Name(), info.MetaLocation().Extent(), 5, 0))
			}
			if mode == "cancelled read" {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				readCtx = cancelCtx
			}
			if mode == "appendable read failure" {
				readInfo.ObjectFlags |= objectio.ObjectFlag_Appendable
			}
			candidate := matcherBenchVector(t, mp, []uint64{3, 19})
			filter, err := ConstructBlockPKFilter(true, BasePKFilter{Valid: true, Op: function.IN, Oid: types.T_uint64, Vec: candidate}, nil)
			require.NoError(t, err)
			t.Cleanup(func() {
				if filter.Cleanup != nil {
					filter.Cleanup()
				}
			})
			if mode == "real PK guard" {
				filter.HasFakePK = false
			}
			var baselineErr error
			for trial := 0; trial < 2; trial++ {
				if trial == 1 {
					filter.UnSortedSearchFuncWithRange = func(containers.Vectors, objectio.Uint64Range) []int64 {
						t.Fatal("unsupported domain or failed load must not invoke the range callback")
						return nil
					}
				}
				output := batch.NewWithSize(1)
				output.Vecs[0] = vector.NewOffHeapVecWithType(columnTypes[0])
				cache := containers.NewVectors(2)
				err = blockio.BlockDataRead(readCtx, &readInfo, &matcherRangeBlockSource{}, columns, columnTypes, -1, timestamp.Timestamp{}, columns, columnTypes, filter, nil, 0, "range-control", output, cache, mp, fs)
				if mode == "real PK guard" {
					require.NoError(t, err)
					require.Equal(t, []uint64{19, 3, 3}, vector.MustFixedColWithTypeCheck[uint64](output.Vecs[0]))
				} else {
					require.Error(t, err)
					if trial == 0 {
						baselineErr = err
					} else {
						require.Equal(t, baselineErr.Error(), err.Error())
					}
				}
				output.Clean(mp)
				cache.Free(mp)
			}
		})
	}
}
