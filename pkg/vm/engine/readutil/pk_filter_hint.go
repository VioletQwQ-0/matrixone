// Copyright 2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package readutil

import (
	"github.com/matrixorigin/matrixone/pkg/common/mpool"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/pb/plan"
	"github.com/matrixorigin/matrixone/pkg/sql/plan/function"
	"github.com/matrixorigin/matrixone/pkg/vm/engine"
)

// BuildPrimaryKeyHint extracts the narrow execution-time fast path used by
// range and reader construction. Unsupported predicates deliberately return
// an invalid hint so both callers keep using the existing expression path.
func BuildPrimaryKeyHint(
	expr *plan.Expr,
	tableDef *plan.TableDef,
	mp *mpool.MPool,
) (engine.PrimaryKeyHint, error) {
	if tableDef == nil || tableDef.Pkey == nil {
		return engine.PrimaryKeyHint{}, nil
	}
	base, err := ConstructBasePKFilter(expr, tableDef, mp)
	if err != nil {
		// The hint is optional. Preserve the existing reader-time behavior for
		// malformed or unsupported expressions instead of moving the error into
		// reusable-scope compilation.
		return engine.PrimaryKeyHint{}, nil
	}
	defer base.Cleanup()

	if !base.Valid || base.Op != function.EQUAL || base.Vec != nil ||
		len(base.Disjuncts) != 0 {
		return engine.PrimaryKeyHint{}, nil
	}

	pkCol := primaryKeyColumn(tableDef)
	if !validPrimaryKeyHint(engine.PrimaryKeyHint{
		Valid: true,
		Value: base.LB,
		Oid:   base.Oid,
	}, pkCol) {
		return engine.PrimaryKeyHint{}, nil
	}

	value := make([]byte, len(base.LB))
	copy(value, base.LB)
	return engine.PrimaryKeyHint{
		Valid: true,
		Value: value,
		Oid:   base.Oid,
	}, nil
}

func validPrimaryKeyHint(hint engine.PrimaryKeyHint, pkCol *plan.ColDef) bool {
	if !hint.Valid || pkCol == nil || types.T(pkCol.Typ.Id) != hint.Oid || hint.Oid.IsDecimal() {
		return false
	}
	if width := hint.Oid.FixedLength(); width > 0 && len(hint.Value) != width {
		return false
	}
	return true
}

func primaryKeyColumn(tableDef *plan.TableDef) *plan.ColDef {
	if tableDef == nil || tableDef.Pkey == nil {
		return nil
	}
	name := tableDef.Pkey.PkeyColName
	if pos, ok := tableDef.Name2ColIndex[name]; ok && pos >= 0 && int(pos) < len(tableDef.Cols) {
		return tableDef.Cols[pos]
	}
	for _, col := range tableDef.Cols {
		if col.Name == name {
			return col
		}
	}
	return nil
}

func basePKFilterFromHint(hint engine.PrimaryKeyHint, tableDef *plan.TableDef) BasePKFilter {
	if !validPrimaryKeyHint(hint, primaryKeyColumn(tableDef)) {
		return BasePKFilter{}
	}
	return BasePKFilter{
		Valid: true,
		Op:    function.EQUAL,
		LB:    hint.Value,
		Oid:   hint.Oid,
	}
}
