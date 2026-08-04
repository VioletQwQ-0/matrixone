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
	"testing"

	pb "github.com/matrixorigin/matrixone/pkg/pb/lock"
	v2 "github.com/matrixorigin/matrixone/pkg/util/metric/v2"
	"github.com/stretchr/testify/require"
)

func TestLocalLockProfileRowRelation(t *testing.T) {
	var table localLockTable
	owner := &lockContext{
		rows: [][]byte{[]byte("a"), []byte("b")},
		opts: LockOptions{LockOptions: pb.LockOptions{
			Granularity: pb.Granularity_Row,
		}},
	}
	table.profileSetAcquireOwner(owner)

	require.Equal(t, v2.LocalLockProfileSameRow,
		table.profileRowRelation([][]byte{[]byte("b"), []byte("c")}))
	require.Equal(t, v2.LocalLockProfileDifferentRow,
		table.profileRowRelation([][]byte{[]byte("c"), []byte("d")}))
	owner.rows = make([][]byte, localLockProfileMaxOwnerRows+1)
	for idx := range owner.rows {
		owner.rows[idx] = []byte{byte(idx)}
	}
	table.profileSetAcquireOwner(owner)
	require.Equal(t, v2.LocalLockProfileSameRow,
		table.profileRowRelation([][]byte{owner.rows[0]}))
	require.Equal(t, v2.LocalLockProfileUnknown,
		table.profileRowRelation([][]byte{owner.rows[len(owner.rows)-1]}))

	table.profileSetAcquireOwner(&lockContext{
		rows: [][]byte{[]byte("a"), []byte("z")},
		opts: LockOptions{LockOptions: pb.LockOptions{
			Granularity: pb.Granularity_Range,
		}},
	})
	require.Equal(t, v2.LocalLockProfileRangeOrOther,
		table.profileRowRelation([][]byte{[]byte("a")}))

	table.profile.ownerKind.Store(localLockProfileOwnerUnlock)
	require.Equal(t, v2.LocalLockProfileRangeOrOther,
		table.profileRowRelation([][]byte{[]byte("a")}))

	table.profile.ownerKind.Store(localLockProfileOwnerNone)
	require.Equal(t, v2.LocalLockProfileUnknown,
		table.profileRowRelation([][]byte{[]byte("a")}))
}

func TestProfiledLockStorageDelegatesBTreeOperations(t *testing.T) {
	store := newProfiledLockStorage()
	key := []byte("row")
	lock := Lock{value: 7}

	store.Add(key, lock)
	foundKey, foundLock, ok := store.Seek(key)
	require.True(t, ok)
	require.Equal(t, key, foundKey)
	require.Equal(t, lock.value, foundLock.value)

	deleted, ok := store.Delete(key)
	require.True(t, ok)
	require.Equal(t, lock.value, deleted.value)
	require.Equal(t, 0, store.Len())
}
