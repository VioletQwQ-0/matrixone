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

package v2

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

const LocalLockProfileShardCount = 64

const (
	LocalLockProfileSameRow = iota
	LocalLockProfileDifferentRow
	LocalLockProfileRangeOrOther
	LocalLockProfileUnknown
)

var (
	lockServiceStaleBindPurgedCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "mo",
			Subsystem: "lockservice",
			Name:      "stale_bind_purged_total",
			Help:      "Total number of stale lock table binds purged by lockservice allocator epoch fencing.",
		}, []string{"source"})

	lockServiceAllocatorEpochChangedCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "mo",
			Subsystem: "lockservice",
			Name:      "allocator_epoch_changed_total",
			Help:      "Total number of allocator epoch changes observed by lockservice.",
		}, []string{"source"})

	lockServiceAllocatorEpochRegressionCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "mo",
			Subsystem: "lockservice",
			Name:      "allocator_epoch_regression_total",
			Help:      "Total number of allocator epoch regressions observed by lockservice.",
		}, []string{"source"})

	LockServiceAllocatorEpochObservedGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mo",
			Subsystem: "lockservice",
			Name:      "allocator_epoch_observed",
			Help:      "Latest allocator epoch observed by lockservice.",
		})

	// The local lock profile metrics are intentionally diagnostic-only. They use
	// fixed labels so a profiling build can attribute the table mutex to row-key
	// overlap, critical-section work, and a projected 64-shard distribution
	// without publishing row or table identifiers.
	localLockProfileDurationHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "mo",
			Subsystem: "lockservice",
			Name:      "local_lock_profile_duration_seconds",
			Help:      "Profiling-only local lock table duration by stage and row-key relation.",
			Buckets:   getDurationBuckets(),
		}, []string{"stage", "relation"})

	LocalLockProfileAcquireHoldDurationHistogram = localLockProfileDurationHistogram.WithLabelValues("acquire-hold", "all")
	LocalLockProfileUnlockHoldDurationHistogram  = localLockProfileDurationHistogram.WithLabelValues("unlock-hold", "all")
	LocalLockProfileBTreeSeekDurationHistogram   = localLockProfileDurationHistogram.WithLabelValues("btree-seek", "all")
	LocalLockProfileBTreeAddDurationHistogram    = localLockProfileDurationHistogram.WithLabelValues("btree-add", "all")
	LocalLockProfileBTreeDeleteDurationHistogram = localLockProfileDurationHistogram.WithLabelValues("btree-delete", "all")
	LocalLockProfileWaiterDurationHistogram      = localLockProfileDurationHistogram.WithLabelValues("waiter-bookkeeping", "all")
	LocalLockProfileDeadlockDurationHistogram    = localLockProfileDurationHistogram.WithLabelValues("deadlock-bookkeeping", "all")

	localLockProfileAcquireWaitDuration = [4]prometheus.Observer{
		localLockProfileDurationHistogram.WithLabelValues("acquire-wait", "same-row"),
		localLockProfileDurationHistogram.WithLabelValues("acquire-wait", "different-row"),
		localLockProfileDurationHistogram.WithLabelValues("acquire-wait", "range-or-other"),
		localLockProfileDurationHistogram.WithLabelValues("acquire-wait", "unknown"),
	}
	localLockProfileUnlockWaitDuration = [4]prometheus.Observer{
		localLockProfileDurationHistogram.WithLabelValues("unlock-wait", "same-row"),
		localLockProfileDurationHistogram.WithLabelValues("unlock-wait", "different-row"),
		localLockProfileDurationHistogram.WithLabelValues("unlock-wait", "range-or-other"),
		localLockProfileDurationHistogram.WithLabelValues("unlock-wait", "unknown"),
	}

	localLockProfileContentionCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "mo",
			Subsystem: "lockservice",
			Name:      "local_lock_profile_contention_total",
			Help:      "Profiling-only contended local lock table acquisitions by operation and row-key relation.",
		}, []string{"operation", "relation"})
	localLockProfileAcquireContention = [4]prometheus.Counter{
		localLockProfileContentionCounter.WithLabelValues("acquire", "same-row"),
		localLockProfileContentionCounter.WithLabelValues("acquire", "different-row"),
		localLockProfileContentionCounter.WithLabelValues("acquire", "range-or-other"),
		localLockProfileContentionCounter.WithLabelValues("acquire", "unknown"),
	}
	localLockProfileUnlockContention = [4]prometheus.Counter{
		localLockProfileContentionCounter.WithLabelValues("unlock", "same-row"),
		localLockProfileContentionCounter.WithLabelValues("unlock", "different-row"),
		localLockProfileContentionCounter.WithLabelValues("unlock", "range-or-other"),
		localLockProfileContentionCounter.WithLabelValues("unlock", "unknown"),
	}

	localLockProfileRowShardCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "mo",
			Subsystem: "lockservice",
			Name:      "local_lock_profile_row_shard_total",
			Help:      "Profiling-only row requests mapped to each projected 64-way shard.",
		}, []string{"shard"})
	LocalLockProfileRowShardCounters [LocalLockProfileShardCount]prometheus.Counter
)

func init() {
	for shard := range LocalLockProfileRowShardCounters {
		LocalLockProfileRowShardCounters[shard] = localLockProfileRowShardCounter.WithLabelValues(strconv.Itoa(shard))
	}
}

func ObserveLocalLockProfileAcquireWait(relation int, seconds float64) {
	if relation < 0 || relation >= len(localLockProfileAcquireWaitDuration) {
		relation = LocalLockProfileUnknown
	}
	localLockProfileAcquireWaitDuration[relation].Observe(seconds)
	localLockProfileAcquireContention[relation].Inc()
}

func ObserveLocalLockProfileUnlockWait(relation int, seconds float64) {
	if relation < 0 || relation >= len(localLockProfileUnlockWaitDuration) {
		relation = LocalLockProfileUnknown
	}
	localLockProfileUnlockWaitDuration[relation].Observe(seconds)
	localLockProfileUnlockContention[relation].Inc()
}

func GetLockServiceStaleBindPurgedCounter(source string) prometheus.Counter {
	return lockServiceStaleBindPurgedCounter.WithLabelValues(source)
}

func GetLockServiceAllocatorEpochChangedCounter(source string) prometheus.Counter {
	return lockServiceAllocatorEpochChangedCounter.WithLabelValues(source)
}

func GetLockServiceAllocatorEpochRegressionCounter(source string) prometheus.Counter {
	return lockServiceAllocatorEpochRegressionCounter.WithLabelValues(source)
}

func initLockServiceMetrics() {
	registry.MustRegister(lockServiceStaleBindPurgedCounter)
	registry.MustRegister(lockServiceAllocatorEpochChangedCounter)
	registry.MustRegister(lockServiceAllocatorEpochRegressionCounter)
	registry.MustRegister(LockServiceAllocatorEpochObservedGauge)
	registry.MustRegister(localLockProfileDurationHistogram)
	registry.MustRegister(localLockProfileContentionCounter)
	registry.MustRegister(localLockProfileRowShardCounter)
}
