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

import (
	"sync"
	"testing"

	"github.com/matrixorigin/matrixone/pkg/vectorindex"
	"github.com/stretchr/testify/require"
)

func TestSearchDiagnosticsPreservesResultsAndOwnership(t *testing.T) {
	for _, seg := range prefixPostingRepresentations(t, ParserNgram) {
		idx := NewIndex([]*Segment{seg}, nil)
		require.Same(t, idx, idx.diagnosticView(nil))
		for _, pattern := range []string{"alpha", "+alpha +beta", "alp* 中国", `"alpha beta"`, "absent"} {
			want, err := idx.SearchQuery([]byte(pattern), true, ParserNgram, BM25, 200, nil)
			require.NoError(t, err)
			d := &SearchDiagnostics{}
			view := idx.diagnosticView(d)
			got, err := view.SearchQuery([]byte(pattern), true, ParserNgram, BM25, 200, nil)
			require.NoError(t, err)
			require.Equal(t, resultScoreBits(want), resultScoreBits(got), "%s/%s", seg.Id, pattern)
			require.Nil(t, idx.segments[0].diagnostics)
			if pattern == "alp* 中国" {
				require.Positive(t, d.ExpandedTerms)
				require.Positive(t, d.ScoreContributions)
				if seg.dict != nil {
					require.Positive(t, d.DirectoryEntries)
					require.Positive(t, d.PostingBlocks)
					require.Positive(t, d.MaterializedBytes)
				}
			}
		}
	}
}

func TestSearchDiagnosticsConcurrentInvocations(t *testing.T) {
	s := loadedSearch(t)
	q := Fulltext2Query{Pattern: []byte("quick fox"), Boolean: true, Algo: BM25}
	want, err := s.runTopK(q, 200, nil)
	require.NoError(t, err)
	const workers = 4
	var wg sync.WaitGroup
	results := make(chan []Result, workers)
	errors := make(chan error, workers)
	counts := make(chan SearchDiagnostics, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := &SearchDiagnostics{}
			query := q
			query.Diagnostics = d
			res, err := s.runTopK(query, 200, nil)
			results <- res
			errors <- err
			counts <- *d
		}()
	}
	wg.Wait()
	var first SearchDiagnostics
	for i := range workers {
		require.NoError(t, <-errors)
		require.Equal(t, resultScoreBits(want), resultScoreBits(<-results))
		d := <-counts
		require.Positive(t, d.WANDSegments)
		require.Positive(t, d.WANDBoundCalls)
		if i == 0 {
			first = d
		} else {
			require.Equal(t, first, d)
		}
	}
	require.Nil(t, s.idx.segments[0].diagnostics)
	// A reused caller supplies a fresh counter, not state retained on the index.
	d := &SearchDiagnostics{}
	q.Diagnostics = d
	_, err = s.runTopK(q, 200, nil)
	require.NoError(t, err)
	q.Diagnostics = &SearchDiagnostics{}
	_, err = s.runTopK(q, 200, nil)
	require.NoError(t, err)
	require.Equal(t, *d, *q.Diagnostics)
}

func TestSearchDiagnosticsMultiSegmentLiveness(t *testing.T) {
	segments := prefixPostingRepresentations(t, ParserNgram)[:2]
	segments[0].Recency = 1
	segments[1].Recency = 2
	idx := NewIndex(segments, map[any]int64{int64(2): 3})
	pattern := []byte("alpha 中国")
	want, err := idx.SearchQuery(pattern, true, ParserNgram, BM25, 200, nil)
	require.NoError(t, err)
	d := &SearchDiagnostics{}
	got, err := idx.diagnosticView(d).SearchQuery(pattern, true, ParserNgram, BM25, 200, nil)
	require.NoError(t, err)
	require.Equal(t, resultScoreBits(want), resultScoreBits(got))
	for _, seg := range segments {
		require.Nil(t, seg.diagnostics)
	}
}

func TestSearchDiagnosticsPostingWork(t *testing.T) {
	seg := prefixPostingRepresentations(t, ParserDefault)[1]
	d := &SearchDiagnostics{}
	view := NewIndex([]*Segment{seg}, nil).diagnosticView(d)
	p, ok := view.segments[0].lookup("alpha")
	require.True(t, ok)
	require.EqualValues(t, 1, d.DirectoryEntries)
	require.EqualValues(t, 1, d.FSTLookups)
	require.EqualValues(t, p.nblk(), d.DirectoryBlocks)
	docs := p.materializeDocIDs(d)
	tfs := p.materializeTfs(d)
	positions := p.materializePositions(d)
	original, ok := seg.lookup("alpha")
	require.True(t, ok)
	require.Equal(t, original.materializeDocIDs(), docs)
	require.Equal(t, original.materializeTfs(), tfs)
	require.Equal(t, original.materializePositions(), positions)
	require.Len(t, tfs, len(docs))
	require.Len(t, positions, len(docs))
	require.EqualValues(t, 2*p.nblk(), d.PostingBlocks)
	require.EqualValues(t, 2*len(p.blockData), d.PostingBytes)
	var total int
	for _, pos := range positions {
		total += len(pos)
	}
	require.EqualValues(t, total, d.DecodedPositions)
	require.Nil(t, seg.diagnostics)
}

func TestSearchDiagnosticsActualLimitIncludeAndReuse(t *testing.T) {
	idx := incIdx(t)
	t.Cleanup(idx.Free)
	s := &Fulltext2Search{idx: idx, loaded: true, cfg: TableConfig{Parser: ParserDefault}}
	proc := newSearchProc(t)
	q := Fulltext2Query{Pattern: []byte("x"), Boolean: true, Algo: BM25,
		IncludePredsJSON: []byte(`[{"col":0,"op":"is_null"}]`), Diagnostics: &SearchDiagnostics{}}
	out := &vectorindex.SearchOutput{}
	rt := vectorindex.RuntimeConfig{Limit: 200}
	require.NoError(t, s.SearchInto(proc, q, rt, out))
	require.Equal(t, 1, out.Keys.N)
	require.EqualValues(t, 1, q.Diagnostics.SearchCalls)
	require.EqualValues(t, 200, q.Diagnostics.EffectiveLimit)
	require.EqualValues(t, 1, q.Diagnostics.IncludePredicates)
	require.EqualValues(t, 5, q.Diagnostics.IncludeEvaluations)
	previous := *q.Diagnostics
	// An invalid predicate fails before search; a following invocation observes
	// fresh counters and the same result, including reused output buffers.
	bad := q
	bad.IncludePredsJSON = []byte("{")
	bad.Diagnostics = &SearchDiagnostics{}
	require.Error(t, s.SearchInto(proc, bad, rt, out))
	require.Zero(t, bad.Diagnostics.SearchCalls)
	q.Diagnostics = &SearchDiagnostics{}
	require.NoError(t, s.SearchInto(proc, q, rt, out))
	require.Equal(t, previous, *q.Diagnostics)
	require.Equal(t, 1, out.Keys.N)
	require.Nil(t, idx.segments[0].diagnostics)
}
