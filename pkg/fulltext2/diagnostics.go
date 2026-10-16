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

package fulltext2

// SearchDiagnostics counts work in one sequential search invocation. The caller
// owns it and must not share it between concurrent searches. No terms, document
// contents or primary keys are retained. SliceBytes counts requested backing
// storage, not allocator size classes, pooled capacity or total process heap.
// Diagnostic-view allocations themselves are deliberately excluded.
type SearchDiagnostics struct {
	SearchCalls         uint64 `json:"search_calls"`
	EffectiveLimit      uint64 `json:"effective_limit"`
	IncludePredicates   uint64 `json:"include_predicates"`
	BooleanFullSegments uint64 `json:"boolean_full_segments"`
	WANDSegments        uint64 `json:"wand_segments"`
	ConjunctiveSegments uint64 `json:"conjunctive_segments"`
	PhraseCalls         uint64 `json:"phrase_calls"`
	ExpandedTerms       uint64 `json:"expanded_terms"`
	FSTLookups          uint64 `json:"fst_lookups"`
	DFHeaderReads       uint64 `json:"df_header_reads"`
	DirectoryEntries    uint64 `json:"directory_entries"`
	DirectoryBlocks     uint64 `json:"directory_blocks"`
	DirectorySliceBytes uint64 `json:"directory_slice_bytes"`
	PostingBlocks       uint64 `json:"posting_blocks"`
	PostingBytes        uint64 `json:"posting_bytes"`
	PositionBlocks      uint64 `json:"position_blocks"`
	PositionBytes       uint64 `json:"position_bytes"`
	DecodedPositions    uint64 `json:"decoded_positions"`
	Materializations    uint64 `json:"materializations"`
	MaterializedBytes   uint64 `json:"materialized_slice_bytes"`
	ScoreContributions  uint64 `json:"score_contributions"`
	ScoredCandidates    uint64 `json:"scored_candidates"`
	IncludeEvaluations  uint64 `json:"include_evaluations"`
	WANDBoundCalls      uint64 `json:"wand_block_bound_calls"`
	WANDTermBounds      uint64 `json:"wand_term_bound_additions"`
	WANDCursorMoves     uint64 `json:"wand_cursor_moves"`
}

// diagnosticView borrows the immutable index data under the caller's existing
// cache read lock. Only the small Index/Segment headers are copied; neither the
// view nor its postings owns the shared buffers and it must never be Free'd.
// Keeping the counter on these query-local headers avoids mutable state on the
// cached Index, Segment, dictionary or build-side postings.
func (idx *Index) diagnosticView(d *SearchDiagnostics) *Index {
	if d == nil {
		return idx
	}
	view := *idx
	view.segments = make([]*Segment, len(idx.segments))
	for i, seg := range idx.segments {
		copy := *seg
		copy.diagnostics = d
		view.segments[i] = &copy
	}
	return &view
}

func firstDiagnostic(values []*SearchDiagnostics) *SearchDiagnostics {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}
