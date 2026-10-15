// Copyright 2026 Matrix Origin
// Experiment-only public-execution observation. Never a performance subject.
package compile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/pb/plan"
	"github.com/matrixorigin/matrixone/pkg/util/trace"
)

var issue29322ScanObservations atomic.Int64

func issue29322ExprShape(expr *plan.Expr, depth int) any {
	if expr == nil {
		return nil
	}
	if depth > 12 {
		return "DEPTH_BOUND"
	}
	result := map[string]any{"type": types.T(expr.Typ.Id).String()}
	if col := expr.GetCol(); col != nil {
		result["column"] = col.Name
		result["position"] = col.ColPos
	}
	if param := expr.GetP(); param != nil {
		result["marker"] = param.Pos
	}
	if vec := expr.GetVec(); vec != nil {
		result["vector_length"] = vec.Len
		result["serialized"] = vec.IsSerialized
	}
	if fold := expr.GetFold(); fold != nil {
		result["fold_id"] = fold.Id
		result["fold_constant"] = fold.IsConst
		result["fold_data_length"] = len(fold.Data)
		if !fold.IsConst && len(fold.Data) <= 65536 {
			vec := vector.NewVec(types.T_any.ToType())
			if vec.UnmarshalBinary(fold.Data) == nil && vec.GetType().Oid.IsMySQLString() && vec.Length() <= 128 {
				prefixes := map[uint8]int{}
				for i := 0; i < vec.Length(); i++ {
					if vec.IsNull(uint64(i)) {
						continue
					}
					data := vec.GetBytesAt(i)
					if len(data) > 0 {
						prefixes[data[0]]++
					}
				}
				result["fold_vector_length"] = vec.Length()
				result["fold_key_prefixes"] = prefixes
			}
		}
	}
	if fn := expr.GetF(); fn != nil {
		result["syntax_explicit_cast"] = fn.GetSyntaxExplicitCast()
		if fn.Func != nil {
			result["function"] = fn.Func.ObjName
			result["function_object_id"] = fn.Func.Obj
		}
		args := make([]any, 0, len(fn.Args))
		for _, arg := range fn.Args {
			args = append(args, issue29322ExprShape(arg, depth+1))
		}
		result["args"] = args
	}
	if list := expr.GetList(); list != nil {
		args := make([]any, 0, len(list.List))
		for _, arg := range list.List {
			args = append(args, issue29322ExprShape(arg, depth+1))
		}
		result["list"] = args
	}
	return result
}

func (c *Compile) issue29322ObserveScan(node *plan.Node, storage, folded, blocks []*plan.Expr) {
	if os.Getenv("MO_ISSUE29322_SCAN_PROBE") != "true" || node.ObjRef == nil ||
		!(strings.Contains(strings.ToLower(node.ObjRef.SchemaName), "issue29322") || (os.Getenv("MO_ISSUE29322_ATTRIBUTION10M") == "true" && node.ObjRef.SchemaName == "sysbench_db")) {
		return
	}
	if issue29322ScanObservations.Add(1) > 65536 {
		fmt.Println("ISSUE29322_PROBE_TRUNCATED scan")
		return
	}
	shapes := func(exprs []*plan.Expr) []any {
		result := make([]any, 0, len(exprs))
		for _, expr := range exprs {
			result = append(result, issue29322ExprShape(expr, 0))
		}
		return result
	}
	bindings := []string{}
	if params := c.proc.GetPrepareParams(); params != nil && params.Length() <= 128 {
		for i := 0; i < params.Length(); i++ {
			bindings = append(bindings, c.proc.GetPrepareParamType(i).String())
		}
	}
	probes := []any{}
	for _, spec := range node.RuntimeFilterProbeList {
		probes = append(probes, map[string]any{"expr": issue29322ExprShape(spec.Expr, 0), "not_on_pk": spec.NotOnPk, "membership": spec.UseMembershipFilter, "encoding": spec.KeyEncoding.String()})
	}
	statementID := ""
	if c.proc != nil && c.proc.GetStmtProfile() != nil {
		statementID = c.proc.GetStmtProfile().GetStmtId().String()
	}
	queryHash := sha256.Sum256([]byte(c.sql))
	value := map[string]any{"trace_id": trace.SpanFromContext(c.proc.Ctx).SpanContext().TraceID.String(), "statement_id": statementID, "connection_id": c.proc.GetSessionInfo().ConnectionID, "query_phase": os.Getenv("MO_ISSUE29322_QUERY_PHASE"), "query_sha256": hex.EncodeToString(queryHash[:]), "explicit_int_cast": strings.Contains(strings.ToLower(c.sql), "cast(? as int)"), "ignore_index": strings.Contains(strings.ToLower(c.sql), "ignore index"), "table": node.TableDef.Name, "bindings": bindings, "logical": shapes(node.FilterList), "storage_selected": shapes(storage), "storage_folded": shapes(folded), "blocks_folded": shapes(blocks), "runtime_probes": probes}
	encoded, err := json.Marshal(value)
	if err == nil && len(encoded) <= 65536 {
		fmt.Println("ISSUE29322_SCAN_OBSERVATION " + string(encoded))
	}
}
