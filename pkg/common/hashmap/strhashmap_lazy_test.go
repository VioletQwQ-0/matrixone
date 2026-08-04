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

package hashmap

import (
	"fmt"
	"sync"
	"testing"

	"github.com/matrixorigin/matrixone/pkg/common/mpool"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/stretchr/testify/require"
)

func TestStrHashMapIteratorLazyBuffers(t *testing.T) {
	shapes := []struct {
		name  string
		types []types.Type
	}{
		{name: "fixed", types: []types.Type{types.T_int64.ToType()}},
		{name: "varlen", types: []types.Type{types.T_varchar.ToType()}},
		{name: "composite", types: []types.Type{types.T_int32.ToType(), types.T_varchar.ToType()}},
	}

	for _, hasNull := range []bool{false, true} {
		for _, shape := range shapes {
			for _, count := range []int{0, 1, 2, 8, 255, 256} {
				t.Run(fmt.Sprintf("has-null-%t/%s/rows-%d", hasNull, shape.name, count), func(t *testing.T) {
					m := mpool.MustNewZero()
					hashMap, err := NewStrHashMap(hasNull, m)
					require.NoError(t, err)
					defer hashMap.Free()

					rows := max(count, 1)
					var vecs []*vector.Vector
					if hasNull {
						vecs = newVectorsWithNull(shape.types, false, rows, m)
					} else {
						vecs = newVectors(shape.types, false, rows, m)
					}
					defer func() {
						for _, vec := range vecs {
							vec.Free(m)
						}
					}()

					itr := hashMap.NewIterator().(*strHashmapIterator)
					assertStrIteratorCapacity(t, itr, 0)

					values, zValues, err := itr.Insert(0, count, vecs)
					require.NoError(t, err)
					require.Len(t, values, count)
					require.Len(t, zValues, count)
					insertedValues := append([]uint64(nil), values...)
					insertedZValues := append([]int64(nil), zValues...)

					values, zValues, err = itr.Find(0, count, vecs)
					require.NoError(t, err)
					require.Equal(t, insertedValues, values)
					require.Equal(t, insertedZValues, zValues)
					assertStrIteratorCapacity(t, itr, count)
				})
			}
		}
	}
}

func TestStrHashMapIteratorLazyBufferReuse(t *testing.T) {
	m := mpool.MustNewZero()
	first, err := NewStrHashMap(false, m)
	require.NoError(t, err)
	defer first.Free()
	second, err := NewStrHashMap(true, m)
	require.NoError(t, err)
	defer second.Free()

	vecs := newVectors([]types.Type{types.T_varchar.ToType()}, false, UnitLimit, m)
	defer vecs[0].Free(m)
	itr := first.NewIterator().(*strHashmapIterator)

	for _, tc := range []struct {
		count   int
		wantCap int
	}{
		{count: 0, wantCap: 0},
		{count: 1, wantCap: 1},
		{count: 8, wantCap: 8},
		{count: 2, wantCap: 8},
		{count: 256, wantCap: 256},
		{count: 0, wantCap: 256},
	} {
		_, _, err = itr.Find(0, tc.count, vecs)
		require.NoError(t, err)
		assertStrIteratorCapacity(t, itr, tc.wantCap)
		require.Len(t, itr.keys, tc.count)
	}

	IteratorChangeOwner(itr, second)
	_, _, err = itr.Find(0, 1, vecs)
	require.NoError(t, err)
	assertStrIteratorCapacity(t, itr, UnitLimit)
}

func TestStrHashMapIteratorLazyErrorAndDetectDup(t *testing.T) {
	m := mpool.MustNewZero()
	hashMap, err := NewStrHashMap(false, m)
	require.NoError(t, err)
	defer hashMap.Free()

	vec := newVector(1, types.T_varchar.ToType(), m, false, nil)
	defer vec.Free(m)
	itr := hashMap.NewIterator().(*strHashmapIterator)

	_, _, err = itr.Find(0, 1, []*vector.Vector{vec})
	require.NoError(t, err)
	assertStrIteratorCapacity(t, itr, 1)

	_, _, err = itr.Find(0, UnitLimit+1, []*vector.Vector{vec})
	require.ErrorIs(t, err, mpool.ErrAllocationAccountInvalid)
	assertStrIteratorCapacity(t, itr, 1)

	_, _, err = itr.Find(0, 1, []*vector.Vector{nil})
	require.ErrorIs(t, err, mpool.ErrAllocationAccountInvalid)
	assertStrIteratorCapacity(t, itr, 1)

	newKey, err := itr.DetectDup([]*vector.Vector{vec}, 0)
	require.NoError(t, err)
	require.True(t, newKey)
	newKey, err = itr.DetectDup([]*vector.Vector{vec}, 0)
	require.NoError(t, err)
	require.False(t, newKey)
	assertStrIteratorCapacity(t, itr, 1)
}

func TestStrHashMapIteratorLazyGrouping(t *testing.T) {
	m := mpool.MustNewZero()
	hashMap, err := NewStrHashMap(false, m)
	require.NoError(t, err)
	require.NoError(t, hashMap.SetGroupingAware())
	defer hashMap.Free()

	vec := newVector(8, types.T_int32.ToType(), m, false, nil)
	defer vec.Free(m)
	vec.GetGrouping().Add(0)
	vec.GetGrouping().Add(7)

	itr := hashMap.NewIterator().(*strHashmapIterator)
	insertedValues, insertedZValues, err := itr.Insert(0, 8, []*vector.Vector{vec})
	require.NoError(t, err)
	values, zValues, err := itr.Find(0, 8, []*vector.Vector{vec})
	require.NoError(t, err)
	require.Equal(t, insertedValues, values)
	require.Equal(t, insertedZValues, zValues)
	assertStrIteratorCapacity(t, itr, 8)
}

func TestStrHashMapIteratorConcurrentFindWithIndependentIterators(t *testing.T) {
	const (
		rows    = 8
		workers = 32
		repeats = 100
	)
	m := mpool.MustNewZero()
	hashMap, err := NewStrHashMap(false, m)
	require.NoError(t, err)
	defer hashMap.Free()
	vecs := newVectors([]types.Type{types.T_int32.ToType(), types.T_varchar.ToType()}, false, rows, m)
	defer func() {
		for _, vec := range vecs {
			vec.Free(m)
		}
	}()
	_, _, err = hashMap.NewIterator().Insert(0, rows, vecs)
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			itr := hashMap.NewIterator()
			for range repeats {
				values, zValues, err := itr.Find(0, rows, vecs)
				if err != nil {
					errs <- err
					return
				}
				if len(values) != rows || len(zValues) != rows {
					errs <- fmt.Errorf("unexpected result lengths %d/%d", len(values), len(zValues))
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func assertStrIteratorCapacity(t *testing.T, itr *strHashmapIterator, want int) {
	t.Helper()
	require.Equal(t, want, cap(itr.keys))
	require.Equal(t, want, cap(itr.values))
	require.Equal(t, want, cap(itr.zValues))
	require.Equal(t, want, cap(itr.strHashStates))
}

var benchmarkStrIterator Iterator

func BenchmarkNewStrHashMapIterator(b *testing.B) {
	m := mpool.MustNewZero()
	hashMap, err := NewStrHashMap(false, m)
	if err != nil {
		b.Fatal(err)
	}
	defer hashMap.Free()

	b.Run("eager-unit-limit", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkStrIterator = newEagerStrHashMapIterator(hashMap)
		}
	})
	b.Run("lazy", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkStrIterator = hashMap.NewIterator()
		}
	})
}

func BenchmarkStrHashMapIteratorFirstFind(b *testing.B) {
	shapes := []struct {
		name  string
		types []types.Type
	}{
		{name: "fixed", types: []types.Type{types.T_int64.ToType()}},
		{name: "varlen", types: []types.Type{types.T_varchar.ToType()}},
		{name: "composite", types: []types.Type{types.T_int32.ToType(), types.T_varchar.ToType()}},
	}
	for _, shape := range shapes {
		for _, count := range []int{1, 2, 8, 16, 64, 256} {
			b.Run(fmt.Sprintf("%s/rows-%d", shape.name, count), func(b *testing.B) {
				m := mpool.MustNewZero()
				hashMap, err := NewStrHashMap(false, m)
				if err != nil {
					b.Fatal(err)
				}
				defer hashMap.Free()
				vecs := newVectors(shape.types, false, count, m)
				defer func() {
					for _, vec := range vecs {
						vec.Free(m)
					}
				}()
				if _, _, err := hashMap.NewIterator().Insert(0, count, vecs); err != nil {
					b.Fatal(err)
				}

				b.Run("eager-unit-limit", func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						itr := newEagerStrHashMapIterator(hashMap)
						if _, _, err := itr.Find(0, count, vecs); err != nil {
							b.Fatal(err)
						}
						benchmarkStrIterator = itr
					}
				})
				b.Run("lazy", func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						itr := hashMap.NewIterator()
						if _, _, err := itr.Find(0, count, vecs); err != nil {
							b.Fatal(err)
						}
						benchmarkStrIterator = itr
					}
				})
			})
		}
	}
}

func BenchmarkStrHashMapIteratorParallelFind(b *testing.B) {
	for _, count := range []int{1, 8, 64, 256} {
		b.Run(fmt.Sprintf("rows-%d", count), func(b *testing.B) {
			m := mpool.MustNewZero()
			hashMap, err := NewStrHashMap(false, m)
			if err != nil {
				b.Fatal(err)
			}
			defer hashMap.Free()
			vecs := newVectors([]types.Type{types.T_int32.ToType(), types.T_varchar.ToType()}, false, count, m)
			defer func() {
				for _, vec := range vecs {
					vec.Free(m)
				}
			}()
			if _, _, err := hashMap.NewIterator().Insert(0, count, vecs); err != nil {
				b.Fatal(err)
			}

			factories := []struct {
				name string
				new  func() Iterator
			}{
				{name: "eager-unit-limit", new: func() Iterator { return newEagerStrHashMapIterator(hashMap) }},
				{name: "lazy", new: hashMap.NewIterator},
			}
			for _, factory := range factories {
				b.Run(factory.name, func(b *testing.B) {
					b.ReportAllocs()
					b.RunParallel(func(pb *testing.PB) {
						for pb.Next() {
							itr := factory.new()
							values, _, err := itr.Find(0, count, vecs)
							if err != nil || len(values) != count {
								b.Fatalf("find failed: values=%d err=%v", len(values), err)
							}
						}
					})
				})
			}
		})
	}
}

func newEagerStrHashMapIterator(hashMap *StrHashMap) Iterator {
	return &strHashmapIterator{
		mp:            hashMap,
		values:        make([]uint64, UnitLimit),
		zValues:       make([]int64, UnitLimit),
		keys:          make([][]byte, UnitLimit),
		strHashStates: make([][3]uint64, UnitLimit),
	}
}
