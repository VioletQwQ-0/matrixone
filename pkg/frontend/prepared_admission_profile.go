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
	"strings"

	"github.com/matrixorigin/matrixone/pkg/catalog"
	"github.com/matrixorigin/matrixone/pkg/pb/plan"
)

func preparedProtocolReason(cmd CommandType) string {
	switch cmd {
	case COM_STMT_PREPARE:
		return "com-stmt-prepare"
	case COM_STMT_EXECUTE:
		return "com-stmt-execute"
	default:
		return "text-or-internal"
	}
}

// preparedPKAdmissionReason is profiling-only classification. It deliberately
// accepts fewer plans than a future production specialization might support:
// one ordinary table scan, no relational shape that can multiply/reorder rows,
// and a physical primary-key equality whose value is execution-bound.
func preparedPKAdmissionReason(p *plan.Plan) string {
	query := p.GetQuery()
	if query == nil {
		return "no-query"
	}
	switch query.GetStmtType() {
	case plan.Query_SELECT, plan.Query_UPDATE, plan.Query_DELETE:
	default:
		return "unsupported-statement"
	}

	var scan *plan.Node
	for _, node := range query.GetNodes() {
		if node == nil {
			continue
		}
		if len(node.GetRuntimeFilterProbeList()) > 0 || len(node.GetRuntimeFilterBuildList()) > 0 {
			return "runtime-filter"
		}
		switch node.GetNodeType() {
		case plan.Node_TABLE_SCAN:
			if scan != nil {
				return "multiple-table-scans"
			}
			scan = node
		case plan.Node_AGG,
			plan.Node_DISTINCT,
			plan.Node_JOIN,
			plan.Node_SAMPLE,
			plan.Node_SORT,
			plan.Node_WINDOW,
			plan.Node_UNION,
			plan.Node_UNION_ALL,
			plan.Node_INTERSECT,
			plan.Node_INTERSECT_ALL,
			plan.Node_MINUS,
			plan.Node_MINUS_ALL,
			plan.Node_RECURSIVE_CTE,
			plan.Node_RECURSIVE_SCAN:
			return "unsupported-shape"
		}
	}
	if scan == nil || scan.GetTableDef() == nil {
		return "no-table-scan"
	}
	if scan.GetTableDef().GetIsTemporary() {
		return "temporary-table"
	}
	if ref := scan.GetObjRef(); ref != nil &&
		(ref.GetSubscriptionName() != "" || ref.GetPubInfo() != nil) {
		return "subscription-table"
	}
	pkey := scan.GetTableDef().GetPkey()
	if pkey == nil || pkey.GetPkeyColName() == "" ||
		catalog.IsFakePkName(pkey.GetPkeyColName()) {
		return "no-primary-key"
	}
	if len(scan.GetBindingTags()) != 1 {
		return "invalid-primary-key-metadata"
	}
	if preparedPKEqualityForTable(scan, scan.GetBindingTags()[0]) {
		return "eligible"
	}
	return "no-bound-primary-key-equality"
}

func preparedPKEqualityForTable(scan *plan.Node, bindingTag int32) bool {
	tableDef := scan.GetTableDef()
	pkey := tableDef.GetPkey()
	if pkey.GetPkeyColName() == catalog.CPrimaryKeyColName {
		return preparedCompositePKEquality(
			scan.GetFilterList(), bindingTag, tableDef.GetName2ColIndex(), pkey.GetNames())
	}
	pkPos, ok := tableDef.GetName2ColIndex()[pkey.GetPkeyColName()]
	if !ok {
		return false
	}
	for _, filter := range scan.GetFilterList() {
		if preparedPKEquality(filter, bindingTag, pkPos) {
			return true
		}
	}
	return false
}

func preparedCompositePKEquality(filters []*plan.Expr, bindingTag int32, colIndex map[string]int32, pkNames []string) bool {
	if len(pkNames) < 2 {
		return false
	}
	required := make(map[int32]struct{}, len(pkNames))
	for _, name := range pkNames {
		pos, ok := colIndex[name]
		if !ok {
			return false
		}
		required[pos] = struct{}{}
	}
	cpkeyPos, hasCPKey := colIndex[catalog.CPrimaryKeyColName]
	matched := make(map[int32]struct{}, len(required))
	var visit func(*plan.Expr) bool
	visit = func(expr *plan.Expr) bool {
		if expr == nil {
			return false
		}
		fn := expr.GetF()
		if fn == nil || fn.GetFunc() == nil {
			return false
		}
		if strings.EqualFold(fn.GetFunc().GetObjName(), "and") {
			if len(fn.GetArgs()) == 0 {
				return false
			}
			for _, arg := range fn.GetArgs() {
				if !visit(arg) {
					return false
				}
			}
			return true
		}
		if len(fn.GetArgs()) != 2 || fn.GetFunc().GetObjName() != "=" {
			return false
		}
		left, right := fn.GetArgs()[0], fn.GetArgs()[1]
		if col := left.GetCol(); col != nil && col.GetRelPos() == bindingTag {
			if _, ok := required[col.GetColPos()]; ok && preparedPKBoundValue(right) {
				matched[col.GetColPos()] = struct{}{}
				return true
			}
			if hasCPKey && col.GetColPos() == cpkeyPos &&
				preparedCompositeBoundValue(right, len(pkNames)) {
				for pos := range required {
					matched[pos] = struct{}{}
				}
				return true
			}
		}
		if col := right.GetCol(); col != nil && col.GetRelPos() == bindingTag {
			if _, ok := required[col.GetColPos()]; ok && preparedPKBoundValue(left) {
				matched[col.GetColPos()] = struct{}{}
				return true
			}
			if hasCPKey && col.GetColPos() == cpkeyPos &&
				preparedCompositeBoundValue(left, len(pkNames)) {
				for pos := range required {
					matched[pos] = struct{}{}
				}
				return true
			}
		}
		return false
	}
	for _, filter := range filters {
		if !visit(filter) {
			continue
		}
		if len(matched) == len(required) {
			return true
		}
	}
	return false
}

func preparedCompositeBoundValue(expr *plan.Expr, partCount int) bool {
	if expr == nil {
		return false
	}
	if expr.GetLit() != nil || expr.GetP() != nil || expr.GetFold() != nil {
		return true
	}
	fn := expr.GetF()
	if fn == nil || fn.GetFunc() == nil {
		return false
	}
	name := strings.ToLower(fn.GetFunc().GetObjName())
	if name != "serial" && name != "serial_full" || len(fn.GetArgs()) < partCount {
		return false
	}
	for _, arg := range fn.GetArgs() {
		if !preparedPKBoundValue(arg) {
			return false
		}
	}
	return true
}

func preparedPKEquality(expr *plan.Expr, bindingTag, pkPos int32) bool {
	fn := expr.GetF()
	if fn == nil || fn.GetFunc().GetObjName() != "=" || len(fn.GetArgs()) != 2 {
		return false
	}
	left, right := fn.GetArgs()[0], fn.GetArgs()[1]
	return preparedPKColumn(left, bindingTag, pkPos) && preparedPKBoundValue(right) ||
		preparedPKColumn(right, bindingTag, pkPos) && preparedPKBoundValue(left)
}

func preparedPKColumn(expr *plan.Expr, bindingTag, pkPos int32) bool {
	col := expr.GetCol()
	return col != nil && col.GetRelPos() == bindingTag && col.GetColPos() == pkPos
}

func preparedPKBoundValue(expr *plan.Expr) bool {
	if expr == nil {
		return false
	}
	if expr.GetLit() != nil || expr.GetP() != nil || expr.GetFold() != nil {
		return true
	}
	fn := expr.GetF()
	if fn == nil || fn.GetFunc() == nil {
		return false
	}
	name := strings.ToLower(fn.GetFunc().GetObjName())
	if name != "cast" && name != "serial" && name != "serial_full" {
		return false
	}
	if len(fn.GetArgs()) == 0 {
		return false
	}
	for _, arg := range fn.GetArgs() {
		if !preparedPKBoundValue(arg) {
			return false
		}
	}
	return true
}
