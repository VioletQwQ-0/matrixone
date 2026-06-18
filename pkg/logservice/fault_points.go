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

import "github.com/matrixorigin/matrixone/pkg/util/fault"

const (
	fjLogServiceHeartbeatDelay            = "fj/logservice/heartbeat/delay"
	fjLogServiceHeartbeatDropOnce         = "fj/logservice/heartbeat/drop-once"
	fjLogServiceHeartbeatReportStaleView  = "fj/logservice/heartbeat/report-stale-view"
	fjLogServiceSnapshotImportVersion     = "fj/logservice/snapshot/import-version-override"
	fjLogServiceConfigChangePendingWindow = "fj/logservice/config-change/pending-window"
	fjHAKeeperCommandDelayLKill           = "fj/hakeeper/command/delay-LKill"
	fjHAKeeperCommandDropLStartOnce       = "fj/hakeeper/command/drop-LStart-once"
)

func triggerFaultPoint(name string) (int64, string, bool) {
	return fault.TriggerFault(name)
}
