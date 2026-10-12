// Copyright 2026 Matrix Origin
// Licensed under the Apache License, Version 2.0 (the "License").
package fulltext2

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matrixorigin/matrixone/pkg/vectorindex"
	"github.com/stretchr/testify/require"
)

func TestQueryDiagnosticSearchEquivalence(t *testing.T) {
	proc := newSearchProc(t)
	s := &Fulltext2Search{idx: incIdx(t), loaded: true, cfg: TableConfig{IndexTable: "__store", Parser: ParserDefault}}
	q := Fulltext2Query{Pattern: []byte("x"), Algo: BM25, IncludePredsJSON: []byte(`[{"col":0,"op":"is_null"}]`)}
	rt := vectorindex.RuntimeConfig{Limit: 200}
	off := &vectorindex.SearchOutput{}
	require.NoError(t, s.SearchInto(proc, q, rt, off))
	d := &SearchDiagnostic{}
	q.Diagnostic = d
	on := &vectorindex.SearchOutput{}
	require.NoError(t, s.SearchInto(proc, q, rt, on))
	require.Equal(t, off.Dists, on.Dists)
	require.Equal(t, off.Keys, on.Keys)
	require.True(t, d.SearchObserved)
	require.EqualValues(t, 200, d.RuntimeLimit)
	require.Equal(t, 200, d.ResolvedK)
	require.False(t, d.Emit)
	require.True(t, d.PredicateCompiled)
	require.Len(t, d.Predicates, 1)
	require.Equal(t, "is_null", d.Predicates[0].Operation)
	require.Positive(t, d.Predicates[0].Evaluations)
	require.Positive(t, d.Predicates[0].Rejected)
	require.Zero(t, d.Predicates[0].ReadErrors)
	require.True(t, d.TopKObserved)
	require.Equal(t, 1, d.BeforeScoreFilter)
	require.Equal(t, 1, d.AfterScoreFilter)
	// A fresh query must not receive an earlier invocation's predicate/counters.
	fresh := &SearchDiagnostic{}
	q.Diagnostic, q.IncludePredsJSON = fresh, nil
	require.NoError(t, s.SearchInto(proc, q, rt, on))
	require.Empty(t, fresh.Predicates)
	require.False(t, fresh.PredicateCompiled)
	require.Len(t, on.Dists, 5)
}

func TestQueryDiagnosticEmitAndError(t *testing.T) {
	proc := newSearchProc(t)
	s := &Fulltext2Search{idx: incIdx(t), loaded: true, cfg: TableConfig{IndexTable: "__store", Parser: ParserDefault}}
	d := &SearchDiagnostic{}
	q := Fulltext2Query{Pattern: []byte("x"), Algo: BM25, Diagnostic: d}
	_, _, err := s.Search(proc, q, vectorindex.RuntimeConfig{Emit: func(out *vectorindex.SearchOutput) error {
		return context.Canceled
	}})
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, d.Emit)
	require.Zero(t, d.RuntimeLimit)
	require.False(t, d.TopKObserved)
	q.Diagnostic = &SearchDiagnostic{}
	q.IncludePredsJSON = []byte(`[{"col":0,"op":"bogus","val":"private value"}]`)
	require.Error(t, s.SearchInto(proc, q, vectorindex.RuntimeConfig{Limit: 200}, &vectorindex.SearchOutput{}))
	require.False(t, q.Diagnostic.PredicateCompiled)
	data, err := json.Marshal(q.Diagnostic)
	require.NoError(t, err)
	require.NotContains(t, string(data), "private value")
}

func TestQueryDiagnosticConcurrentOwnership(t *testing.T) {
	s := &Fulltext2Search{idx: incIdx(t), loaded: true, cfg: TableConfig{Parser: ParserDefault}}
	start := make(chan struct{})
	type observed struct {
		diagnostic *SearchDiagnostic
		err        error
	}
	completed := make(chan observed, 2)
	for _, spec := range []string{`[{"col":0,"op":"is_null"}]`, `[{"col":0,"op":"is_not_null"}]`} {
		proc := newSearchProc(t)
		go func(spec string) {
			<-start
			d := &SearchDiagnostic{}
			q := Fulltext2Query{Pattern: []byte("x"), Algo: BM25, IncludePredsJSON: []byte(spec), Diagnostic: d}
			err := s.SearchInto(proc, q, vectorindex.RuntimeConfig{Limit: 200}, &vectorindex.SearchOutput{})
			completed <- observed{d, err}
		}(spec)
	}
	close(start)
	operations := make(map[string]bool)
	for i := 0; i < 2; i++ {
		r := <-completed
		require.NoError(t, r.err)
		require.Len(t, r.diagnostic.Predicates, 1)
		operations[r.diagnostic.Predicates[0].Operation] = true
	}
	require.Len(t, operations, 2)
}
