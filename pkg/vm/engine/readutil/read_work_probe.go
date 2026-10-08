// Copyright 2022 Matrix Origin
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

package readutil

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/matrixorigin/matrixone/pkg/defines"
	"github.com/matrixorigin/matrixone/pkg/logutil"
	"github.com/matrixorigin/matrixone/pkg/objectio"
	"github.com/matrixorigin/matrixone/pkg/vm/engine/tae/containers"
	"go.uber.org/zap"
)

const readWorkBlockLimit = 4096

var readWorkReaderID atomic.Uint64

type readWorkBlock struct {
	ID           string `json:"id"`
	InputRows    uint64 `json:"input_rows"`
	PKMatchRows  uint64 `json:"pk_match_rows"`
	ReturnedRows uint64 `json:"returned_rows"`
	SearchCalls  uint64 `json:"search_calls"`
	SearchNS     uint64 `json:"search_ns"`
	Error        bool   `json:"error"`
}

// Diagnostic-only, serial-reader-owned counters. No vectors or source resources
// are retained; logging is once at Close and collection fails closed on overflow.
type readWorkProbe struct {
	Reader          uint64          `json:"reader"`
	Label           string          `json:"label"`
	Execution       string          `json:"execution"`
	Table           string          `json:"table"`
	FakePK          bool            `json:"fake_pk"`
	FilterValid     bool            `json:"filter_valid"`
	PKType          string          `json:"pk_type"`
	FilterOp        int             `json:"filter_op"`
	MembershipSizes []int           `json:"membership_sizes"`
	HasBF           bool            `json:"has_bf"`
	Complete        bool            `json:"complete"`
	Overflow        bool            `json:"overflow"`
	Blocks          []readWorkBlock `json:"blocks"`
	active          readWorkBlock
}

func (r *reader) installReadWorkProbe(ctx context.Context, base BasePKFilter) {
	id, ok := ctx.Value(defines.ReadWorkProbeKey{}).(defines.ReadWorkProbe)
	if !ok || id.Label == "" || len(id.Label) > 32 || id.Execution == "" {
		return
	}
	p := &readWorkProbe{Reader: readWorkReaderID.Add(1), Label: id.Label, Execution: id.Execution, Table: r.name,
		FakePK: r.filterState.filter.HasFakePK, FilterValid: base.Valid,
		PKType: base.Oid.String(), FilterOp: base.Op, HasBF: r.filterState.hasBF}
	disjuncts := base.Disjuncts
	if len(disjuncts) == 0 {
		disjuncts = []BasePKFilter{base}
	}
	for _, f := range disjuncts {
		if f.Vec != nil {
			p.MembershipSizes = append(p.MembershipSizes, f.Vec.Length())
		}
	}
	r.readWorkProbe = p
	r.filterState.filter.SortedSearchFunc = p.wrap(r.filterState.filter.SortedSearchFunc)
	r.filterState.filter.UnSortedSearchFunc = p.wrap(r.filterState.filter.UnSortedSearchFunc)
}

func (p *readWorkProbe) wrap(search objectio.ReadFilterSearchFuncType) objectio.ReadFilterSearchFuncType {
	if search == nil {
		return nil
	}
	return func(vectors containers.Vectors) []int64 {
		if len(vectors) > 0 {
			p.active.InputRows += uint64(vectors[0].Length())
		}
		p.active.SearchCalls++
		start := time.Now()
		result := search(vectors)
		p.active.SearchNS += uint64(time.Since(start).Nanoseconds())
		p.active.PKMatchRows += uint64(len(result))
		return result
	}
}

func (p *readWorkProbe) beginBlock(id string) { p.active = readWorkBlock{ID: id} }

func (p *readWorkProbe) endBlock(rows int, failed bool) {
	p.active.ReturnedRows = uint64(rows)
	p.active.Error = failed
	if len(p.Blocks) < readWorkBlockLimit {
		p.Blocks = append(p.Blocks, p.active)
	} else {
		p.Overflow = true
	}
	p.active = readWorkBlock{}
}

func (r *reader) finishReadWorkProbe() {
	if r.readWorkProbe != nil {
		logutil.Info("ReadWorkProbe", zap.Any("probe", r.readWorkProbe))
		r.readWorkProbe = nil
	}
}
