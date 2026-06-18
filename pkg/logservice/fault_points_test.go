// Copyright 2021 - 2022 Matrix Origin
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

package logservice

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matrixorigin/matrixone/pkg/util/fault"
)

func TestLogServiceFaultPointNames(t *testing.T) {
	names := []string{
		fjLogServiceHeartbeatDelay,
		fjLogServiceHeartbeatDropOnce,
		fjLogServiceHeartbeatReportStaleView,
		fjLogServiceSnapshotImportVersion,
		fjLogServiceConfigChangePendingWindow,
		fjHAKeeperCommandDelayLKill,
		fjHAKeeperCommandDropLStartOnce,
	}
	for _, name := range names {
		require.Contains(t, name, "fj/")
	}
}

func TestTriggerLogServiceFaultPoint(t *testing.T) {
	fault.Enable()
	defer fault.Disable()

	require.NoError(t, fault.AddFaultPoint(
		context.Background(),
		fjLogServiceHeartbeatReportStaleView,
		":::",
		"echo",
		7,
		"drop-replicas",
		false,
	))

	iarg, sarg, ok := triggerFaultPoint(fjLogServiceHeartbeatReportStaleView)
	require.True(t, ok)
	require.Equal(t, int64(7), iarg)
	require.Equal(t, "drop-replicas", sarg)
}
