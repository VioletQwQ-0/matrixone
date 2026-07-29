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

package table_function

import (
	"context"
	"errors"
	"sync"

	"github.com/matrixorigin/matrixone/pkg/common/docfilter"
	"github.com/matrixorigin/matrixone/pkg/common/hashmap"
	"github.com/matrixorigin/matrixone/pkg/common/moerr"
	"github.com/matrixorigin/matrixone/pkg/common/mpool"
	"github.com/matrixorigin/matrixone/pkg/container/hashtable"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/fulltext"
	"github.com/matrixorigin/matrixone/pkg/util/executor"
	"github.com/matrixorigin/matrixone/pkg/vectorindex"
	"github.com/matrixorigin/matrixone/pkg/vectorindex/sqlexec"
	"github.com/matrixorigin/matrixone/pkg/vm/process"
)

const (
	maxFulltextAndCandidates = uint64(262144)
	maxFulltextAndBytes      = uint64(64 << 20)
)

var errFulltextAndCapacity = errors.New("fulltext strict-and candidate capacity exceeded")

type fulltextAndCandidates struct {
	vec *vector.Vector
	hm  *hashmap.IntHashMap
}

func (c *fulltextAndCandidates) free(mp *mpool.MPool) {
	if c.hm != nil {
		c.hm.Free()
		c.hm = nil
	}
	if c.vec != nil {
		c.vec.Free(mp)
		c.vec = nil
	}
}

func (c *fulltextAndCandidates) bytes() uint64 {
	var n uint64
	if c.hm != nil && c.hm.Size() > 0 {
		n += uint64(c.hm.Size())
	}
	if c.vec != nil {
		n += uint64(c.vec.Allocated())
	}
	return n
}

func (c *fulltextAndCandidates) checkCapacity(extra uint64) error {
	if c.vec != nil && uint64(c.vec.Length()) > maxFulltextAndCandidates {
		return errFulltextAndCapacity
	}
	if c.bytes()+extra > maxFulltextAndBytes {
		return errFulltextAndCapacity
	}
	return nil
}

func (c *fulltextAndCandidates) init(typ *vector.Vector, mp *mpool.MPool) error {
	if c.vec != nil {
		return nil
	}
	if typ == nil || !docfilter.SupportsBitset(*typ.GetType()) {
		return moerr.NewInternalErrorNoCtx("fulltext strict-and requires an integer doc_id")
	}
	c.vec = vector.NewVec(*typ.GetType())
	var err error
	c.hm, err = hashmap.NewIntHashMap(false, mp)
	if err == nil {
		c.hm.SetResizeAdmission(func(plan hashtable.ResizePlan) (hashtable.ResizeReservation, error) {
			if uint64(c.vec.Allocated())+plan.ProjectedPeakBytes > maxFulltextAndBytes {
				return nil, errFulltextAndCapacity
			}
			return nil, nil
		})
	}
	return err
}

func (c *fulltextAndCandidates) addDriverVector(src *vector.Vector, mp *mpool.MPool) error {
	if err := c.init(src, mp); err != nil {
		return err
	}
	iterator := c.hm.NewIterator()
	for start := 0; start < src.Length(); start += hashmap.UnitLimit {
		count := min(hashmap.UnitLimit, src.Length()-start)
		before := c.hm.GroupCount()
		values, _, err := iterator.Insert(start, count, []*vector.Vector{src})
		if err != nil {
			return err
		}
		if c.hm.GroupCount() > maxFulltextAndCandidates {
			return errFulltextAndCapacity
		}
		newRows := int(c.hm.GroupCount() - before)
		if newRows > 0 {
			currentBytes := int64(c.vec.Allocated())
			requiredBytes := int64(c.vec.Length()+newRows) * int64(c.vec.GetType().TypeSize())
			projectedPeak := uint64(max(currentBytes, 0)) + uint64(max(c.hm.Size(), 0))
			if requiredBytes > currentBytes {
				newCapacity, ok := mpool.GrowCapacity(currentBytes, requiredBytes)
				if !ok || projectedPeak+uint64(newCapacity) > maxFulltextAndBytes {
					return errFulltextAndCapacity
				}
			}
			if err := c.vec.PreExtend(newRows, mp); err != nil {
				return err
			}
		}
		maxSeen := before
		for i, value := range values {
			if value > maxSeen {
				if err := c.vec.UnionOne(src, int64(start+i), mp); err != nil {
					return err
				}
				maxSeen = value
			}
		}
		if err := c.checkCapacity(0); err != nil {
			return err
		}
	}
	return nil
}

func buildFulltextAndMap(vec *vector.Vector, mp *mpool.MPool, otherBytes uint64) (*hashmap.IntHashMap, error) {
	if otherBytes+uint64(vec.Allocated())+hashtable.Int64HashMapInitialAllocationBytes() > maxFulltextAndBytes {
		return nil, errFulltextAndCapacity
	}
	hm, err := hashmap.NewIntHashMap(false, mp)
	if err != nil {
		return nil, err
	}
	hm.SetResizeAdmission(func(plan hashtable.ResizePlan) (hashtable.ResizeReservation, error) {
		if otherBytes+uint64(vec.Allocated())+plan.ProjectedPeakBytes > maxFulltextAndBytes {
			return nil, errFulltextAndCapacity
		}
		return nil, nil
	})
	iterator := hm.NewIterator()
	for start := 0; start < vec.Length(); start += hashmap.UnitLimit {
		count := min(hashmap.UnitLimit, vec.Length()-start)
		if _, _, err = iterator.Insert(start, count, []*vector.Vector{vec}); err != nil {
			hm.Free()
			return nil, err
		}
	}
	return hm, nil
}

func (c *fulltextAndCandidates) retainMatched(matched []uint8, extraBytes uint64, mp *mpool.MPool) (uint64, error) {
	next := vector.NewVec(*c.vec.GetType())
	projectedVectorBytes := uint64(c.vec.Length() * c.vec.GetType().TypeSize())
	if c.bytes()+projectedVectorBytes+extraBytes > maxFulltextAndBytes {
		return 0, errFulltextAndCapacity
	}
	if err := next.PreExtend(c.vec.Length(), mp); err != nil {
		next.Free(mp)
		return 0, err
	}
	if err := next.UnionBatch(c.vec, 0, c.vec.Length(), matched, mp); err != nil {
		next.Free(mp)
		return 0, err
	}
	nextMap, err := buildFulltextAndMap(next, mp, c.bytes()+extraBytes)
	if err != nil {
		next.Free(mp)
		return 0, err
	}
	peak := c.bytes() + uint64(next.Allocated()) + uint64(max(nextMap.Size(), 0)) + extraBytes
	c.hm.Free()
	c.vec.Free(mp)
	c.hm, c.vec = nextMap, next
	return peak, c.checkCapacity(0)
}

// streamFulltextPosting owns one internal SQL stream from start through close.
// On consumer failure it cancels the producer, closes every queued result, and
// waits for the executor goroutine before returning.
func streamFulltextPosting(
	proc *process.Process,
	sql string,
	membership []byte,
	consume func(*vector.Vector) error,
) (executor.Result, error) {
	streamCh := make(chan executor.Result, 8)
	errCh := make(chan error, 2)
	ctx, cancel := context.WithCancelCause(proc.GetTopContext())
	defer cancel(nil)
	var waiter sync.WaitGroup
	var final executor.Result
	var producerErr error
	waiter.Add(1)
	go func() {
		defer waiter.Done()
		defer close(streamCh)
		sqlProc := sqlexec.NewSqlProcess(proc)
		sqlProc.FulltextMembershipFilter = membership
		res, err := ft_runSql_streaming(ctx, sqlProc, sql, streamCh, errCh)
		final = res
		producerErr = err
	}()

	var consumeErr error
	closed := false
	for !closed && consumeErr == nil {
		select {
		case res, ok := <-streamCh:
			if !ok {
				closed = true
				break
			}
			for _, bat := range res.Batches {
				if len(bat.Vecs) != 1 {
					consumeErr = moerr.NewInternalError(proc.Ctx, "fulltext posting SQL must return one column")
					break
				}
				if err := consume(bat.Vecs[0]); err != nil {
					consumeErr = err
					break
				}
			}
			res.Close()
		case err := <-errCh:
			consumeErr = err
		case <-proc.Ctx.Done():
			consumeErr = moerr.NewInternalError(proc.Ctx, "fulltext posting scan cancelled")
		}
	}
	if consumeErr != nil {
		cancel(consumeErr)
	}
	if !closed {
		for res := range streamCh {
			res.Close()
		}
	}
	waiter.Wait()
	if consumeErr == nil {
		select {
		case consumeErr = <-errCh:
		default:
		}
	}
	if consumeErr == nil && proc.Ctx.Err() != nil {
		consumeErr = moerr.NewInternalError(proc.Ctx, "fulltext posting scan cancelled")
	}
	if consumeErr == nil {
		consumeErr = producerErr
	}
	return final, consumeErr
}

func runFulltextAndIntersection(
	u *fulltextState,
	proc *process.Process,
	tableFunction *TableFunction,
	s *fulltext.SearchAccum,
) (bool, error) {
	stats := tableFunction.OpAnalyzer.GetOpStats()
	if len(u.strictAndTerms) < 2 || u.estimatedDriver > maxFulltextAndCandidates {
		stats.AddExtraStat("FulltextFallbackCount", 1)
		return false, nil
	}

	candidates := &fulltextAndCandidates{}
	defer candidates.free(proc.Mp())
	var postingRows int64
	var driverDocs int64
	peakBytes := uint64(0)
	for termIndex, term := range u.strictAndTerms {
		var matched []uint8
		var membership []byte
		if termIndex > 0 {
			if candidates.vec == nil || candidates.vec.Length() == 0 {
				break
			}
			var err error
			membership, err = docfilter.Build(candidates.vec)
			if err != nil {
				return false, err
			}
			matched, err = mpool.MakeSlice[uint8](candidates.vec.Length(), proc.Mp(), true)
			if err != nil {
				return false, err
			}
			if err = candidates.checkCapacity(uint64(len(matched) + len(membership))); err != nil {
				mpool.FreeSlice(proc.Mp(), matched)
				stats.AddExtraStat("FulltextFallbackCount", 1)
				return false, nil
			}
		}

		planResult, err := streamFulltextPosting(proc, fulltext.ExactTermPostingSQL(s.TblName, term), membership, func(docIDs *vector.Vector) error {
			postingRows += int64(docIDs.Length())
			if termIndex == 0 {
				return candidates.addDriverVector(docIDs, proc.Mp())
			}
			iterator := candidates.hm.NewIterator()
			for start := 0; start < docIDs.Length(); start += hashmap.UnitLimit {
				count := min(hashmap.UnitLimit, docIDs.Length()-start)
				values, _ := iterator.Find(start, count, []*vector.Vector{docIDs})
				for _, value := range values {
					if value > 0 {
						matched[value-1] = 1
					}
				}
			}
			return nil
		})
		stats.BackgroundQueries = append(stats.BackgroundQueries, planResult.LogicalPlan)
		planResult.Close()
		if termIndex > 0 && matched != nil {
			if err == nil {
				var retainPeak uint64
				retainPeak, err = candidates.retainMatched(matched, uint64(len(membership)+len(matched)), proc.Mp())
				peakBytes = max(peakBytes, retainPeak)
			}
			mpool.FreeSlice(proc.Mp(), matched)
		}
		if errors.Is(err, errFulltextAndCapacity) {
			stats.AddExtraStat("FulltextPostingRows", postingRows)
			stats.SetMaxExtraStat("FulltextCandidatePeakBytes", int64(max(peakBytes, candidates.bytes())))
			if candidates.hm != nil {
				stats.AddExtraStat("FulltextDriverDocs", int64(candidates.hm.GroupCount()))
			}
			stats.AddExtraStat("FulltextFallbackCount", 1)
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if termIndex == 0 && candidates.hm != nil {
			driverDocs = int64(candidates.hm.GroupCount())
		}
		peakBytes = max(peakBytes, candidates.bytes()+uint64(len(membership)))
	}

	if candidates.vec != nil {
		limit := min(uint64(candidates.vec.Length()), u.limit)
		u.resbuf = make([]*vectorindex.SearchResultAnyKey, 0, limit)
		for i := range int(limit) {
			u.resbuf = append(u.resbuf, &vectorindex.SearchResultAnyKey{
				Id:       vector.GetAny(candidates.vec, i, false),
				Distance: 0,
			})
		}
	}
	stats.AddExtraStat("FulltextDriverDocs", driverDocs)
	stats.AddExtraStat("FulltextFastPathCount", 1)
	stats.AddExtraStat("FulltextPostingRows", postingRows)
	stats.SetMaxExtraStat("FulltextCandidatePeakBytes", int64(peakBytes))
	return true, nil
}
