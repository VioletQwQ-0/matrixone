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

package objectio

import (
	"math"
	"testing"

	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/vm/engine/tae/index"
	"github.com/stretchr/testify/require"
)

func uint64RangeFixture(t *testing.T) (ObjectDataMeta, Location) {
	meta := BuildMetaData(2, 3)
	location := NewRandomLocation(1, 3)
	copy(meta.BlockHeader().ShortName()[:], location.ShortName())
	for id := uint32(0); id < 2; id++ {
		block := meta.GetBlockMeta(id)
		block.ColumnMeta(2).setDataType(uint8(types.T_uint64))
		zm := index.NewZM(types.T_uint64, 0)
		require.NoError(t, zm.Update(uint64(7+id)))
		require.NoError(t, zm.Update(uint64(math.MaxUint64)))
		block.ColumnMeta(2).SetZoneMap(zm)
	}
	return meta, location
}

func TestIssue29322Uint64BlockRange(t *testing.T) {
	meta, location := uint64RangeFixture(t)
	before := append([]byte(nil), meta...)
	got := meta.Uint64BlockRange(location, 2)
	require.Equal(t, Uint64Range{Min: 8, Max: math.MaxUint64, Valid: true}, got)
	require.Equal(t, before, []byte(meta))
	// The returned scalars must outlive a recycled metadata view.
	clear(meta)
	require.Equal(t, uint64(8), got.Min)
	require.Equal(t, uint64(math.MaxUint64), got.Max)
}

func TestIssue29322Uint64BlockRangeFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(ObjectDataMeta, Location) (ObjectDataMeta, Location, uint16)
	}{
		{"nil metadata", func(_ ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) { return nil, l, 2 }},
		{"short header", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) { return m[:headerLen-1], l, 2 }},
		{"short location", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) { return m, l[:3], 2 }},
		{"different object", func(m ObjectDataMeta, _ Location) (ObjectDataMeta, Location, uint16) {
			return m, NewRandomLocation(1, 3), 2
		}},
		{"different block", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) { l.SetID(2); return m, l, 2 }},
		{"missing column", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) { return m, l, 3 }},
		{"special column", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) { return m, l, SEQNUM_COMMITTS }},
		{"appendable object", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) {
			m.BlockHeader().SetAppendable(true)
			return m, l, 2
		}},
		{"appendable block", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) {
			m.GetBlockMeta(1).BlockHeader().SetAppendable(true)
			return m, l, 2
		}},
		{"wrong sequence", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) {
			m.GetBlockMeta(1).BlockHeader().SetSequence(0)
			return m, l, 2
		}},
		{"truncated index", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) {
			return m[:m.Length()+blockCountLen], l, 2
		}},
		{"wrong index count", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) {
			m.BlockIndex().SetBlockCount(1)
			return m, l, 2
		}},
		{"invalid offset", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) {
			m.BlockIndex().SetBlockMetaPos(1, math.MaxUint32, 100)
			return m, l, 2
		}},
		{"truncated column", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) {
			off, _ := m.BlockIndex().BlockMetaPos(1)
			m.BlockIndex().SetBlockMetaPos(1, off, headerLen+colMetaLen)
			return m, l, 2
		}},
		{"wrong column type", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) {
			m.GetBlockMeta(1).ColumnMeta(2).setDataType(uint8(types.T_int64))
			return m, l, 2
		}},
		{"uninitialized bounds", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) {
			m.GetBlockMeta(1).ColumnMeta(2).SetZoneMap(index.NewZM(types.T_uint64, 0))
			return m, l, 2
		}},
		{"wrong bound type", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) {
			m.GetBlockMeta(1).ColumnMeta(2).SetZoneMap(index.NewZM(types.T_int64, 0))
			return m, l, 2
		}},
		{"inverted bounds", func(m ObjectDataMeta, l Location) (ObjectDataMeta, Location, uint16) {
			zm := m.GetBlockMeta(1).ColumnMeta(2).ZoneMap()
			lo, hi := uint64(10), uint64(9)
			copy(zm.GetMinBuf(), types.EncodeUint64(&lo))
			copy(zm.GetMaxBuf(), types.EncodeUint64(&hi))
			return m, l, 2
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta, location := uint64RangeFixture(t)
			meta, location, seqnum := tc.edit(meta, location)
			require.Equal(t, Uint64Range{}, meta.Uint64BlockRange(location, seqnum))
		})
	}
}
