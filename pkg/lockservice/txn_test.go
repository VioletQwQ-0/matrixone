// Copyright 2023 Matrix Origin
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

package lockservice

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matrixorigin/matrixone/pkg/common/moerr"
	"github.com/matrixorigin/matrixone/pkg/common/reuse"
	"github.com/matrixorigin/matrixone/pkg/common/runtime"
	pb "github.com/matrixorigin/matrixone/pkg/pb/lock"
	"github.com/matrixorigin/matrixone/pkg/pb/timestamp"
	"github.com/stretchr/testify/assert"
)

func TestLockAdded(t *testing.T) {
	reuse.RunReuseTests(func() {
		id := []byte("t1")
		fsp := newFixedSlicePool(2)
		txn := newActiveTxn(id, string(id), fsp, "")
		defer reuse.Free(txn, nil)

		err := txn.lockAdded(0, pb.LockTable{Table: 1}, [][]byte{[]byte("k1")}, getLogger(""))
		assert.NoError(t, err)
		err = txn.lockAdded(0, pb.LockTable{Table: 1}, [][]byte{[]byte("k11")}, getLogger(""))
		assert.NoError(t, err)
		err = txn.lockAdded(0, pb.LockTable{Table: 2}, [][]byte{[]byte("k2"), []byte("k22")}, getLogger(""))
		assert.NoError(t, err)
		assert.Equal(t, 2, len(txn.getHoldLocksLocked(0).tableKeys))

		sp := txn.getHoldLocksLocked(0).tableKeys[1]
		s := sp.slice()
		defer s.unref()
		assert.Equal(t, 2, s.len())

		sp2 := txn.getHoldLocksLocked(0).tableKeys[2]
		s2 := sp2.slice()
		defer s2.unref()
		assert.Equal(t, 2, s2.len())
	})
}

func TestLockAddedThatShouldFail(t *testing.T) {
	reuse.RunReuseTests(func() {
		id := []byte("t1")
		fsp := newFixedSlicePool(2)
		txn := newActiveTxn(id, string(id), fsp, "")
		defer reuse.Free(txn, nil)
		err := txn.lockAdded(0, pb.LockTable{Table: 1}, [][]byte{[]byte("k2"), []byte("k22"), []byte("k222")}, getLogger(""))
		assert.Error(t, err)
		assert.True(t, moerr.IsMoErrCode(err, moerr.ErrLockNeedUpgrade))
	})
}

func TestLockTableBindTouchedTracksFenceIntentOnly(t *testing.T) {
	reuse.RunReuseTests(func() {
		id := []byte("t1")
		fsp := newFixedSlicePool(2)
		txn := newActiveTxn(id, string(id), fsp, "")
		defer reuse.Free(txn, nil)

		bind := pb.LockTable{Group: 0, Table: 1, ServiceID: "s1", Version: 1}
		txn.lockTableBindTouched(bind)

		h := txn.getHoldLocksLocked(bind.Group)
		assert.Empty(t, h.tableBinds)
		assert.Equal(t, bind, h.tableBindIntents[bind.Table])

		refs := make(map[uint32]map[uint64]uint64)
		txn.incLockTableRef(refs, bind.ServiceID)
		assert.Empty(t, refs)

		changed := bind
		changed.Version++
		assert.True(t, txn.fenceByBindChanged(changed, getLogger("")))
		assert.True(t, txn.bindChanged)

		txn.reset()
		assert.Empty(t, txn.lockHolders)
	})
}

func TestClose(t *testing.T) {
	reuse.RunReuseTests(func() {
		events := newWaiterEvents(1, nil, nil, time.Second, nil, getLogger(""))
		defer events.close()

		id := []byte("t1")
		fsp := newFixedSlicePool(2)
		txn := newActiveTxn(id, string(id), fsp, "")
		tables := map[uint64]lockTable{
			1: newLocalLockTable(pb.LockTable{Table: 1}, nil, events, runtime.DefaultRuntime().Clock(), nil, getLogger("")),
			2: newLocalLockTable(pb.LockTable{Table: 2}, nil, events, runtime.DefaultRuntime().Clock(), nil, getLogger("")),
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()

		tables[1].lock(ctx, txn, [][]byte{[]byte("k1")}, LockOptions{}, func(r pb.Result, err error) {
			assert.NoError(t, err)
		})

		tables[2].lock(ctx, txn, [][]byte{[]byte("k2")}, LockOptions{}, func(r pb.Result, err error) {
			assert.NoError(t, err)
		})

		txn.close(
			txn.txnID,
			timestamp.Timestamp{},
			func(group uint32, table uint64) (lockTable, error) {
				return tables[table], nil
			},
			getLogger(""),
		)
		assert.Empty(t, txn.txnID)
		assert.Empty(t, txn.txnKey)
		assert.Empty(t, txn.blockedWaiters)
		assert.Empty(t, txn.getHoldLocksLocked(0).tableKeys)
		assert.Empty(t, txn.getHoldLocksLocked(0).tableBinds)
		assert.Equal(t, 0, tables[1].(*localLockTable).mu.store.Len())
		assert.Equal(t, 0, tables[2].(*localLockTable).mu.store.Len())
	})
}

type closeTestProbe struct {
	active     atomic.Int32
	maxActive  atomic.Int32
	calls      atomic.Int32
	entered    chan struct{}
	release    <-chan struct{}
	panicValue any
}

func (p *closeTestProbe) unlock() {
	if p == nil {
		return
	}
	p.calls.Add(1)
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		maxActive := p.maxActive.Load()
		if active <= maxActive || p.maxActive.CompareAndSwap(maxActive, active) {
			break
		}
	}
	if p.entered != nil {
		p.entered <- struct{}{}
	}
	if p.release != nil {
		<-p.release
	}
	if p.panicValue != nil {
		panic(p.panicValue)
	}
}

type closeTestLockTable struct {
	bind  pb.LockTable
	probe *closeTestProbe
	delay time.Duration
}

func (l *closeTestLockTable) lock(
	_ context.Context,
	_ *activeTxn,
	_ [][]byte,
	_ LockOptions,
	cb func(pb.Result, error),
) {
	cb(pb.Result{}, nil)
}

func (l *closeTestLockTable) unlock(
	_ *activeTxn,
	_ *cowSlice,
	_ timestamp.Timestamp,
	_ ...pb.ExtraMutation,
) {
	l.probe.unlock()
	if l.delay > 0 {
		time.Sleep(l.delay)
	}
}

func (l *closeTestLockTable) getLock(_ []byte, _ pb.WaitTxn, _ func(Lock)) {}

func (l *closeTestLockTable) getLockHolder(
	_ context.Context,
	_ []byte,
) (pb.WaitTxn, bool, error) {
	return pb.WaitTxn{}, false, nil
}

func (l *closeTestLockTable) getBind() pb.LockTable { return l.bind }

func (l *closeTestLockTable) close(_ closeReason) {}

func newCloseTestTxn(
	tb testing.TB,
	tableCount int,
	probe *closeTestProbe,
	delay time.Duration,
) (*activeTxn, map[uint64]lockTable) {
	tb.Helper()
	txnID := []byte("close-test-txn")
	fsp := newFixedSlicePool(16)
	txn := newActiveTxn(txnID, string(txnID), fsp, "")
	if err := addCloseTestHeldTables(txn, fsp, tableCount); err != nil {
		tb.Fatalf("create held locks: %v", err)
	}
	return txn, newCloseTestLockTables(tableCount, probe, delay)
}

func addCloseTestHeldTables(txn *activeTxn, fsp *fixedSlicePool, tableCount int) error {
	h := txn.getHoldLocksLocked(0)
	for i := 0; i < tableCount; i++ {
		tableID := uint64(i + 1)
		cs, err := newCowSlice(fsp, [][]byte{{byte(i + 1)}})
		if err != nil {
			return err
		}
		bind := pb.LockTable{Group: 0, Table: tableID}
		h.tableKeys[tableID] = cs
		h.tableBinds[tableID] = bind
	}
	return nil
}

func newCloseTestLockTables(
	tableCount int,
	probe *closeTestProbe,
	delay time.Duration,
) map[uint64]lockTable {
	tables := make(map[uint64]lockTable, tableCount)
	for i := 0; i < tableCount; i++ {
		tableID := uint64(i + 1)
		bind := pb.LockTable{Group: 0, Table: tableID}
		tables[tableID] = &closeTestLockTable{
			bind:  bind,
			probe: probe,
			delay: delay,
		}
	}
	return tables
}

func closeTestTableLookup(tables map[uint64]lockTable) func(uint32, uint64) (lockTable, error) {
	return func(_ uint32, table uint64) (lockTable, error) {
		return tables[table], nil
	}
}

func goUnlockTask(fn func()) error {
	go fn()
	return nil
}

func TestCloseParallelUnlockUsesActualTableCountAndIsBounded(t *testing.T) {
	reuse.RunReuseTests(func() {
		release := make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		probe := &closeTestProbe{
			entered: make(chan struct{}, 8),
			release: release,
		}
		txn, tables := newCloseTestTxn(t, 8, probe, 0)
		assert.Len(t, txn.lockHolders, 1)
		assert.Equal(t, 8, txn.heldLockTableCount())

		result := make(chan error, 1)
		go func() {
			result <- txn.closeWithTaskSubmitter(
				txn.txnID,
				timestamp.Timestamp{},
				closeTestTableLookup(tables),
				getLogger(""),
				goUnlockTask,
			)
		}()

		for i := 0; i < maxParallelUnlockWorkers; i++ {
			select {
			case <-probe.entered:
			case <-time.After(2 * time.Second):
				t.Fatalf("only %d table unlocks started in parallel", i)
			}
		}
		select {
		case <-probe.entered:
			t.Fatal("parallel table unlock exceeded the worker bound")
		case <-time.After(50 * time.Millisecond):
		}

		close(release)
		released = true
		select {
		case err := <-result:
			assert.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("parallel table unlock did not complete")
		}
		assert.Equal(t, int32(8), probe.calls.Load())
		assert.Equal(t, int32(maxParallelUnlockWorkers), probe.maxActive.Load())
	})
}

func TestCloseUnlocksTwoTablesSequentially(t *testing.T) {
	reuse.RunReuseTests(func() {
		probe := &closeTestProbe{}
		txn, tables := newCloseTestTxn(t, parallelUnlockTables, probe, 0)
		var submitCalls atomic.Int32
		err := txn.closeWithTaskSubmitter(
			txn.txnID,
			timestamp.Timestamp{},
			closeTestTableLookup(tables),
			getLogger(""),
			func(func()) error {
				submitCalls.Add(1)
				return nil
			},
		)
		assert.NoError(t, err)
		assert.Zero(t, submitCalls.Load())
		assert.Equal(t, int32(parallelUnlockTables), probe.calls.Load())
		assert.Equal(t, int32(1), probe.maxActive.Load())
	})
}

func TestCloseParallelUnlockPartialSubmitFailureFallsBack(t *testing.T) {
	reuse.RunReuseTests(func() {
		probe := &closeTestProbe{}
		txn, tables := newCloseTestTxn(t, 4, probe, 0)
		var submitCalls atomic.Int32
		submitErr := errors.New("submit failed")
		err := txn.closeWithTaskSubmitter(
			txn.txnID,
			timestamp.Timestamp{},
			closeTestTableLookup(tables),
			getLogger(""),
			func(fn func()) error {
				if submitCalls.Add(1) == 2 {
					return submitErr
				}
				go fn()
				return nil
			},
		)
		assert.NoError(t, err)
		assert.Equal(t, int32(2), submitCalls.Load())
		assert.Equal(t, int32(4), probe.calls.Load())
		assert.Zero(t, probe.active.Load())
	})
}

func TestCloseParallelUnlockPropagatesWorkerPanic(t *testing.T) {
	reuse.RunReuseTests(func() {
		const panicValue = "unlock panic"
		probe := &closeTestProbe{panicValue: panicValue}
		txn, tables := newCloseTestTxn(t, 4, probe, 0)
		freedByClose := false
		defer func() {
			if !freedByClose {
				reuse.Free(txn, nil)
			}
		}()
		assert.PanicsWithValue(t, panicValue, func() {
			_ = txn.closeWithTaskSubmitter(
				txn.txnID,
				timestamp.Timestamp{},
				closeTestTableLookup(tables),
				getLogger(""),
				goUnlockTask,
			)
			freedByClose = true
		})
		assert.Equal(t, int32(4), probe.calls.Load())
	})
}

func TestCloseSequentialUnlockPreservesFailFastPanic(t *testing.T) {
	reuse.RunReuseTests(func() {
		const panicValue = "sequential unlock panic"
		probe := &closeTestProbe{panicValue: panicValue}
		txn, tables := newCloseTestTxn(t, parallelUnlockTables, probe, 0)
		freedByClose := false
		defer func() {
			if !freedByClose {
				reuse.Free(txn, nil)
			}
		}()
		assert.PanicsWithValue(t, panicValue, func() {
			_ = txn.closeWithTaskSubmitter(
				txn.txnID,
				timestamp.Timestamp{},
				closeTestTableLookup(tables),
				getLogger(""),
				func(func()) error {
					t.Fatal("sequential close submitted an unlock task")
					return nil
				},
			)
			freedByClose = true
		})
		assert.Equal(t, int32(1), probe.calls.Load())
	})
}

func TestCloseParallelUnlockWaitsBeforePropagatingLookupError(t *testing.T) {
	reuse.RunReuseTests(func() {
		probe := &closeTestProbe{}
		txn, tables := newCloseTestTxn(t, 4, probe, time.Millisecond)
		lookupErr := errors.New("lookup failed")
		var lookupCalls atomic.Int32
		freedByClose := false
		defer func() {
			if !freedByClose {
				reuse.Free(txn, nil)
			}
		}()
		assert.PanicsWithValue(t, lookupErr, func() {
			_ = txn.closeWithTaskSubmitter(
				txn.txnID,
				timestamp.Timestamp{},
				func(_ uint32, table uint64) (lockTable, error) {
					if lookupCalls.Add(1) == maxParallelUnlockWorkers {
						return nil, lookupErr
					}
					return tables[table], nil
				},
				getLogger(""),
				goUnlockTask,
			)
			freedByClose = true
		})
		assert.Equal(t, int32(3), probe.calls.Load())
		assert.Zero(t, probe.active.Load())
	})
}

func BenchmarkTxnCloseTables(b *testing.B) {
	// The noop workload captures scheduling cost. The delayed workload models
	// independent table unlocks whose latency can overlap.
	workloads := []struct {
		name  string
		delay time.Duration
	}{
		{name: "noop"},
		{name: "100us-per-table", delay: 100 * time.Microsecond},
	}
	for _, workload := range workloads {
		for _, tableCount := range []int{1, 2, 3, 4, 6, 8} {
			b.Run(fmt.Sprintf("%s/%d-tables", workload.name, tableCount), func(b *testing.B) {
				fsp := newFixedSlicePool(16)
				tables := newCloseTestLockTables(tableCount, nil, workload.delay)
				txnID := []byte("close-benchmark-txn")
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					txn := newActiveTxn(txnID, string(txnID), fsp, "")
					if err := addCloseTestHeldTables(txn, fsp, tableCount); err != nil {
						b.Fatal(err)
					}
					if err := txn.close(
						txn.txnID,
						timestamp.Timestamp{},
						closeTestTableLookup(tables),
						getLogger(""),
					); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
