// Copyright 2026 Matrix Origin
// Licensed under the Apache License, Version 2.0 (the "License").
package fulltext2

// SearchDiagnostic is owned by ONE search invocation. It must never be kept on
// a cached Index or read while the search producer is running. Nil is off.
// These observations do not decide execution or contain predicate literals.
type SearchDiagnostic struct {
	SearchObserved     bool                  `json:"search_observed"`
	RuntimeLimit       uint                  `json:"runtime_limit"`
	ResolvedK          int                   `json:"resolved_k"`
	Emit               bool                  `json:"emit"`
	PredicateCompiled  bool                  `json:"predicate_compiled"`
	Predicates         []PredicateDiagnostic `json:"predicates"`
	PredicateOverflow  bool                  `json:"predicate_overflow"`
	TopKObserved       bool                  `json:"topk_observed"`
	BeforeScoreFilter  int                   `json:"before_score_filter"`
	AfterScoreFilter   int                   `json:"after_score_filter"`
	ScoreFilterPresent bool                  `json:"score_filter_present"`
}

type PredicateDiagnostic struct {
	Column      int    `json:"column"`
	Type        int32  `json:"type"`
	Operation   string `json:"operation"`
	Evaluations uint64 `json:"evaluations"`
	Rejected    uint64 `json:"rejected"`
	ReadErrors  uint64 `json:"read_errors"`
}

func (d *SearchDiagnostic) bindPredicates(preds []compiledIncludePred, includeTypes []int32, pkType int32) {
	if d == nil {
		return
	}
	d.PredicateCompiled = true
	n := len(preds)
	if n > 16 {
		n = 16
		d.PredicateOverflow = true
	}
	d.Predicates = make([]PredicateDiagnostic, n)
	for i := 0; i < n; i++ {
		p := &preds[i]
		typ := pkType
		if p.col >= 0 {
			typ = includeTypes[p.col]
		}
		op := "unknown"
		for name, kind := range includeOpKinds {
			if kind == p.kind {
				op = name
				break
			}
		}
		d.Predicates[i] = PredicateDiagnostic{Column: p.col, Type: typ, Operation: op}
		p.diagnostic = &d.Predicates[i]
	}
}
