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

package lockservice

import (
	"time"

	"github.com/cespare/xxhash/v2"
	pb "github.com/matrixorigin/matrixone/pkg/pb/lock"
	v2 "github.com/matrixorigin/matrixone/pkg/util/metric/v2"
)

const (
	localLockProfileOwnerNone uint32 = iota
	localLockProfileOwnerAcquireRow
	localLockProfileOwnerAcquireOther
	localLockProfileOwnerUnlock
	localLockProfileMaxOwnerRows = 16
)

// profiledLockStorage keeps the diagnostic B-tree accounting at the storage
// boundary. The embedded interface delegates operations that are not part of
// the row-sharding admission decision.
type profiledLockStorage struct {
	LockStorage
}

func newProfiledLockStorage() LockStorage {
	return &profiledLockStorage{LockStorage: newBtreeBasedStorage()}
}

func (s *profiledLockStorage) Add(key []byte, value Lock) {
	start := time.Now()
	s.LockStorage.Add(key, value)
	v2.LocalLockProfileBTreeAddDurationHistogram.Observe(time.Since(start).Seconds())
}

func (s *profiledLockStorage) Delete(key []byte) (Lock, bool) {
	start := time.Now()
	lock, ok := s.LockStorage.Delete(key)
	v2.LocalLockProfileBTreeDeleteDurationHistogram.Observe(time.Since(start).Seconds())
	return lock, ok
}

func (s *profiledLockStorage) Seek(key []byte) ([]byte, Lock, bool) {
	start := time.Now()
	row, lock, ok := s.LockStorage.Seek(key)
	v2.LocalLockProfileBTreeSeekDurationHistogram.Observe(time.Since(start).Seconds())
	return row, lock, ok
}

func (l *localLockTable) profileRowRequests(c *lockContext) {
	if c.opts.Granularity != pb.Granularity_Row {
		return
	}
	for _, row := range c.rows[c.offset:] {
		shard := xxhash.Sum64(row) % v2.LocalLockProfileShardCount
		v2.LocalLockProfileRowShardCounters[shard].Inc()
	}
}

func (l *localLockTable) profileAcquireMutex(c *lockContext) time.Time {
	l.profileRowRequests(c)
	if l.mu.TryLock() {
		l.profileSetAcquireOwner(c)
		return time.Now()
	}

	relation := l.profileRowRelation(c.rows[c.offset:])
	start := time.Now()
	l.mu.Lock()
	v2.ObserveLocalLockProfileAcquireWait(relation, time.Since(start).Seconds())
	l.profileSetAcquireOwner(c)
	return time.Now()
}

func (l *localLockTable) profileUnlockMutex() time.Time {
	if l.mu.TryLock() {
		l.profile.ownerKind.Store(localLockProfileOwnerUnlock)
		return time.Now()
	}

	relation := v2.LocalLockProfileUnknown
	start := time.Now()
	l.mu.Lock()
	v2.ObserveLocalLockProfileUnlockWait(relation, time.Since(start).Seconds())
	l.profile.ownerKind.Store(localLockProfileOwnerUnlock)
	return time.Now()
}

func (l *localLockTable) profileReleaseAcquireMutex(start time.Time) {
	l.profile.ownerKind.Store(localLockProfileOwnerNone)
	l.mu.Unlock()
	v2.LocalLockProfileAcquireHoldDurationHistogram.Observe(time.Since(start).Seconds())
}

func (l *localLockTable) profileReleaseUnlockMutex(start time.Time) {
	l.profile.ownerKind.Store(localLockProfileOwnerNone)
	l.mu.Unlock()
	v2.LocalLockProfileUnlockHoldDurationHistogram.Observe(time.Since(start).Seconds())
}

func (l *localLockTable) profileOwnerRelation() int {
	switch l.profile.ownerKind.Load() {
	case localLockProfileOwnerAcquireRow, localLockProfileOwnerAcquireOther:
		return v2.LocalLockProfileRangeOrOther
	case localLockProfileOwnerUnlock:
		return v2.LocalLockProfileRangeOrOther
	default:
		return v2.LocalLockProfileUnknown
	}
}

func (l *localLockTable) profileRowRelation(rows [][]byte) int {
	if l.profile.ownerKind.Load() != localLockProfileOwnerAcquireRow {
		return l.profileOwnerRelation()
	}
	ownerRows := l.profile.ownerRowCount.Load()
	trackedRows := min(ownerRows, localLockProfileMaxOwnerRows)
	for _, row := range rows {
		hash := profileRowHash(row)
		for idx := uint32(0); idx < trackedRows; idx++ {
			if hash == l.profile.ownerRowHashes[idx].Load() {
				return v2.LocalLockProfileSameRow
			}
		}
	}
	if ownerRows > localLockProfileMaxOwnerRows {
		return v2.LocalLockProfileUnknown
	}
	return v2.LocalLockProfileDifferentRow
}

func (l *localLockTable) profileSetAcquireOwner(c *lockContext) {
	if c.opts.Granularity != pb.Granularity_Row {
		l.profile.ownerKind.Store(localLockProfileOwnerAcquireOther)
		return
	}
	rows := c.rows[c.offset:]
	trackedRows := min(len(rows), localLockProfileMaxOwnerRows)
	for idx := range trackedRows {
		l.profile.ownerRowHashes[idx].Store(profileRowHash(rows[idx]))
	}
	l.profile.ownerRowCount.Store(uint32(len(rows)))
	l.profile.ownerKind.Store(localLockProfileOwnerAcquireRow)
}

func profileRowHash(row []byte) uint64 {
	// Reserve zero as the unset value. A collision in a 64-bit diagnostic
	// fingerprint is negligible; overflowing hash zero is mapped to one.
	hash := xxhash.Sum64(row)
	if hash == 0 {
		return 1
	}
	return hash
}
