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

package readutil

import (
	"context"
	"testing"

	"github.com/matrixorigin/matrixone/pkg/common/mpool"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/defines"
	"github.com/matrixorigin/matrixone/pkg/vm/engine/tae/containers"
	"github.com/stretchr/testify/require"
)

func TestReadWorkProbeCountersPreserveOffsets(t *testing.T) {
	mp := mpool.MustNewZero()
	vectors := containers.NewVectors(1)
	defer vectors.Free(mp)
	vectors[0].Reset(types.T_uint64.ToType())
	require.NoError(t, vector.AppendFixedList(&vectors[0], []uint64{8, 3, 8}, nil, mp))
	p := &readWorkProbe{}
	p.beginBlock("block1")
	search := p.wrap(func(containers.Vectors) []int64 { return []int64{0, 2} })
	require.Equal(t, []int64{0, 2}, search(vectors))
	p.endBlock(2, false)
	require.Equal(t, uint64(3), p.Blocks[0].InputRows)
	require.Equal(t, uint64(2), p.Blocks[0].PKMatchRows)
	require.Equal(t, uint64(2), p.Blocks[0].ReturnedRows)
	require.Equal(t, uint64(1), p.Blocks[0].SearchCalls)
	require.Nil(t, p.wrap(nil))
	for range readWorkBlockLimit {
		p.beginBlock("empty")
		p.endBlock(0, true)
	}
	require.Len(t, p.Blocks, readWorkBlockLimit)
	require.True(t, p.Overflow)
	require.True(t, p.Blocks[1].Error)
}

func TestReadWorkProbeOptInAndCardinality(t *testing.T) {
	mp := mpool.MustNewZero()
	vec := vector.NewVec(types.T_uint64.ToType())
	defer vec.Free(mp)
	require.NoError(t, vector.AppendFixedList(vec, []uint64{8, 9}, nil, mp))
	r := &reader{}
	base := BasePKFilter{Valid: true, Oid: types.T_uint64, Vec: vec}
	r.installReadWorkProbe(context.Background(), base)
	require.Nil(t, r.readWorkProbe)
	ctx := context.WithValue(context.Background(), defines.ReadWorkProbeKey{}, defines.ReadWorkProbe{Label: "p01", Execution: "exec1"})
	r.installReadWorkProbe(ctx, base)
	require.Equal(t, []int{2}, r.readWorkProbe.MembershipSizes)
	r.finishReadWorkProbe()
	require.Nil(t, r.readWorkProbe)
	r.finishReadWorkProbe()
}
