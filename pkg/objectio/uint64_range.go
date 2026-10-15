// Copyright 2026 Matrix Origin
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package objectio

import "github.com/matrixorigin/matrixone/pkg/container/types"

// Uint64Range is a copied, call-local view of one persisted block column.
// An invalid range cannot exclude any candidate. It owns no metadata memory.
type Uint64Range struct {
	Min, Max uint64
	Valid    bool
}

// Uint64BlockRange reads only metadata already returned by the column loader.
// Missing or unsupported optimization metadata is conservatively unavailable;
// the caller must still propagate the loader's original error before calling it.
func (o objectDataMetaV1) Uint64BlockRange(location Location, seqnum uint16) Uint64Range {
	var unavailable Uint64Range
	if len(location) != LocationLen || len(o) < headerLen || seqnum >= SEQNUM_UPPER ||
		!IsSameObjectLocVsMeta(location, o) {
		return unavailable
	}
	header := o.BlockHeader()
	start, count := uint32(header.StartID()), o.BlockCount()
	id := uint32(location.ID())
	if header.Appendable() || id < start || id-start >= count {
		return unavailable
	}
	indexOffset := uint64(headerLen) + uint64(header.MetaColumnCount())*uint64(colMetaLen)
	if indexOffset+blockCountLen+uint64(count)*posLen > uint64(len(o)) {
		return unavailable
	}
	index := BlockIndex(o[indexOffset:])
	if index.BlockCount() != count {
		return unavailable
	}
	offset, length := index.BlockMetaPos(id - start)
	if uint64(offset) < indexOffset+blockCountLen+uint64(count)*posLen ||
		length < headerLen || uint64(offset)+uint64(length) > uint64(len(o)) {
		return unavailable
	}
	block := BlockObject(o[offset : uint64(offset)+uint64(length)])
	if block.BlockHeader().Appendable() || block.BlockHeader().Sequence() != location.ID() ||
		seqnum >= block.BlockHeader().MetaColumnCount() || seqnum > block.GetMaxSeqnum() ||
		uint64(headerLen)+(uint64(seqnum)+1)*uint64(colMetaLen) > uint64(len(block)) {
		return unavailable
	}
	column := block.ColumnMeta(seqnum)
	if types.T(column.DataType()) != types.T_uint64 {
		return unavailable
	}
	zm := column.ZoneMap()
	if len(zm) < ZoneMapSize || !zm.IsInited() || zm.GetType() != types.T_uint64 ||
		len(zm.GetMinBuf()) != 8 || len(zm.GetMaxBuf()) != 8 {
		return unavailable
	}
	lo, hi := types.DecodeUint64(zm.GetMinBuf()), types.DecodeUint64(zm.GetMaxBuf())
	if lo > hi {
		return unavailable
	}
	return Uint64Range{Min: lo, Max: hi, Valid: true}
}
