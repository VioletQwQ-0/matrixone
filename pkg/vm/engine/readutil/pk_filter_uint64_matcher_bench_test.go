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
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/matrixorigin/matrixone/pkg/catalog"
	"github.com/matrixorigin/matrixone/pkg/common/mpool"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/fileservice"
	"github.com/matrixorigin/matrixone/pkg/pb/plan"
	"github.com/matrixorigin/matrixone/pkg/pb/timestamp"
	"github.com/matrixorigin/matrixone/pkg/sql/plan/function"
	"github.com/matrixorigin/matrixone/pkg/vm/engine"
	"github.com/matrixorigin/matrixone/pkg/vm/engine/tae/containers"
	"github.com/stretchr/testify/require"
)

// These factories are experiment-only. The production generic function is the
// baseline; none of these prototypes changes the product's dispatch or ownership.
type matcherBenchFactory func([]uint64) func(*vector.Vector) []int64

func matcherBenchLinear(values []uint64) func(*vector.Vector) []int64 {
	return func(v *vector.Vector) []int64 {
		var offsets []int64
		for i, row := range vector.MustFixedColNoTypeCheck[uint64](v) {
			for _, value := range values {
				if row == value {
					offsets = append(offsets, int64(i))
					break
				}
			}
		}
		return offsets
	}
}

func matcherBenchFactoryFor(algorithm string) matcherBenchFactory {
	if algorithm == "hybrid" {
		threshold, err := strconv.Atoi(os.Getenv("MATCHER_BENCH_THRESHOLD"))
		small, large := os.Getenv("MATCHER_BENCH_SMALL"), os.Getenv("MATCHER_BENCH_LARGE")
		if err != nil || threshold < 2 || threshold > 4096 ||
			(small != "generic" && small != "linear") || (large != "binary" && large != "hash") {
			panic("invalid test-only matcher strategy")
		}
		left, right := matcherBenchFactoryFor(small), matcherBenchFactoryFor(large)
		return func(values []uint64) func(*vector.Vector) []int64 {
			if len(values) < threshold {
				return left(values)
			}
			return right(values)
		}
	}

	if algorithm == "generic" {
		return func(values []uint64) func(*vector.Vector) []int64 {
			return vector.OrderedLinearSearchOffsetByValFactory(values, nil)
		}
	}
	return func(values []uint64) func(*vector.Vector) []int64 {
		// GetSorted is not evidence that the borrowed candidate data is ordered.
		for i := 1; i < len(values); i++ {
			if values[i] < values[i-1] {
				return vector.OrderedLinearSearchOffsetByValFactory(values, nil)
			}
		}
		if len(values) == 0 {
			return func(*vector.Vector) []int64 { return nil }
		}
		switch algorithm {
		case "linear":
			return matcherBenchLinear(values)
		case "binary":
			return func(v *vector.Vector) []int64 {
				var offsets []int64
				for i, row := range vector.MustFixedColNoTypeCheck[uint64](v) {
					low, high := 0, len(values)
					for low < high {
						mid := low + (high-low)/2
						if values[mid] < row {
							low = mid + 1
						} else {
							high = mid
						}
					}
					if low < len(values) && values[low] == row {
						offsets = append(offsets, int64(i))
					}
				}
				return offsets
			}
		case "hash":
			members := make(map[uint64]struct{}, len(values))
			for _, value := range values {
				members[value] = struct{}{}
			}
			return func(v *vector.Vector) []int64 {
				var offsets []int64
				for i, row := range vector.MustFixedColNoTypeCheck[uint64](v) {
					if _, ok := members[row]; ok {
						offsets = append(offsets, int64(i))
					}
				}
				return offsets
			}
		default:
			panic("unknown matcher experiment algorithm")
		}
	}
}

var matcherBenchAlgorithms = []string{"generic", "linear", "binary", "hash"}

func matcherBenchVector(t testing.TB, mp *mpool.MPool, values []uint64) *vector.Vector {
	t.Helper()
	v := vector.NewVec(types.T_uint64.ToType())
	t.Cleanup(func() { v.Free(mp) })
	require.NoError(t, vector.AppendFixedList(v, values, nil, mp))
	return v
}

// The oracle uses standard-library membership, not a tested matcher factory.
func matcherBenchOracle(values, rows []uint64) []int64 {
	var offsets []int64
	for i, row := range rows {
		if slices.Contains(values, row) {
			offsets = append(offsets, int64(i))
		}
	}
	return offsets
}

func TestIssue29322MatcherAlgorithms(t *testing.T) {
	mp := mpool.MustNewZero()
	t.Cleanup(func() { mpool.DeleteMPool(mp) })
	rows := []uint64{math.MaxUint64, 3, 0, 2, 3, 1, math.MaxUint64 - 1}
	v := matcherBenchVector(t, mp, rows)
	for _, values := range [][]uint64{nil, {0}, {math.MaxUint64}, {0, 1, 2, 3, 3, math.MaxUint64}, {3, 1, 2}, {3, 3, 3}} {
		for _, algorithm := range matcherBenchAlgorithms {
			before := slices.Clone(values)
			got := matcherBenchFactoryFor(algorithm)(values)(v)
			require.Equal(t, matcherBenchOracle(values, rows), got, "%s values=%v", algorithm, values)
			require.Equal(t, before, values, "shared candidates must remain immutable")
		}
	}
	rng := rand.New(rand.NewPCG(42, 42))
	for _, m := range []int{0, 1, 2, 4, 5, 7, 8, 9, 11, 13, 16, 31, 32, 33, 64, 256, 1024, 4096} {
		values := make([]uint64, m)
		for i := range values {
			values[i] = uint64(i * 3)
		}
		rows := make([]uint64, 257)
		for i := range rows {
			rows[i] = rng.Uint64N(uint64(max(1, m*4)))
		}
		v := matcherBenchVector(t, mp, rows)
		for _, algorithm := range matcherBenchAlgorithms {
			require.Equal(t, matcherBenchOracle(values, rows), matcherBenchFactoryFor(algorithm)(values)(v), "%s M=%d", algorithm, m)
		}
	}
}

func TestIssue29322MatcherBorrowedCandidates(t *testing.T) {
	mp := mpool.MustNewZero()
	t.Cleanup(func() { mpool.DeleteMPool(mp) })
	rows := []uint64{3, 0, 2, 1, 3}
	input := matcherBenchVector(t, mp, rows)
	for _, sortedFlag := range []bool{false, true} {
		for _, values := range [][]uint64{{1, 2, 3}, {3, 1, 2}} {
			v := matcherBenchVector(t, mp, values)
			v.SetSorted(sortedFlag)
			wire, err := v.MarshalBinary()
			require.NoError(t, err)
			before := slices.Clone(wire)
			decoded, err := unmarshalPKInVector(wire)
			require.NoError(t, err)
			for _, candidates := range []*vector.Vector{v, decoded} {
				for _, algorithm := range matcherBenchAlgorithms {
					var factory matcherBenchFactory
					if algorithm != "generic" {
						factory = matcherBenchFactoryFor(algorithm)
					}
					filter, err := constructBlockPKFilterForMatcher(true, BasePKFilter{Valid: true, Op: function.IN, Oid: types.T_uint64, Vec: candidates}, nil, factory)
					require.NoError(t, err)
					require.Equal(t, matcherBenchOracle(values, rows), filter.UnSortedSearchFunc(containers.Vectors{*input}))
				}
			}
			require.Equal(t, before, wire)
			require.Equal(t, values, vector.MustFixedColNoTypeCheck[uint64](v))
		}
	}
}

func TestIssue29322MatcherReaderReset(t *testing.T) {
	mp := mpool.MustNewZero()
	t.Cleanup(func() { mpool.DeleteMPool(mp) })
	input := matcherBenchVector(t, mp, []uint64{3, 1, 2})
	for _, algorithm := range matcherBenchAlgorithms {
		var readers [2]withFilterMixin
		cleanups := [2]int{}
		for i := range readers {
			candidate := matcherBenchVector(t, mp, []uint64{uint64(i + 1)})
			base := BasePKFilter{Valid: true, Op: function.IN, Oid: types.T_uint64, Vec: candidate,
				cleanup: &basePKFilterCleanup{fns: []func(){func() { cleanups[i]++ }}}}
			var selected matcherBenchFactory
			if algorithm != "generic" {
				selected = matcherBenchFactoryFor(algorithm)
			}
			filter, err := constructBlockPKFilterForMatcher(true, base, nil, selected)
			require.NoError(t, err)
			readers[i].filterState.filter = filter
		}
		require.Equal(t, []int64{1}, readers[0].filterState.filter.UnSortedSearchFunc(containers.Vectors{*input}))
		require.Equal(t, []int64{2}, readers[1].filterState.filter.UnSortedSearchFunc(containers.Vectors{*input}))
		readers[0].reset()
		readers[0].reset()
		require.Nil(t, readers[0].filterState.filter.UnSortedSearchFunc)
		require.Equal(t, [2]int{1, 0}, cleanups)
		require.Equal(t, []int64{2}, readers[1].filterState.filter.UnSortedSearchFunc(containers.Vectors{*input}))
		readers[1].reset()
		require.Equal(t, [2]int{1, 1}, cleanups)
	}
}

func TestIssue29322MatcherThresholds(t *testing.T) {
	mp := mpool.MustNewZero()
	t.Cleanup(func() { mpool.DeleteMPool(mp) })
	for _, small := range []string{"generic", "linear"} {
		for _, large := range []string{"binary", "hash"} {
			for _, threshold := range []int{2, 8, 13, 32} {
				t.Setenv("MATCHER_BENCH_SMALL", small)
				t.Setenv("MATCHER_BENCH_LARGE", large)
				t.Setenv("MATCHER_BENCH_THRESHOLD", strconv.Itoa(threshold))
				factory := matcherBenchFactoryFor("hybrid")
				for _, m := range []int{threshold - 1, threshold, threshold + 1} {
					values, vectors := matcherBenchInputs(t, mp, matcherBenchCase{m, 64, 1, "dense"})
					require.Equal(t, matcherBenchOracle(values, vector.MustFixedColNoTypeCheck[uint64](vectors[0])), factory(values)(vectors[0]))
				}
			}
		}
	}
}

func TestIssue29322MatcherDispatchBoundary(t *testing.T) {
	mp := mpool.MustNewZero()
	t.Cleanup(func() { mpool.DeleteMPool(mp) })
	candidate := matcherBenchVector(t, mp, []uint64{1, 2, 3})
	base := BasePKFilter{Valid: true, Op: function.IN, Oid: types.T_uint64, Vec: candidate}
	called := 0
	factory := func(values []uint64) func(*vector.Vector) []int64 {
		called++
		return matcherBenchFactoryFor("binary")(values)
	}
	_, err := constructBlockPKFilterForMatcher(false, base, nil, factory)
	require.NoError(t, err)
	require.Zero(t, called, "real-PK construction must not use the experiment")
	filter, err := constructBlockPKFilterForMatcher(true, base, nil, factory)
	require.NoError(t, err)
	require.Equal(t, 1, called)
	wrongType := vector.NewVec(types.T_int64.ToType())
	t.Cleanup(func() { wrongType.Free(mp) })
	require.NoError(t, vector.AppendFixedList(wrongType, []int64{1, 2, 3}, nil, mp))
	require.Equal(t, []int64{0, 1, 2}, filter.UnSortedSearchFunc(containers.Vectors{*wrongType}), "mismatched PK type must fail open")
}

type matcherBenchCase struct {
	m, n, blocks int
	hits         string
}

func (c matcherBenchCase) name() string {
	return fmt.Sprintf("m%d_n%d_b%d_%s", c.m, c.n, c.blocks, c.hits)
}

func matcherBenchCases() []matcherBenchCase {
	var cases []matcherBenchCase
	for m := 2; m <= 32; m++ {
		cases = append(cases, matcherBenchCase{m, 8192, m, "one"})
	}
	for _, m := range []int{0, 1, 2, 5, 13, 64, 256, 1024, 4096} {
		for _, n := range []int{0, 1, 64, 1024, 8192} {
			cases = append(cases, matcherBenchCase{m, n, 1, "miss"})
		}
		cases = append(cases, matcherBenchCase{m, 8192, 1, "dense"}, matcherBenchCase{m, 8192, 32, "one"})
	}
	return cases
}

// Identical deterministic inputs for every algorithm. Each primary block has
// exactly one candidate hit; remaining keys exceed every candidate value.
func matcherBenchInputs(t testing.TB, mp *mpool.MPool, c matcherBenchCase) ([]uint64, []*vector.Vector) {
	values := make([]uint64, c.m)
	for i := range values {
		values[i] = uint64(i*2 + 1)
	}
	vectors := make([]*vector.Vector, c.blocks)
	for block := range vectors {
		rows := make([]uint64, c.n)
		for i := range rows {
			rows[i] = 1<<63 + uint64((i*4051)%max(1, c.n))
			if c.hits == "dense" && c.m > 0 {
				rows[i] = values[i%c.m]
			}
		}
		if c.hits == "one" && c.n > 0 && c.m > 0 {
			rows[(block*127)%c.n] = values[block%c.m]
		}
		vectors[block] = matcherBenchVector(t, mp, rows)
	}
	return values, vectors
}

type matcherBenchReaderSource struct {
	engine.DataSource
	closed bool
}

func (s *matcherBenchReaderSource) Close() { s.closed = true }

func matcherBenchReaderPlan(wire []byte, count int) (*plan.TableDef, *plan.Expr) {
	typ := plan.Type{Id: int32(types.T_uint64)}
	name := catalog.FakePrimaryKeyColName
	table := &plan.TableDef{Name: "uint64_matcher_bench", Cols: []*plan.ColDef{{Name: name, Typ: typ}},
		Name2ColIndex: map[string]int32{name: 0}, Pkey: &plan.PrimaryKeyDef{PkeyColName: name, Names: []string{name}}}
	expr := &plan.Expr{Expr: &plan.Expr_F{F: &plan.Function{Func: &plan.ObjectRef{ObjName: "in"}, Args: []*plan.Expr{
		{Typ: typ, Expr: &plan.Expr_Col{Col: &plan.ColRef{Name: name}}},
		{Typ: typ, Expr: &plan.Expr_Vec{Vec: &plan.LiteralVec{Len: int32(count), Data: wire}}},
	}}}}
	return table, expr
}

var matcherBenchSink int

func matcherBenchBenchmarkAlgorithms() []string {
	if os.Getenv("MATCHER_BENCH_LARGE") != "" {
		return []string{"generic", "hybrid"}
	}
	return matcherBenchAlgorithms
}

func BenchmarkIssue29322Matcher(b *testing.B) {
	for _, algorithm := range matcherBenchBenchmarkAlgorithms() {
		b.Run(algorithm, func(b *testing.B) {
			factory := matcherBenchFactoryFor(algorithm)
			for _, c := range matcherBenchCases() {
				b.Run(c.name(), func(b *testing.B) {
					mp := mpool.MustNewZero()
					b.Cleanup(func() { mpool.DeleteMPool(mp) })
					values, vectors := matcherBenchInputs(b, mp, c)
					search := factory(values)
					for _, v := range vectors {
						require.Equal(b, matcherBenchOracle(values, vector.MustFixedColNoTypeCheck[uint64](v)), search(v))
					}
					b.Run("search", func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							for _, v := range vectors {
								matcherBenchSink = len(search(v))
							}
						}
					})
					candidate := matcherBenchVector(b, mp, values)
					candidate.SetSorted(true)
					wire, err := candidate.MarshalBinary()
					require.NoError(b, err)
					table, expr := matcherBenchReaderPlan(wire, c.m)
					packer := types.NewPacker()
					b.Cleanup(packer.Close)
					pool := fileservice.NewPool(1, func() *types.Packer { return packer },
						func(p *types.Packer) { p.Reset() }, func(p *types.Packer) { p.Close() })
					b.Run("reader", func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							var selected matcherBenchFactory
							if algorithm != "generic" {
								selected = factory
							}
							source := &matcherBenchReaderSource{}
							reader, err := newReaderForMatcher(context.Background(), mp, pool, nil, table,
								timestamp.Timestamp{}, expr, source, 0, engine.FilterHint{}, selected)
							if err != nil {
								b.Fatal(err)
							}
							for _, input := range vectors {
								matcherBenchSink = len(reader.filterState.filter.UnSortedSearchFunc(containers.Vectors{*input}))
							}
							if err := reader.Close(); err != nil {
								b.Fatal(err)
							}
							if !source.closed {
								b.Fatal("reader failed to close its source")
							}
						}
					})
				})
			}
		})
	}
}
