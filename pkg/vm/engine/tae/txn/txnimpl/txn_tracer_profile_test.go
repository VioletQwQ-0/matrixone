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

package txnimpl

import (
	"testing"
	"time"

	"github.com/matrixorigin/matrixone/pkg/vm/engine/tae/iface/txnif"
	"github.com/stretchr/testify/require"
)

func TestTxnTracerProfileTransitions(t *testing.T) {
	tracer := &txnTracer{}
	now := time.Unix(1, 0)
	tracer.recordProfileTransition(txnif.TraceStart, now)
	for state := uint8(txnif.TraceAfterFreeze); state <= txnif.TraceDoneApply; state++ {
		now = now.Add(time.Millisecond)
		tracer.recordProfileTransition(state, now)
	}

	for idx := range tracer.profileSeen {
		require.Truef(t, tracer.profileSeen[idx], "stage %d was not recorded", idx)
		require.Equal(t, time.Millisecond, tracer.profileDurations[idx])
	}
}

func TestTxnTracerProfileRejectsSkippedTransition(t *testing.T) {
	tracer := &txnTracer{}
	now := time.Unix(1, 0)
	tracer.recordProfileTransition(txnif.TraceStart, now)
	tracer.recordProfileTransition(txnif.TracePreWal, now.Add(time.Millisecond))

	for _, seen := range tracer.profileSeen {
		require.False(t, seen)
	}
	require.Equal(t, uint8(txnif.TraceStart), tracer.profileState)
}
