// Experiment-only reader matcher observation. Never a TPS subject.
package readutil

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/matrixorigin/matrixone/pkg/catalog"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/fileservice"
	"github.com/matrixorigin/matrixone/pkg/objectio"
	"github.com/matrixorigin/matrixone/pkg/pb/plan"
	"github.com/matrixorigin/matrixone/pkg/sql/plan/function"
	"github.com/matrixorigin/matrixone/pkg/util/trace"
	"github.com/matrixorigin/matrixone/pkg/vm/engine"
	"github.com/matrixorigin/matrixone/pkg/vm/engine/tae/containers"
	"os"
	"strings"
	"sync/atomic"
)

var issue29322ReaderIDs atomic.Uint64
var issue29322ReaderRecords atomic.Uint64

func issue29322SecondaryReaderTarget(name string) bool {
	if !strings.HasPrefix(name, "__mo_index_secondary_") {
		return false
	}
	if os.Getenv("MO_ISSUE29322_ATTRIBUTION10M") == "true" {
		return true
	}
	names := strings.Split(os.Getenv("MO_ISSUE29322_SECONDARY_TABLES"), ",")
	if len(names) > 2 {
		return false
	}
	for _, allowed := range names {
		if name == allowed && name != "" {
			return true
		}
	}
	return false
}

func issue29322AttachReaderProbe(ctx context.Context, filter *objectio.BlockReadFilter, table *plan.TableDef, base BasePKFilter, blockID *string) uint64 {
	if os.Getenv("MO_ISSUE29322_READER_PROBE") != "true" || filter == nil || table == nil {
		return 0
	}
	fakePK := filter.HasFakePK || (table.Pkey != nil && catalog.IsFakePkName(table.Pkey.PkeyColName))
	if !(table.Name == "sbtest1" && fakePK) && !issue29322SecondaryReaderTarget(table.Name) {
		return 0
	}
	traceID := trace.SpanFromContext(ctx).SpanContext().TraceID.String()
	tableName := table.Name
	valid, oid, op := base.Valid, base.Oid.String(), base.Op
	id := issue29322ReaderIDs.Add(1)
	m := -1
	eligible := fakePK && base.Valid && base.Oid == types.T_uint64 && base.Op == function.IN && len(base.Disjuncts) == 0 && base.Vec != nil && filter.UnSortedSearchFunc != nil
	if eligible {
		m = base.Vec.Length()
	}
	var candidates []uint64
	candidateComplete := false
	if os.Getenv("MO_ISSUE29322_ATTRIBUTION10M") == "true" && eligible && m <= 4096 {
		candidates = append([]uint64{}, vector.MustFixedColNoTypeCheck[uint64](base.Vec)...)
		candidateComplete = true
	}
	var matchedOffsets []int64
	record := func(kind string, n, hits int) {
		if issue29322ReaderRecords.Add(1) > 1048576 {
			fmt.Println("ISSUE29322_PROBE_TRUNCATED matcher")
			return
		}
		data, err := json.Marshal(map[string]any{"kind": kind, "reader_probe_id": id, "phase": os.Getenv("MO_ISSUE29322_QUERY_PHASE"), "table": tableName, "trace_id": traceID, "M": m, "N": n, "hits": hits, "block_id": *blockID, "offsets": matchedOffsets, "matcher_eligible": eligible, "candidates": candidates, "candidate_capture_complete": candidateComplete, "base_valid": valid, "base_oid": oid, "base_op": op})
		if err == nil {
			fmt.Println("ISSUE29322_READER_OBSERVATION " + string(data))
		}
	}
	record("build", 0, 0)
	if !eligible {
		return id
	}
	original := filter.UnSortedSearchFunc
	filter.UnSortedSearchFunc = func(input containers.Vectors) []int64 {
		offsets := original(input)
		matchedOffsets = append([]int64{}, offsets...)
		n := -1
		if len(input) > 0 {
			n = input[0].Length()
		}
		record("match", n, len(offsets))
		return offsets
	}
	return id
}

func (r *reader) issue29322ObserveRead(ctx context.Context, state engine.DataState, gotNext bool, blockID string, metadataRows uint32, blockReads uint64, returnedRows int, filterValid, isEnd, failed bool) {
	if r.issue29322ProbeID == 0 {
		return
	}
	if issue29322ReaderRecords.Add(1) > 1048576 {
		fmt.Println("ISSUE29322_PROBE_TRUNCATED read")
		return
	}
	data, err := json.Marshal(map[string]any{"kind": "read", "reader_pointer": fmt.Sprintf("%p", r), "reader_probe_id": r.issue29322ProbeID, "phase": os.Getenv("MO_ISSUE29322_QUERY_PHASE"), "table": r.name, "trace_id": trace.SpanFromContext(ctx).SpanContext().TraceID.String(), "source_next_returned": gotNext, "source_state": int(state), "block_id": blockID, "candidate_block_rows_metadata": metadataRows, "block_read_invocations": blockReads, "returned_batch_rows": returnedRows, "filter_valid_before_read": filterValid, "end": isEnd, "failed": failed})
	if err == nil {
		fmt.Println("ISSUE29322_READER_OBSERVATION " + string(data))
	}
}

func (r *reader) Issue29322ProbeIDs() []uint64 {
	if r.issue29322ProbeID == 0 {
		return nil
	}
	return []uint64{r.issue29322ProbeID}
}
func (r *mergeReader) Issue29322ProbeIDs() []uint64 {
	var ids []uint64
	for _, rd := range r.rds {
		if p, ok := rd.(interface{ Issue29322ProbeIDs() []uint64 }); ok {
			ids = append(ids, p.Issue29322ProbeIDs()...)
		}
	}
	return ids
}
func (r *reader) issue29322ObserveBlockMetadata(ctx context.Context, info *objectio.BlockInfo, fs fileservice.FileService, seqnums []uint16, typs []types.Type) {
	if os.Getenv("MO_ISSUE29322_ATTRIBUTION10M") != "true" || r.issue29322ProbeID == 0 || r.name != "sbtest1" || len(seqnums) == 0 || len(typs) == 0 {
		return
	}
	value := map[string]any{"kind": "block_metadata", "reader_probe_id": r.issue29322ProbeID, "block_id": info.BlockID.String(), "seqnum": seqnums[0], "snapshot_rows_metadata": info.MetaLocation().Rows(), "sorted": info.IsSorted(), "object_location": info.MetaLocation().String(), "extra_probe_metadata_load": true, "complete": false}
	location := info.MetaLocation()
	meta, err := objectio.FastLoadObjectMeta(ctx, &location, false, fs)
	if err == nil {
		zm := meta.MustDataMeta().GetBlockMeta(uint32(location.ID())).MustGetColumn(seqnums[0]).ZoneMap()
		value["block_rows_metadata"] = meta.MustDataMeta().GetBlockMeta(uint32(location.ID())).GetRows()
		value["complete"] = true
		value["initialized"] = zm.IsInited()
		value["oid"] = zm.GetType().String()
		if zm.IsInited() && zm.GetType() == types.T_uint64 {
			value["min"] = types.DecodeUint64(zm.GetMinBuf())
			value["max"] = types.DecodeUint64(zm.GetMaxBuf())
		}
	} else {
		value["error_category"] = "PROBE_METADATA_READ_FAILED"
	}
	if issue29322ReaderRecords.Add(1) > 1048576 {
		fmt.Println("ISSUE29322_PROBE_TRUNCATED metadata")
		return
	}
	data, err := json.Marshal(value)
	if err == nil {
		fmt.Println("ISSUE29322_READER_OBSERVATION " + string(data))
	}
}
