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

package util

import (
	"fmt"
	"testing"

	"github.com/matrixorigin/matrixone/pkg/common/mpool"
	"github.com/matrixorigin/matrixone/pkg/container/batch"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/testutil"
	"github.com/stretchr/testify/require"
)

func TestCopyBatchRawOnHeapDeepCopy(t *testing.T) {
	proc := testutil.NewProcess(t)
	mp := proc.Mp()
	src := batch.NewWithSize(4)
	defer src.Clean(mp)
	src.Attrs = []string{"fixed", "varlen", "const", "const_null"}

	fixed := vector.NewVec(types.T_int64.ToType())
	require.NoError(t, vector.AppendFixedList(fixed, []int64{11, 22, 33}, []bool{false, true, false}, mp))
	fixed.GetGrouping().Add(2)
	fixed.GetNulls().Add(9)
	fixed.GetGrouping().Add(9)
	fixed.SetSorted(true)

	varlen := vector.NewVec(types.T_varchar.ToType())
	require.NoError(t, vector.AppendBytesList(
		varlen,
		[][]byte{[]byte("alpha"), []byte("a value long enough to use the area"), []byte("omega")},
		[]bool{false, false, true},
		mp,
	))
	varlen.GetGrouping().Add(1)

	constVec, err := vector.NewConstBytes(
		types.T_varchar.ToType(),
		[]byte("a repeated value long enough to use the area"),
		3,
		mp,
	)
	require.NoError(t, err)
	constNull := vector.NewConstNull(types.T_int32.ToType(), 3, mp)

	src.SetVector(0, fixed)
	src.SetVector(1, varlen)
	src.SetVector(2, constVec)
	src.SetVector(3, constNull)
	src.SetRowCount(3)

	got, err := CopyBatch(src, proc)
	require.NoError(t, err)
	defer got.Clean(mp)
	want, err := copyBatchWithUnionAll(src, mp)
	require.NoError(t, err)
	defer want.Clean(mp)
	require.Equal(t, src.Attrs, got.Attrs)
	require.Equal(t, src.RowCount(), got.RowCount())
	gotBinary, err := got.MarshalBinary()
	require.NoError(t, err)
	wantBinary, err := want.MarshalBinary()
	require.NoError(t, err)
	require.Equal(t, wantBinary, gotBinary)

	for _, vec := range got.Vecs {
		require.False(t, vec.IsConst())
		require.Equal(t, 3, vec.Length())
	}
	require.False(t, got.Vecs[0].GetSorted())
	require.Equal(t, []int64{11, 0, 33}, vector.MustFixedColNoTypeCheck[int64](got.Vecs[0]))
	require.True(t, got.Vecs[0].GetNulls().Contains(1))
	require.True(t, got.Vecs[0].GetGrouping().Contains(2))
	require.False(t, got.Vecs[0].GetNulls().Contains(9))
	require.False(t, got.Vecs[0].GetGrouping().Contains(9))
	require.Equal(t, "a value long enough to use the area", string(got.Vecs[1].GetBytesAt(1)))
	require.True(t, got.Vecs[1].GetNulls().Contains(2))
	require.True(t, got.Vecs[1].GetGrouping().Contains(1))
	for i := 0; i < got.RowCount(); i++ {
		require.Equal(t, "a repeated value long enough to use the area", string(got.Vecs[2].GetBytesAt(i)))
		require.True(t, got.Vecs[3].GetNulls().Contains(uint64(i)))
	}

	require.NoError(t, vector.SetFixedAtNoTypeCheck(fixed, 0, int64(99)))
	require.NoError(t, vector.SetBytesAt(varlen, 1, []byte("changed source value"), mp))
	require.Equal(t, int64(11), vector.GetFixedAtNoTypeCheck[int64](got.Vecs[0], 0))
	require.Equal(t, "a value long enough to use the area", string(got.Vecs[1].GetBytesAt(1)))

	src.Clean(mp)
	got.Clean(mp)
	want.Clean(mp)
	require.Equal(t, int64(0), mp.CurrNB())
}

func TestDupToFlatRejectsUnsupportedType(t *testing.T) {
	proc := testutil.NewProcess(t)
	vec := vector.NewVec(types.T_any.ToType())
	require.Panics(t, func() {
		_, _ = vec.DupToFlat(proc.Mp())
	})
}

func BenchmarkCopyBatchFlatVarlena(b *testing.B) {
	proc := testutil.NewProcess(b)
	mp := proc.Mp()
	src := batch.NewWithSize(10)
	defer src.Clean(mp)
	for col := range src.Vecs {
		vec := vector.NewVec(types.T_varchar.ToType())
		for row := 0; row < 128; row++ {
			require.NoError(b, vector.AppendBytes(vec, []byte(fmt.Sprintf("column-%d-row-%d-value-long-enough-for-area", col, row)), false, mp))
		}
		src.SetVector(int32(col), vec)
	}
	src.SetRowCount(128)

	bench := func(b *testing.B, copyFn func(*batch.Batch, *mpool.MPool) (*batch.Batch, error)) {
		b.Helper()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			got, err := copyFn(src, mp)
			if err != nil {
				b.Fatal(err)
			}
			got.Clean(mp)
		}
	}

	b.Run("union-all", func(b *testing.B) {
		bench(b, copyBatchWithUnionAll)
	})
	b.Run("raw-on-heap", func(b *testing.B) {
		bench(b, func(src *batch.Batch, _ *mpool.MPool) (*batch.Batch, error) {
			return CopyBatch(src, proc)
		})
	})
}

func copyBatchWithUnionAll(src *batch.Batch, mp *mpool.MPool) (*batch.Batch, error) {
	dst := batch.NewWithSize(len(src.Vecs))
	dst.Attrs = append(dst.Attrs, src.Attrs...)
	for i, srcVec := range src.Vecs {
		vec := vector.NewVec(*srcVec.GetType())
		if err := vector.GetUnionAllFunction(*srcVec.GetType(), mp)(vec, srcVec); err != nil {
			vec.Free(mp)
			dst.Clean(mp)
			return nil, err
		}
		dst.SetVector(int32(i), vec)
	}
	dst.SetRowCount(src.RowCount())
	return dst, nil
}
