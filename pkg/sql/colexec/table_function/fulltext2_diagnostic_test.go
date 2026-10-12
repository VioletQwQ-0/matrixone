// Copyright 2026 Matrix Origin
// Licensed under the Apache License, Version 2.0 (the "License").
package table_function

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/matrixorigin/matrixone/pkg/pb/plan"
	"github.com/matrixorigin/matrixone/pkg/vm/process"
	"github.com/stretchr/testify/require"
)

func TestFT2DiagnosticTerminalAndRedaction(t *testing.T) {
	d := &ft2QueryDiagnostic{SQLHash: ft2DiagnosticHash([]byte("secret"))}
	d.seal(context.Background(), nil)
	require.Equal(t, "not_executed", d.Terminal)
	d.Search.SearchObserved = true
	d.seal(context.Background(), nil)
	require.Equal(t, "success", d.Terminal)
	d.seal(context.Background(), errors.New("secret error"))
	require.Equal(t, "error", d.Terminal)
	d.seal(context.Background(), context.Canceled)
	require.Equal(t, "cancelled", d.Terminal)
	data, err := json.Marshal(d)
	require.NoError(t, err)
	require.NotContains(t, string(data), "secret")
	require.Less(t, len(data), 8192)
}

func TestFT2DiagnosticOptInIdentityAndBound(t *testing.T) {
	old := ft2DiagnosticEnabled
	oldSequence := ft2DiagnosticSequence.Load()
	t.Cleanup(func() { ft2DiagnosticEnabled = old; ft2DiagnosticSequence.Store(oldSequence) })
	p := &process.Process{Base: &process.BaseProcess{Id: "query-1", StmtProfile: process.NewStmtProfile(uuid.New(), uuid.New())}}
	p.Base.StmtProfile.SetSqlOfStmt("SELECT /*ft2-diagnostic*/ secret_literal")
	tf := &TableFunction{}
	ft2DiagnosticEnabled = false
	require.Nil(t, beginFT2Diagnostic(tf, p, 200, 1))
	ft2DiagnosticEnabled = true
	ft2DiagnosticSequence.Store(0)
	p.Base.StmtProfile.SetSqlOfStmt("SELECT secret_literal")
	require.Nil(t, beginFT2Diagnostic(tf, p, 200, 1))
	p.Base.StmtProfile.SetSqlOfStmt("SELECT /*ft2-diagnostic*/ secret_literal")
	a := beginFT2Diagnostic(tf, p, 200, 1)
	require.Equal(t, "query-1", a.QueryID)
	require.Len(t, a.SQLHash, 64)
	require.EqualValues(t, 200, a.PlannedLimit)
	require.Nil(t, a.PostLimitResidualQualification)
	p.SetQueryId("query-2")
	b := beginFT2Diagnostic(tf, p, 0, 2)
	require.NotEqual(t, a.QueryID, b.QueryID)
	require.NotEqual(t, a.Invocation, b.Invocation)
	require.EqualValues(t, 2, b.Round)
	ft2DiagnosticSequence.Store(64)
	require.Nil(t, beginFT2Diagnostic(tf, p, 200, 3))
}

func TestFT2DiagnosticActualPlanResidual(t *testing.T) {
	q := &plan.Query{Steps: []int32{2}, Nodes: []*plan.Node{
		{NodeId: 0, NodeType: plan.Node_FUNCTION_SCAN, TableDef: &plan.TableDef{TblFunc: &plan.TableFunction{Name: "fulltext2_search"}}},
		{NodeId: 1, NodeType: plan.Node_SORT, Children: []int32{0}},
		{NodeId: 2, NodeType: plan.Node_PROJECT, Children: []int32{1}},
	}}
	r := ft2ResidualQualification(q, 0)
	require.NotNil(t, r)
	require.False(t, *r)
	q.Nodes[1].FilterList = []*plan.Expr{{}}
	r = ft2ResidualQualification(q, 0)
	require.NotNil(t, r)
	require.True(t, *r)
	q.Nodes[1].FilterList = nil
	q.Nodes[1].NodeType = plan.Node_JOIN
	require.Nil(t, ft2ResidualQualification(q, 0))
	q.Nodes[1].NodeType = plan.Node_SORT
	q.Nodes[1].Children = []int32{2}
	require.Nil(t, ft2ResidualQualification(q, 0))
	q.Nodes[1].Children = []int32{0}
	old := ft2DiagnosticEnabled
	t.Cleanup(func() { ft2DiagnosticEnabled = old })
	tf := &TableFunction{FuncName: "fulltext2_search"}
	ft2DiagnosticEnabled = false
	AttachFT2DiagnosticPlan(tf, q.Nodes[0], q)
	require.Empty(t, tf.Params)
	ft2DiagnosticEnabled = true
	AttachFT2DiagnosticPlan(tf, q.Nodes[0], q)
	require.NotEmpty(t, tf.Params)
	saved := append([]byte(nil), tf.Params...)
	AttachFT2DiagnosticPlan(tf, q.Nodes[0], q)
	require.Equal(t, saved, tf.Params)
}
