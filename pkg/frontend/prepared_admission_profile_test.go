// Copyright 2026 Matrix Origin
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

package frontend

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matrixorigin/matrixone/pkg/catalog"
	"github.com/matrixorigin/matrixone/pkg/pb/plan"
)

func TestPreparedPKAdmissionReason(t *testing.T) {
	pkCol := admissionCol(7, 0)
	param := &plan.Expr{Expr: &plan.Expr_P{P: &plan.ParamRef{Pos: 0}}}
	literal := &plan.Expr{Expr: &plan.Expr_Lit{Lit: &plan.Literal{}}}
	otherCol := admissionCol(7, 1)

	tests := []struct {
		name   string
		plan   *plan.Plan
		reason string
	}{
		{name: "nil plan", reason: "no-query"},
		{name: "single pk", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn("=", pkCol, param))), reason: "eligible"},
		{name: "reversed equality", plan: admissionPlan(plan.Query_UPDATE,
			admissionScan(false, nil, admissionFn("=", param, pkCol))), reason: "eligible"},
		{name: "literal equality", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn("=", pkCol, literal))), reason: "eligible"},
		{name: "cast parameter equality", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn("=", pkCol, admissionFn("cast", param)))), reason: "eligible"},
		{name: "pk equality under and with residual", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn("and",
				admissionFn("=", pkCol, param), admissionFn(">", otherCol, literal)))), reason: "eligible"},
		{name: "pk equality under or", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn("or",
				admissionFn("=", pkCol, param), admissionFn("=", otherCol, literal)))), reason: "no-bound-primary-key-equality"},
		{name: "wrong binding", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn("=", admissionCol(8, 0), param))), reason: "no-bound-primary-key-equality"},
		{name: "composite serial", plan: admissionPlan(plan.Query_DELETE,
			admissionScan(false, nil, admissionFn("=", pkCol,
				admissionFn("serial", param, &plan.Expr{Expr: &plan.Expr_Lit{Lit: &plan.Literal{}}})))), reason: "eligible"},
		{name: "insert", plan: admissionPlan(plan.Query_INSERT,
			admissionScan(false, nil, admissionFn("=", pkCol, param))), reason: "unsupported-statement"},
		{name: "range", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn(">", pkCol, param))), reason: "no-bound-primary-key-equality"},
		{name: "prefix composite", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn("prefix_eq", pkCol, param))), reason: "no-bound-primary-key-equality"},
		{name: "column equality", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn("=", pkCol, otherCol))), reason: "no-bound-primary-key-equality"},
		{name: "runtime expression", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn("=", pkCol, admissionFn("now")))), reason: "no-bound-primary-key-equality"},
		{name: "temporary", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(true, nil, admissionFn("=", pkCol, param))), reason: "temporary-table"},
		{name: "subscription", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, &plan.ObjectRef{SubscriptionName: "sub"}, admissionFn("=", pkCol, param))), reason: "subscription-table"},
		{name: "multiple scans", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn("=", pkCol, param)),
			admissionScan(false, nil, admissionFn("=", pkCol, param))), reason: "multiple-table-scans"},
		{name: "join", plan: admissionPlan(plan.Query_SELECT,
			admissionScan(false, nil, admissionFn("=", pkCol, param)),
			&plan.Node{NodeType: plan.Node_JOIN}), reason: "unsupported-shape"},
		{name: "fake pk", plan: admissionPlan(plan.Query_SELECT,
			admissionScanWithPK(catalog.FakePrimaryKeyColName, admissionFn("=", pkCol, param))), reason: "no-primary-key"},
		{name: "composite full pk", plan: admissionPlan(plan.Query_SELECT,
			admissionCompositeScan(admissionFn("=", admissionCol(7, 0), param),
				admissionFn("=", admissionCol(7, 1), param))), reason: "eligible"},
		{name: "composite full pk under and with residual", plan: admissionPlan(plan.Query_SELECT,
			admissionCompositeScan(admissionFn("and",
				admissionFn("=", admissionCol(7, 0), param),
				admissionFn("=", admissionCol(7, 1), param),
				admissionFn(">", admissionCol(7, 3), literal)))), reason: "eligible"},
		{name: "composite pk under or", plan: admissionPlan(plan.Query_SELECT,
			admissionCompositeScan(admissionFn("or",
				admissionFn("=", admissionCol(7, 0), param),
				admissionFn("=", admissionCol(7, 1), param)))), reason: "no-bound-primary-key-equality"},
		{name: "composite duplicate component", plan: admissionPlan(plan.Query_SELECT,
			admissionCompositeScan(admissionFn("=", admissionCol(7, 0), param),
				admissionFn("=", admissionCol(7, 0), literal))), reason: "no-bound-primary-key-equality"},
		{name: "composite partial pk", plan: admissionPlan(plan.Query_SELECT,
			admissionCompositeScan(admissionFn("=", admissionCol(7, 0), param))), reason: "no-bound-primary-key-equality"},
		{name: "composite encoded pk", plan: admissionPlan(plan.Query_SELECT,
			admissionCompositeScan(admissionFn("=", admissionCol(7, 2),
				admissionFn("serial_full", param, param)))), reason: "eligible"},
		{name: "composite encoded partial pk", plan: admissionPlan(plan.Query_SELECT,
			admissionCompositeScan(admissionFn("=", admissionCol(7, 2),
				admissionFn("serial_full", param)))), reason: "no-bound-primary-key-equality"},
		{name: "composite encoded extra part", plan: admissionPlan(plan.Query_SELECT,
			admissionCompositeScan(admissionFn("=", admissionCol(7, 2),
				admissionFn("serial_full", param, param, param)))), reason: "no-bound-primary-key-equality"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.reason, preparedPKAdmissionReason(test.plan))
		})
	}
}

func admissionPlan(stmtType plan.Query_StatementType, nodes ...*plan.Node) *plan.Plan {
	return &plan.Plan{Plan: &plan.Plan_Query{Query: &plan.Query{
		StmtType: stmtType,
		Nodes:    nodes,
	}}}
}

func admissionScan(temporary bool, ref *plan.ObjectRef, filters ...*plan.Expr) *plan.Node {
	node := admissionScanWithPK("id", filters...)
	node.TableDef.IsTemporary = temporary
	node.ObjRef = ref
	return node
}

func admissionScanWithPK(pkName string, filters ...*plan.Expr) *plan.Node {
	return &plan.Node{
		NodeType:    plan.Node_TABLE_SCAN,
		BindingTags: []int32{7},
		TableDef: &plan.TableDef{
			Pkey:          &plan.PrimaryKeyDef{PkeyColName: pkName, Names: []string{pkName}},
			Name2ColIndex: map[string]int32{pkName: 0},
		},
		FilterList: filters,
	}
}

func admissionCompositeScan(filters ...*plan.Expr) *plan.Node {
	return &plan.Node{
		NodeType:    plan.Node_TABLE_SCAN,
		BindingTags: []int32{7},
		TableDef: &plan.TableDef{
			Pkey:          &plan.PrimaryKeyDef{PkeyColName: catalog.CPrimaryKeyColName, Names: []string{"a", "b"}},
			Name2ColIndex: map[string]int32{"a": 0, "b": 1, catalog.CPrimaryKeyColName: 2},
		},
		FilterList: filters,
	}
}

func admissionCol(relPos, colPos int32) *plan.Expr {
	return &plan.Expr{Expr: &plan.Expr_Col{Col: &plan.ColRef{RelPos: relPos, ColPos: colPos}}}
}

func admissionFn(name string, args ...*plan.Expr) *plan.Expr {
	return &plan.Expr{Expr: &plan.Expr_F{F: &plan.Function{
		Func: &plan.ObjectRef{ObjName: name},
		Args: args,
	}}}
}
