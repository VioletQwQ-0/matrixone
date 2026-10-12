// Copyright 2026 Matrix Origin
// Licensed under the Apache License, Version 2.0 (the "License").
package table_function

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"

	"github.com/matrixorigin/matrixone/pkg/fulltext2"
	"github.com/matrixorigin/matrixone/pkg/logutil"
	"github.com/matrixorigin/matrixone/pkg/pb/plan"
	"github.com/matrixorigin/matrixone/pkg/vm/process"
)

// Administrative opt-in on an isolated diagnostic CN only. A SQL comment marks
// the exact probe, so fixture/convergence queries cannot exhaust the log budget.
var ft2DiagnosticEnabled = os.Getenv("MO_FT2_QUERY_DIAGNOSTIC") == "1"
var ft2DiagnosticSequence atomic.Uint64

const ft2DiagnosticMarker = "/*ft2-diagnostic*/"

type ft2QueryDiagnostic struct {
	SchemaVersion      int    `json:"schema_version"`
	QueryID            string `json:"query_id"`
	StatementID        string `json:"statement_id"`
	Node               int    `json:"ft2_node"`
	Operator           int32  `json:"operator"`
	Parallel           int32  `json:"parallel"`
	Invocation         uint64 `json:"invocation"`
	Round              uint64 `json:"round"`
	SQLHash            string `json:"sql_sha256"`
	NodePlanHash       string `json:"node_plan_sha256"`
	ExecutionPlanHash  string `json:"execution_plan_sha256"`
	PlannedLimit       uint64 `json:"planned_limit"`
	EffectiveLimit     uint64 `json:"effective_limit"`
	PredicateHash      string `json:"predicate_sha256"`
	IncludeColumnsHash string `json:"include_columns_sha256"`
	MembershipOutcome  string `json:"membership_outcome"`
	FallbackReason     string `json:"fallback_reason"`
	Path               string `json:"path"`
	Terminal           string `json:"terminal"`
	// Compiler snapshot, not inferred from TVF row counts. Unknown topology or
	// identity is nil; only the exact simple covered plan yields false.
	PostLimitResidualQualification *bool                      `json:"post_limit_residual_qualification"`
	Search                         fulltext2.SearchDiagnostic `json:"search"`
}

func ft2DiagnosticHash(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func beginFT2Diagnostic(tf *TableFunction, proc *process.Process, planned, round uint64) *ft2QueryDiagnostic {
	if !ft2DiagnosticEnabled || proc.Base == nil || proc.Base.StmtProfile == nil {
		return nil
	}
	text := proc.Base.StmtProfile.GetSqlOfStmt()
	if !strings.Contains(text, ft2DiagnosticMarker) {
		return nil
	}
	id := ft2DiagnosticSequence.Add(1)
	if id > 64 {
		return nil
	}
	plan, e := json.Marshal(struct {
		Args    any
		Limit   any
		Filters any
	}{tf.Args, tf.Limit, tf.RuntimeFilterSpecs})
	if e != nil {
		return nil
	}
	queryID := proc.QueryId()
	if len(queryID) > 128 {
		return nil
	} // no unbounded caller-controlled log fields
	d := &ft2QueryDiagnostic{SchemaVersion: 1, QueryID: queryID,
		StatementID: proc.Base.StmtProfile.GetStmtId().String(), Node: tf.GetIdx(),
		Operator: tf.OperatorID, Parallel: tf.ParallelID, Invocation: id, Round: round,
		SQLHash: ft2DiagnosticHash([]byte(text)), NodePlanHash: ft2DiagnosticHash(plan),
		PlannedLimit: planned, Path: "not_started", MembershipOutcome: "not_configured"}
	var meta ft2DiagnosticPlan
	if json.Unmarshal(tf.Params, &meta) == nil && meta.Schema == "ft2-query-plan-v1" && meta.Node == tf.GetIdx() {
		d.ExecutionPlanHash = meta.Hash
		d.PostLimitResidualQualification = meta.Residual
	}
	// Seal the compiler/statement association BEFORE search observation. Consumers
	// join the two envelopes and additionally bind query ID/SQL hash from the
	// owned SQL client, never fabricate identity by copying a terminal verdict.
	if data, e := json.Marshal(d); e == nil {
		logutil.Infof("[ft2-query-plan-diagnostic] %s", data)
	}
	return d
}

type ft2DiagnosticPlan struct {
	Schema   string `json:"schema"`
	Hash     string `json:"hash"`
	Node     int    `json:"node"`
	Residual *bool  `json:"residual"`
}

// AttachFT2DiagnosticPlan observes the exact query used by constructTableFunction.
// Existing Params transport/copy carries the receipt metadata; it is never used
// for query decisions. Default-off and non-empty function Params are untouched.
func AttachFT2DiagnosticPlan(tf *TableFunction, node *plan.Node, qry *plan.Query) {
	if !ft2DiagnosticEnabled || tf.FuncName != "fulltext2_search" || len(tf.Params) != 0 || qry == nil || node == nil || len(qry.Nodes) > 4096 {
		return
	}
	data, err := json.Marshal(qry)
	if err != nil {
		return
	}
	meta := ft2DiagnosticPlan{Schema: "ft2-query-plan-v1", Hash: ft2DiagnosticHash(data), Node: int(node.NodeId), Residual: ft2ResidualQualification(qry, node.NodeId)}
	if out, err := json.Marshal(meta); err == nil {
		tf.Params = out
	}
}

// Only the minimal covered Q1 path (Project/Sort over one FT2 node) establishes
// absence. Other topologies, multiple paths, cycles or filters fail closed.
func ft2ResidualQualification(qry *plan.Query, target int32) *bool {
	if qry == nil || len(qry.Steps) != 1 || len(qry.Nodes) > 4096 {
		return nil
	}
	seen := make(map[int32]bool)
	var walk func(int32, int) (bool, bool)
	walk = func(id int32, depth int) (bool, bool) {
		if depth > 256 || id < 0 || int(id) >= len(qry.Nodes) || seen[id] || qry.Nodes[id] == nil {
			return false, false
		}
		seen[id] = true
		n := qry.Nodes[id]
		if len(n.FilterList) != 0 || len(n.RuntimeFilterProbeList) != 0 {
			return false, true
		}
		if id == target {
			return n.NodeType == plan.Node_FUNCTION_SCAN && n.TableDef != nil && n.TableDef.TblFunc != nil && n.TableDef.TblFunc.Name == "fulltext2_search" && len(n.Children) == 0, true
		}
		if (n.NodeType != plan.Node_PROJECT && n.NodeType != plan.Node_SORT) || len(n.Children) != 1 {
			return false, false
		}
		return walk(n.Children[0], depth+1)
	}
	covered, known := walk(qry.Steps[0], 0)
	if !known {
		return nil
	}
	residual := !covered
	return &residual
}

func (d *ft2QueryDiagnostic) finish(ctx context.Context, err error) {
	if d == nil {
		return
	}
	d.seal(ctx, err)
	if data, e := json.Marshal(d); e == nil {
		// JSON is bounded by fixed fields and <=16 value-free predicate descriptors.
		logutil.Infof("[ft2-query-diagnostic] %s", data)
	}
}

func (d *ft2QueryDiagnostic) seal(ctx context.Context, err error) {
	d.Terminal = "not_executed"
	if d.Search.SearchObserved {
		d.Terminal = "success"
	}
	if err != nil {
		d.Terminal = "error"
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		d.Terminal = "cancelled"
	}
}
