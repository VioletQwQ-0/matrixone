// Copyright 2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package readutil

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/matrixorigin/matrixone/pkg/common/mpool"
	"github.com/matrixorigin/matrixone/pkg/container/batch"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/defines"
	"github.com/matrixorigin/matrixone/pkg/fileservice"
	"github.com/matrixorigin/matrixone/pkg/objectio"
	"github.com/matrixorigin/matrixone/pkg/objectio/ioutil"
	"github.com/matrixorigin/matrixone/pkg/pb/timestamp"
	"github.com/matrixorigin/matrixone/pkg/sql/plan/function"
	"github.com/matrixorigin/matrixone/pkg/util/toml"
	"github.com/matrixorigin/matrixone/pkg/vm/engine"
	"github.com/matrixorigin/matrixone/pkg/vm/engine/tae/blockio"
	"github.com/matrixorigin/matrixone/pkg/vm/engine/tae/containers"
	"github.com/stretchr/testify/require"
)

type matcherBenchBlockSource struct{ engine.DataSource }

func (*matcherBenchBlockSource) ApplyTombstones(_ context.Context, _ *objectio.Blockid, rows []int64, _ engine.TombstoneApplyPolicy) ([]int64, error) {
	return rows, nil
}

// A real cached persisted block consumer, with an unsorted UINT64 fake PK and
// materialized matching rows. No IVF/top-K path or replacement block-read loop is used.
func BenchmarkIssue29322MatcherBlock(b *testing.B) {
	if group := os.Getenv("MATCHER_BENCH_GROUP"); group != "" && group != "normal" {
		return
	}
	for _, algorithm := range matcherBenchBenchmarkAlgorithms() {
		b.Run(algorithm, func(b *testing.B) {
			for _, m := range []int{2, 5, 7, 8, 9, 11, 13} {
				b.Run(fmt.Sprintf("m%d", m), func(b *testing.B) {
					ctx := context.Background()
					mp := mpool.MustNewZero()
					b.Cleanup(func() { mpool.DeleteMPool(mp) })
					capacity := toml.ByteSize(8 << 20)
					fs, err := fileservice.NewLocalFS2(ctx, defines.SharedFileServiceName, b.TempDir(), fileservice.CacheConfig{MemoryCapacity: &capacity}, nil)
					require.NoError(b, err)
					b.Cleanup(func() { fs.Close(ctx) })
					fs.SetAsyncUpdate(false)
					values, vectors := matcherBenchInputs(b, mp, matcherBenchCase{m, 8192, 1, "one"})
					input := batch.NewWithSize(1)
					input.Vecs[0] = vectors[0]
					input.SetRowCount(8192)
					writer := ioutil.ConstructWriter(0, []uint16{0}, -1, false, false, fs)
					_, err = writer.WriteBatch(input)
					require.NoError(b, err)
					_, _, err = writer.Sync(ctx)
					require.NoError(b, err)
					stats := writer.GetObjectStats()
					info := stats.ConstructBlockInfo(0)
					columnTypes := []types.Type{types.T_uint64.ToType()}
					columns := []uint16{0}
					output := batch.NewWithSize(1)
					output.Vecs[0] = vector.NewOffHeapVecWithType(columnTypes[0])
					b.Cleanup(func() { output.Clean(mp) })
					cache := containers.NewVectors(2)
					b.Cleanup(func() { cache.Free(mp) })
					candidate := matcherBenchVector(b, mp, values)
					candidate.SetSorted(true)
					wire, err := candidate.MarshalBinary()
					require.NoError(b, err)
					read := func() error {
						decoded, err := unmarshalPKInVector(wire)
						if err != nil {
							return err
						}
						var selected matcherBenchFactory
						if algorithm != "generic" {
							selected = matcherBenchFactoryFor(algorithm)
						}
						filter, err := constructBlockPKFilterForMatcher(true, BasePKFilter{Valid: true, Op: function.IN, Oid: types.T_uint64, Vec: decoded}, nil, selected)
						if err != nil {
							return err
						}
						output.CleanOnlyData()
						return blockio.BlockDataRead(ctx, &info, &matcherBenchBlockSource{}, columns, columnTypes, -1, timestamp.Timestamp{}, columns, columnTypes, filter, nil, fileservice.Policy(0), "uint64-matcher-benchmark", output, cache, mp, fs)
					}
					require.NoError(b, read())
					require.NoError(b, read())
					require.Equal(b, 1, output.RowCount())
					require.Equal(b, []uint64{values[0]}, vector.MustFixedColNoTypeCheck[uint64](output.Vecs[0]))
					location := info.MetaLocation()
					meta, err := objectio.FastLoadObjectMeta(ctx, &location, false, fs)
					require.NoError(b, err)
					dataMeta := meta.MustGetMeta(objectio.SchemaData)
					cached, err := objectio.ReadOneBlock(ctx, &dataMeta, location.Name().UnsafeString(), location.ID(), columns, columnTypes, mp, fs, fileservice.Policy(0), objectio.ShareScopedDecodedColumn)
					require.NoError(b, err)
					for _, entry := range cached.Entries {
						require.True(b, entry.WasFromCache())
					}
					cached.Release()
					b.ReportAllocs()
					finish := matcherBenchWindow(b)
					defer finish()
					b.ResetTimer()
					b.StartTimer()
					for i := 0; i < b.N; i++ {
						if err := read(); err != nil {
							b.Fatal(err)
						}
						if output.RowCount() != 1 {
							b.Fatal("persisted block output differs from oracle")
						}
					}
				})
			}
		})
	}
}
