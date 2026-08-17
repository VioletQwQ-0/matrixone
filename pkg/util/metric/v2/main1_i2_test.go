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

package v2

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func resetMain1I2ForTest(t *testing.T, enabled bool, completedCapacity int) {
	t.Helper()
	oldEnabled := main1I2Enabled.Load()
	main1I2.Lock()
	oldActive := main1I2.active
	oldCompleted := main1I2.completed
	oldNext := main1I2.next
	oldDropped := main1I2.dropped
	main1I2.active = nil
	main1I2.completed = nil
	main1I2.next = 0
	main1I2.dropped = 0
	if enabled {
		main1I2.active = make(map[main1I2TxnKey]main1I2ActiveTxn, main1I2MaxActive)
		main1I2.completed = make([]Main1I2TxnRecord, completedCapacity)
	}
	main1I2.Unlock()
	main1I2Enabled.Store(enabled)
	t.Cleanup(func() {
		main1I2Enabled.Store(oldEnabled)
		main1I2.Lock()
		main1I2.active = oldActive
		main1I2.completed = oldCompleted
		main1I2.next = oldNext
		main1I2.dropped = oldDropped
		main1I2.Unlock()
	})
}

func TestMain1I2DisabledPathAllocations(t *testing.T) {
	resetMain1I2ForTest(t, false, 0)
	txnID := []byte("0123456789abcdef")
	allocs := testing.AllocsPerRun(1000, func() {
		if Main1I2RegisterFrontendTxn(txnID, Main1I2Payment) {
			panic("disabled diagnostic admitted a transaction")
		}
		Main1I2AbortTxn(txnID)
		Main1I2ObserveCommitSubstage(Main1I2PrePrepare, Main1I2Payment, time.Microsecond)
	})
	require.Zero(t, allocs)
}

func TestMain1I2TemplateClassification(t *testing.T) {
	resetMain1I2ForTest(t, true, 4)
	require.Equal(t, Main1I2NewOrder, Main1I2ClassifyPreparedSQL("INSERT INTO bmsql_new_order VALUES (?, ?, ?)"))
	require.Equal(t, Main1I2Payment, Main1I2ClassifyPreparedSQL("insert into bmsql_history values (?)"))
	require.Equal(t, Main1I2Delivery, Main1I2ClassifyPreparedSQL("DELETE FROM bmsql_new_order WHERE no_w_id = ?"))
	require.Equal(t, Main1I2Unknown, Main1I2ClassifyPreparedSQL("SELECT 1"))
	require.Equal(t, "mixed", Main1I2TemplateName(Main1I2MergeTemplate(Main1I2Payment, Main1I2NewOrder)))
}

func TestMain1I2FrontendTNJoinAndRingBound(t *testing.T) {
	resetMain1I2ForTest(t, true, 2)
	for idx, template := range []Main1I2Template{Main1I2NewOrder, Main1I2Payment, Main1I2Delivery} {
		txnID := []byte{byte(idx + 1), 2, 3, 4}
		require.True(t, Main1I2RegisterFrontendTxn(txnID, template))
		var durations [Main1I2StageCount]time.Duration
		durations[1] = time.Duration(idx+1) * time.Millisecond
		Main1I2CompleteTN(string(txnID), durations)
	}
	require.Empty(t, main1I2.active)
	require.Equal(t, uint64(3), main1I2.next)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/debug/main1-i2", nil)
	Main1I2SnapshotHandler().ServeHTTP(recorder, request)
	require.Equal(t, 200, recorder.Code)
	var snapshot main1I2Snapshot
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &snapshot))
	require.Len(t, snapshot.FrontendRecords, 2)
	require.Len(t, snapshot.TNRecords, 2)
	require.Equal(t, "payment", snapshot.FrontendRecords[0].Template)
	require.Equal(t, "delivery", snapshot.FrontendRecords[1].Template)
	require.Equal(t, snapshot.FrontendRecords[1].TxnHash, snapshot.TNRecords[1].TxnHash)
	require.Equal(t, int64(3*time.Millisecond), snapshot.TNRecords[1].StageNanos[1])
}

func TestMain1I2ConcurrentUpdateCheckAndAbort(t *testing.T) {
	resetMain1I2ForTest(t, true, 256)
	var wg sync.WaitGroup
	for idx := 0; idx < 128; idx++ {
		idx := idx
		wg.Add(1)
		go func() {
			defer wg.Done()
			txnID := []byte{byte(idx), byte(idx >> 8), 7, 9}
			require.True(t, Main1I2RegisterFrontendTxn(txnID, Main1I2Payment))
			require.Equal(t, Main1I2Payment, Main1I2LookupTemplate(string(txnID)))
			if idx%2 == 0 {
				Main1I2CompleteTN(string(txnID), [Main1I2StageCount]time.Duration{})
			} else {
				Main1I2AbortTxn(txnID)
			}
		}()
	}
	wg.Wait()
	require.Empty(t, main1I2.active)
	require.Equal(t, uint64(64), main1I2.next)
}
