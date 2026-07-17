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

package disttae

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matrixorigin/matrixone/pkg/container/batch"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/pb/api"
)

func TestToPBBatchPreservesOwnership(t *testing.T) {
	data1 := make([]byte, types.T_int64.ToType().TypeSize())
	data2 := make([]byte, types.T_int64.ToType().TypeSize())
	bat := batch.NewWithSize(2)
	bat.Attrs = []string{"a", "b"}
	bat.Vecs[0] = vector.NewVecWithData(types.T_int64.ToType(), 1, data1, nil)
	bat.Vecs[1] = vector.NewVecWithData(types.T_int64.ToType(), 1, data2, nil)

	pb, err := toPBBatch(bat)
	require.NoError(t, err)
	require.Len(t, pb.Vecs, 2)
	require.Equal(t, len(pb.Vecs), cap(pb.Vecs))

	bat.Attrs[0] = "changed"
	require.Equal(t, "changed", pb.Attrs[0])
	pb.Vecs[0].Data[0] = 1
	pb.Vecs[1].Data[0] = 2
	require.Equal(t, byte(1), data1[0])
	require.Equal(t, byte(2), data2[0])
}

func TestToPBBatchKeepsNilVecsForEmptyBatch(t *testing.T) {
	pb, err := toPBBatch(new(batch.Batch))
	require.NoError(t, err)
	require.Nil(t, pb.Vecs)
}

func BenchmarkToPBBatchPreallocation(b *testing.B) {
	for _, size := range []int{2, 8, 16, 32} {
		bat := newDisttaeProtoBenchmarkBatch(size)
		b.Run(fmt.Sprintf("vectors=%d/append", size), func(b *testing.B) {
			benchmarkToPBBatch(b, bat, toPBBatchAppend)
		})
		b.Run(fmt.Sprintf("vectors=%d/exact", size), func(b *testing.B) {
			benchmarkToPBBatch(b, bat, toPBBatch)
		})
	}
}

func newDisttaeProtoBenchmarkBatch(size int) *batch.Batch {
	bat := batch.NewWithSize(size)
	for i := range bat.Vecs {
		bat.Vecs[i] = vector.NewVecWithData(
			types.T_int64.ToType(),
			1,
			make([]byte, types.T_int64.ToType().TypeSize()),
			nil)
	}
	return bat
}

func benchmarkToPBBatch(
	b *testing.B,
	bat *batch.Batch,
	convert func(*batch.Batch) (*api.Batch, error),
) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pb, err := convert(bat)
		if err != nil {
			b.Fatal(err)
		}
		if len(pb.Vecs) != len(bat.Vecs) {
			b.Fatalf("converted %d vectors, want %d", len(pb.Vecs), len(bat.Vecs))
		}
	}
}

func toPBBatchAppend(bat *batch.Batch) (*api.Batch, error) {
	rbat := new(api.Batch)
	rbat.Attrs = bat.Attrs
	for _, vec := range bat.Vecs {
		pbVector, err := vector.VectorToProtoVector(vec)
		if err != nil {
			return nil, err
		}
		rbat.Vecs = append(rbat.Vecs, pbVector)
	}
	return rbat, nil
}
