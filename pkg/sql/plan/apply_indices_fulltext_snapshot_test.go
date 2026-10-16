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

package plan

// ScanSnapshot propagation into the fulltext2 index search scan built by the covered
// fast path (#27941).

import (
	"testing"

	"github.com/matrixorigin/matrixone/pkg/catalog"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	ft2plan "github.com/matrixorigin/matrixone/pkg/fulltext2/plugin/plan"
	"github.com/matrixorigin/matrixone/pkg/pb/plan"
	"github.com/matrixorigin/matrixone/pkg/pb/timestamp"
	"github.com/matrixorigin/matrixone/pkg/sql/parsers/tree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coveredFulltext2Fixture builds the minimal shape that clears every tryApplyCoveredFulltext2
// guard: one MATCH filter, one fulltext2 index with an INCLUDE column, no residual predicate,
// and a pk-only projection. snapshot is attached to the base scan.
func coveredFulltext2Fixture(t *testing.T, snapshot *plan.Snapshot) (
	builder *QueryBuilder, nodeID int32, projNode, sortNode, scanNode *plan.Node, idxdef *plan.IndexDef,
) {
	t.Helper()

	builder = NewQueryBuilder(plan.Query_SELECT, NewMockCompilerContext(true, newPlanTestProcess(t)), false, true)
	bindCtx := NewBindContext(builder, nil)

	scanTag := builder.genNewBindTag()
	scanNode = &plan.Node{
		NodeType: plan.Node_TABLE_SCAN,
		ObjRef:   &plan.ObjectRef{SchemaName: "db"},
		TableDef: &plan.TableDef{
			Name: "t",
			Cols: []*plan.ColDef{
				{Name: "id", Typ: plan.Type{Id: int32(types.T_int64), Width: 64}},
				{Name: "body", Typ: plan.Type{Id: int32(types.T_varchar), Width: 256}},
				{Name: "tag", Typ: plan.Type{Id: int32(types.T_int64), Width: 64}},
			},
			Pkey:          &plan.PrimaryKeyDef{PkeyColName: "id"},
			Name2ColIndex: map[string]int32{"id": 0, "body": 1, "tag": 2},
			Indexes: []*plan.IndexDef{
				{IndexName: "ft2idx", IndexAlgoTableType: catalog.FullText2Index_TblType_Storage, IndexTableName: "__store"},
				{IndexName: "ft2idx", IndexAlgoTableType: catalog.FullText2Index_TblType_Metadata, IndexTableName: "__meta"},
			},
		},
		BindingTags:  []int32{scanTag},
		ScanSnapshot: snapshot,
		// match(body, 'x' IN NATURAL LANGUAGE MODE)
		FilterList: []*plan.Expr{{
			Typ: plan.Type{Id: int32(types.T_float32)},
			Expr: &plan.Expr_F{F: &plan.Function{
				Func: &ObjectRef{ObjName: "match_against"},
				Args: []*plan.Expr{
					makePlan2StringConstExprWithType("x"),
					{
						Typ:  plan.Type{Id: int32(types.T_int64)},
						Expr: &plan.Expr_Lit{Lit: &plan.Literal{Value: &plan.Literal_I64Val{I64Val: int64(tree.FULLTEXT_NL)}}},
					},
				},
			}},
		}},
	}
	nodeID = builder.appendNode(scanNode, bindCtx)
	for i := 0; i < 10; i++ {
		builder.ctxByNode = append(builder.ctxByNode, bindCtx)
	}

	projNode = &plan.Node{
		NodeType: plan.Node_PROJECT,
		Children: []int32{nodeID},
		// pk only
		ProjectList: []*plan.Expr{{
			Typ:  plan.Type{Id: int32(types.T_int64), Width: 64},
			Expr: &plan.Expr_Col{Col: &plan.ColRef{RelPos: scanTag, ColPos: 0}},
		}},
	}
	sortNode = &plan.Node{NodeType: plan.Node_SORT, Children: []int32{nodeID}}

	idxdef = &plan.IndexDef{
		IndexName:       "ft2idx",
		IndexAlgo:       catalog.MoIndexFullText2Algo.ToString(),
		IncludedColumns: []string{"tag"},
	}
	return
}

// The covered path's fulltext2 index search scan carries a deep copy of the base scan's
// snapshot, on the node and on its IndexSearchScan.
func TestTryApplyCoveredFulltext2PropagatesScanSnapshot(t *testing.T) {
	snapshot := &plan.Snapshot{TS: &timestamp.Timestamp{PhysicalTime: 1700000000, LogicalTime: 7}}
	builder, nodeID, projNode, sortNode, scanNode, idxdef := coveredFulltext2Fixture(t, snapshot)

	handled, err := builder.tryApplyCoveredFulltext2(nodeID, projNode, sortNode, scanNode,
		[]int32{0}, []*plan.IndexDef{idxdef}, nil, nil, map[int32]int32{}, nil, nil)
	require.NoError(t, err)
	require.True(t, handled, "the fixture must clear every covered-path guard")

	node := findCoveredFulltext2Scan(t, builder)
	for _, got := range []*plan.Snapshot{node.ScanSnapshot, node.IndexSearchScan.ScanSnapshot} {
		require.NotNil(t, got)
		require.NotNil(t, got.TS)
		assert.Equal(t, snapshot.TS.PhysicalTime, got.TS.PhysicalTime)
		assert.Equal(t, snapshot.TS.LogicalTime, got.TS.LogicalTime)
		assert.NotSame(t, snapshot, got, "must be a deep copy, not the scan node's own Snapshot")
		assert.NotSame(t, snapshot.TS, got.TS, "the TS must be deep-copied too")
	}
}

// No snapshot on the base scan leaves the search scan's ScanSnapshot nil.
func TestTryApplyCoveredFulltext2NoSnapshotLeavesSearchUnsnapshotted(t *testing.T) {
	builder, nodeID, projNode, sortNode, scanNode, idxdef := coveredFulltext2Fixture(t, nil)

	handled, err := builder.tryApplyCoveredFulltext2(nodeID, projNode, sortNode, scanNode,
		[]int32{0}, []*plan.IndexDef{idxdef}, nil, nil, map[int32]int32{}, nil, nil)
	require.NoError(t, err)
	require.True(t, handled)

	node := findCoveredFulltext2Scan(t, builder)
	assert.Nil(t, node.ScanSnapshot)
	assert.Nil(t, node.IndexSearchScan.ScanSnapshot)
}

func TestCoveredFulltext2ScoreLimitOnlyForEquivalentSort(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keys      []*plan.OrderBySpec
		foundRows bool
		wantLimit uint64
	}{
		{name: "score descending", wantLimit: 200},
		{name: "score ascending", keys: []*plan.OrderBySpec{{Flag: plan.OrderBySpec_ASC}}},
		{name: "different projected key", keys: []*plan.OrderBySpec{{Flag: plan.OrderBySpec_DESC,
			Expr: GetColExpr(plan.Type{Id: int32(types.T_int64)}, 0, 0)}}},
		{name: "second key", keys: []*plan.OrderBySpec{{Flag: plan.OrderBySpec_DESC}, {Flag: plan.OrderBySpec_DESC}}},
		{name: "explicit null placement", keys: []*plan.OrderBySpec{{Flag: plan.OrderBySpec_DESC | plan.OrderBySpec_NULLS_FIRST}}},
		{name: "found rows", foundRows: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			builder, nodeID, projNode, sortNode, scanNode, idxdef := coveredFulltext2Fixture(t, nil)
			ctx := builder.ctxByNode[nodeID]
			ctx.projectTag = builder.genNewBindTag()
			projNode.ProjectList = append(projNode.ProjectList, DeepCopyExpr(scanNode.FilterList[0]))
			scoreKey := &plan.OrderBySpec{
				Expr: GetColExpr(plan.Type{Id: int32(types.T_float32)}, ctx.projectTag, 1),
				Flag: plan.OrderBySpec_DESC | plan.OrderBySpec_INTERNAL,
			}
			scanNode.FilterList = append(scanNode.FilterList, fnExpr("is_null", &plan.Expr{
				Typ:  plan.Type{Id: int32(types.T_int64)},
				Expr: &plan.Expr_Col{Col: &plan.ColRef{RelPos: scanNode.BindingTags[0], ColPos: 2, Name: "tag"}},
			}))
			if tc.keys == nil {
				sortNode.OrderBy = []*plan.OrderBySpec{scoreKey}
			} else {
				for _, key := range tc.keys {
					if key.Expr == nil {
						key.Expr = DeepCopyExpr(scoreKey.Expr)
					} else {
						key.Expr.GetCol().RelPos = ctx.projectTag
					}
				}
				sortNode.OrderBy = tc.keys
			}
			builder.sqlCalcFoundRows = tc.foundRows
			handled, err := builder.tryApplyCoveredFulltext2(nodeID, projNode, sortNode, scanNode,
				[]int32{0}, []*plan.IndexDef{idxdef}, []int32{1}, nil,
				map[int32]int32{0: 0}, makePlan2Uint64ConstExprWithType(200), nil)
			require.NoError(t, err)
			require.True(t, handled)
			search := findCoveredFulltext2Scan(t, builder)
			opts, err := ft2plan.DecodeScanOptions(search.IndexSearchScan.AlgoOptions)
			require.NoError(t, err)
			assert.Contains(t, opts.IncludePreds, `"op":"is_null"`)
			if tc.wantLimit == 0 {
				require.Nil(t, fulltextScanLimit(search))
			} else {
				require.NotNil(t, fulltextScanLimit(search))
				assert.Equal(t, tc.wantLimit, fulltextScanLimit(search).GetLit().GetU64Val())
			}
		})
	}
}

func TestCoveredFulltext2ScoreLimitOnBoundSQL(t *testing.T) {
	for _, tc := range []struct {
		name       string
		orderBy    string
		pagination string
		prefix     string
		wantLimit  uint64
	}{
		{name: "score alias", orderBy: "sc DESC", pagination: "LIMIT 2", wantLimit: 2},
		{name: "score alias offset", orderBy: "sc DESC", pagination: "LIMIT 2 OFFSET 1", wantLimit: 3},
		{name: "ascending score", orderBy: "sc ASC", pagination: "LIMIT 2"},
		{name: "different sort", orderBy: "id DESC", pagination: "LIMIT 2"},
		{name: "score with second key", orderBy: "sc DESC, id", pagination: "LIMIT 2"},
		{name: "score expression", orderBy: "sc + 1 DESC", pagination: "LIMIT 2"},
		{name: "no limit", orderBy: "sc DESC"},
		{name: "overflow", orderBy: "sc DESC", pagination: "LIMIT 18446744073709551615 OFFSET 1"},
		{name: "found rows", prefix: "SQL_CALC_FOUND_ROWS ", orderBy: "sc DESC", pagination: "LIMIT 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			optimizer := newIssue24822FullText2Optimizer(t)
			for _, idx := range optimizer.ctx.tables["ft"].Indexes {
				idx.IncludedColumns = []string{"base_id"}
			}
			statement := `SELECT ` + tc.prefix + `id, MATCH(title, body) AGAINST('hello' IN BOOLEAN MODE) AS sc
				FROM ft WHERE MATCH(title, body) AGAINST('hello' IN BOOLEAN MODE)
				AND base_id IS NULL ORDER BY ` + tc.orderBy + ` ` + tc.pagination
			p, err := runOneStmt(optimizer, t, statement)
			require.NoError(t, err)
			var search *plan.Node
			for _, node := range p.GetQuery().Nodes {
				if isFulltext2SearchScan(node) {
					require.Nil(t, search)
					search = node
				}
			}
			require.NotNil(t, search)
			opts, err := ft2plan.DecodeScanOptions(search.IndexSearchScan.AlgoOptions)
			require.NoError(t, err)
			assert.Contains(t, opts.IncludePreds, `"op":"is_null"`)
			if tc.wantLimit != 0 {
				require.NotNil(t, fulltextScanLimit(search))
				assert.Equal(t, tc.wantLimit, fulltextScanLimit(search).GetLit().GetU64Val())
			} else {
				require.Nil(t, fulltextScanLimit(search))
			}
		})
	}
}

// The ANLI client uses PyMySQL's text protocol: %s is interpolated before the
// statement reaches MO. Q1 and the first Q3 stage send this same score alias,
// SHOULD query, NULL eligibility and literal Top200 shape.
func TestCoveredFulltext2ANLIQueryStages(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(map[bool]string{false: "B residual eligibility", true: "C covered eligibility"}[include], func(t *testing.T) {
			optimizer := newIssue24822FullText2Optimizer(t)
			def := optimizer.ctx.tables["ft"]
			def.Cols = []*plan.ColDef{
				{Name: "md5_id", Typ: plan.Type{Id: int32(types.T_varchar), Width: 255}},
				{Name: "question", Typ: plan.Type{Id: int32(types.T_text)}},
				{Name: "answer", Typ: plan.Type{Id: int32(types.T_json)}},
				{Name: "allow_access", Typ: plan.Type{Id: int32(types.T_varchar), Width: 511}},
				{Name: "allow_identities", Typ: plan.Type{Id: int32(types.T_varchar), Width: 512}},
				{Name: "content_type", Typ: plan.Type{Id: int32(types.T_varchar), Width: 255}},
				{Name: "delete_flag", Typ: plan.Type{Id: int32(types.T_int32)}},
			}
			def.Name2ColIndex = make(map[string]int32, len(def.Cols))
			for i, col := range def.Cols {
				def.Name2ColIndex[col.Name] = int32(i)
			}
			def.Pkey = &plan.PrimaryKeyDef{PkeyColName: "md5_id", Names: []string{"md5_id"}}
			for _, index := range def.Indexes {
				index.Parts = []string{"question"}
				index.IndexAlgoParams = `{"parser":"ngram"}`
				if include {
					index.IncludedColumns = []string{"delete_flag"}
				}
			}
			for _, pattern := range []string{"中国 人民", "中国 alpha", "alpha beta"} {
				p, err := runOneStmt(optimizer, t, "SELECT md5_id, MATCH(question) AGAINST ('"+pattern+"' IN BOOLEAN MODE) AS vec_dist FROM ft WHERE MATCH(question) AGAINST ('"+pattern+"' IN BOOLEAN MODE) AND delete_flag IS NULL ORDER BY vec_dist DESC LIMIT 200")
				require.NoError(t, err)
				var scans []*plan.Node
				for _, n := range p.GetQuery().Nodes {
					if isFulltext2SearchScan(n) {
						scans = append(scans, n)
					}
				}
				require.Len(t, scans, 1)
				if include {
					require.NotNil(t, fulltextScanLimit(scans[0]))
					require.EqualValues(t, 200, fulltextScanLimit(scans[0]).GetLit().GetU64Val())
					opts, err := ft2plan.DecodeScanOptions(scans[0].IndexSearchScan.AlgoOptions)
					require.NoError(t, err)
					require.Contains(t, opts.IncludePreds, `"op":"is_null"`)
				} else {
					require.Nil(t, fulltextScanLimit(scans[0]), "the upper SQL LIMIT cannot truncate before residual eligibility")
				}
			}
			p, err := runOneStmt(optimizer, t, "SELECT md5_id, question, answer FROM ft WHERE md5_id IN ('a', 'b') AND (locate('OPEN', allow_access) > 0 OR (locate('ABO', allow_access) > 0 AND locate('SA_O', allow_identities) > 0)) AND locate('日更', content_type) = 0 AND locate('直播', content_type) = 0 ORDER BY FIELD(md5_id, 'a', 'b') LIMIT 30")
			require.NoError(t, err)
			for _, node := range p.GetQuery().Nodes {
				require.False(t, isFulltext2SearchScan(node), "Q3 second stage has no MATCH search")
			}
		})
	}
}

// findCoveredFulltext2Scan returns the single fulltext2 index search scan appended by the
// rewrite.
func findCoveredFulltext2Scan(t *testing.T, builder *QueryBuilder) *plan.Node {
	t.Helper()
	var found *plan.Node
	for _, n := range builder.qry.Nodes {
		if isFulltext2SearchScan(n) {
			require.Nil(t, found, "expected exactly one fulltext2 index search scan")
			found = n
		}
	}
	require.NotNil(t, found, "the covered rewrite must append a fulltext2 index search scan")
	return found
}
