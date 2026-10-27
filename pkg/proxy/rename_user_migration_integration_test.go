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

package proxy_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/matrixorigin/matrixone/pkg/catalog"
	"github.com/matrixorigin/matrixone/pkg/clusterservice"
	"github.com/matrixorigin/matrixone/pkg/cnservice"
	"github.com/matrixorigin/matrixone/pkg/common/moerr"
	moruntime "github.com/matrixorigin/matrixone/pkg/common/runtime"
	"github.com/matrixorigin/matrixone/pkg/embed"
	"github.com/matrixorigin/matrixone/pkg/frontend"
	"github.com/matrixorigin/matrixone/pkg/pb/metadata"
	"github.com/matrixorigin/matrixone/pkg/proxy"
	"github.com/matrixorigin/matrixone/pkg/tests/testutils"
	"github.com/matrixorigin/matrixone/pkg/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestProxyRenameUserMigrationRejectsChangedPrincipal uses a private real
// two-CN cluster and a real Proxy. Only the transfer trigger and the RPC
// observation barrier are test-only; authentication and migration are real.
func TestProxyRenameUserMigrationRejectsChangedPrincipal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	var transferDone chan error
	transferJoined := false
	proxyClosed := true
	cluster, err := embed.StartTestCluster(
		embed.WithCNCount(2),
		embed.WithTestAdmissionContext(ctx),
		embed.WithPreStart(func(op embed.ServiceOperator) {
			if op.ServiceType() == metadata.ServiceType_CN {
				op.Adjust(func(cfg *embed.ServiceConfig) {
					cfg.CN.Frontend.SkipCheckUser = false
					cfg.CN.Frontend.ProxyEnabled = true
				})
			}
		}),
	)
	if cluster != nil {
		var ownedDataDir string
		if first, getErr := cluster.GetCNService(0); getErr == nil {
			ownedDataDir = first.GetServiceConfig().DataDir
		}
		t.Cleanup(func() {
			if transferDone != nil && !transferJoined {
				select {
				case <-transferDone:
					transferJoined = true
				default:
					t.Error("transfer is still active; preserve the owned cluster")
					return
				}
			}
			if !proxyClosed {
				t.Error("proxy cleanup is incomplete; preserve the owned cluster")
				return
			}
			if closeErr := cluster.Close(); closeErr != nil {
				t.Errorf("close owned cluster: %v", closeErr)
				return
			}
			complete, ok := cluster.(interface{ CloseComplete() bool })
			if !ok || !complete.CloseComplete() {
				t.Error("owned cluster cleanup is incomplete; preserve its data directory")
				return
			}
			dir := filepath.Clean(ownedDataDir)
			if filepath.Dir(dir) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(dir), "mo-cluster-test-") {
				t.Errorf("cannot identify the owned cluster data directory %q", ownedDataDir)
				return
			}
			if removeErr := os.RemoveAll(dir); removeErr != nil {
				t.Errorf("remove owned cluster data directory: %v", removeErr)
			}
		})
	}
	require.NoError(t, err)

	cns := make([]embed.ServiceOperator, 2)
	admins := make([]*sql.Conn, 2)
	open := func(address, user, password string) *sql.DB {
		t.Helper()
		cfg := mysql.NewConfig()
		cfg.Net, cfg.Addr, cfg.User, cfg.Passwd = "tcp", address, user, password
		dsn := cfg.FormatDSN()
		parsed, parseErr := mysql.ParseDSN(dsn)
		require.NoError(t, parseErr)
		require.True(t, parsed.User == user && parsed.Passwd == password, "DSN must retain the intended login")
		db, openErr := sql.Open("mysql", dsn)
		require.NoError(t, openErr)
		t.Cleanup(func() {
			if closeErr := db.Close(); closeErr != nil {
				t.Errorf("close SQL pool: %v", closeErr)
			}
		})
		return db
	}
	for i := range cns {
		cns[i], err = cluster.GetCNService(i)
		require.NoError(t, err)
		getter, ok := cns[i].RawService().(testutil.TaskServiceGetter)
		require.True(t, ok)
		require.NoError(t, testutil.WaitTaskServiceReady(ctx, getter, 100*time.Millisecond))
		address := fmt.Sprintf("127.0.0.1:%d", cns[i].GetServiceConfig().CN.Frontend.Port)
		db := open(address, "dump", "111")
		require.NoError(t, testutils.WaitSystemBootstrap(ctx, db))
		admins[i], err = db.Conn(ctx)
		require.NoError(t, err)
		conn := admins[i]
		t.Cleanup(func() {
			if closeErr := conn.Close(); closeErr != nil {
				t.Errorf("close admin connection: %v", closeErr)
			}
		})
	}
	exec := func(statement string) {
		t.Helper()
		_, execErr := admins[0].ExecContext(ctx, statement)
		require.NoError(t, execErr, statement)
	}
	for _, statement := range []string{
		"create role pr31460_role_a",
		"create role pr31460_role_b",
		"create database pr31460_identity",
		"create table pr31460_identity.a_only (id int)",
		"insert into pr31460_identity.a_only values (1)",
		"create table pr31460_identity.b_only (id int)",
		"insert into pr31460_identity.b_only values (2)",
		"create user pr31460_a identified by 'ProxyRenamePassword1'",
		"create user pr31460_b identified by 'ProxyRenamePassword1'",
		"grant pr31460_role_a to pr31460_a",
		"grant pr31460_role_b to pr31460_b",
		"grant select on table pr31460_identity.a_only to pr31460_role_a",
		"grant select on table pr31460_identity.b_only to pr31460_role_b",
		"set default role pr31460_role_a to pr31460_a",
		"set default role pr31460_role_b to pr31460_b",
	} {
		exec(statement)
	}
	var originalID, otherID uint32
	require.NoError(t, admins[0].QueryRowContext(ctx, "select user_id from mo_catalog.mo_user where user_name='pr31460_a'").Scan(&originalID))
	require.NoError(t, admins[0].QueryRowContext(ctx, "select user_id from mo_catalog.mo_user where user_name='pr31460_b'").Scan(&otherID))
	require.NotZero(t, originalID)
	require.NotZero(t, otherID)
	require.NotEqual(t, originalID, otherID)

	// Wait for the peer's logtail to apply the writer's committed catalog state.
	waitPeer := func() {
		t.Helper()
		writer := cns[0].RawService().(cnservice.Service)
		peer := cns[1].RawService().(cnservice.Service)
		frontier := writer.GetTxnClient().GetLatestCommitTS()
		require.False(t, frontier.IsEmpty())
		waitCtx, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		applied, waitErr := peer.GetTxnClient().WaitLogTailAppliedAt(waitCtx, frontier)
		require.NoError(t, waitErr)
		require.True(t, frontier.Less(applied))
	}
	waitPeer()

	proxyID := "pr31460-proxy-" + uuid.NewString()
	rt := moruntime.NewRuntime(metadata.ServiceType_PROXY, proxyID, zap.NewNop())
	moruntime.SetupServiceBasedRuntime(proxyID, rt)
	t.Cleanup(func() { moruntime.CompareAndDeleteServiceBasedRuntime(proxyID, rt) })
	cfg := proxy.Config{UUID: proxyID, ListenAddress: "127.0.0.1:0", RebalanceDisabled: true}
	cfg.HAKeeper.ClientConfig = cns[0].GetServiceConfig().HAKeeperClient
	server, err := proxy.NewServer(ctx, cfg, proxy.WithRuntime(rt))
	require.NoError(t, err)
	proxyClosed = false
	t.Cleanup(func() {
		if closeErr := server.Close(); closeErr != nil {
			t.Errorf("close owned proxy: %v", closeErr)
		} else {
			proxyClosed = true
		}
	})
	probe, err := proxy.ExportInstallMigrationProbe(server)
	require.NoError(t, err)
	t.Cleanup(probe.Release)
	require.NoError(t, server.Start())

	inventory, err := clusterservice.GetMOClusterWithContext(ctx, proxyID)
	require.NoError(t, err)
	refresher, ok := inventory.(clusterservice.AuthoritativeRefresher)
	require.True(t, ok)
	readyCtx, stopReady := context.WithTimeout(ctx, 30*time.Second)
	defer stopReady()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	cnInfo := make(map[string]metadata.CNService)
	for len(cnInfo) != 2 {
		require.NoError(t, refresher.Refresh(readyCtx))
		clear(cnInfo)
		inventory.GetCNService(clusterservice.NewSelectAll(), func(info metadata.CNService) bool {
			if (info.ServiceID == cns[0].ServiceID() || info.ServiceID == cns[1].ServiceID()) &&
				info.WorkState == metadata.WorkState_Working && info.SQLAddress != "" && info.QueryAddress != "" {
				cnInfo[info.ServiceID] = info
			}
			return true
		})
		if len(cnInfo) == 2 {
			break
		}
		select {
		case <-readyCtx.Done():
			t.Fatal("proxy did not discover both owned CNs")
		case <-ticker.C:
		}
	}

	login := "sys#pr31460_a"
	principal, err := frontend.GetTenantInfo(ctx, login)
	require.NoError(t, err)
	require.Equal(t, "sys", principal.GetTenant())
	require.Equal(t, "pr31460_a", principal.GetUser())
	require.Empty(t, principal.GetDefaultRole())
	proxied := open(proxy.ExportProxyAddress(server), login, "ProxyRenamePassword1")
	source, err := proxied.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		if closeErr := source.Close(); closeErr != nil {
			t.Errorf("close source connection: %v", closeErr)
		}
	})
	var connectionID uint32
	require.NoError(t, source.QueryRowContext(ctx, "select connection_id()").Scan(&connectionID))
	require.NotZero(t, connectionID)
	probe.SetConnID(connectionID)
	sourceCN, err := proxy.ExportConnectionCN(server, connectionID)
	require.NoError(t, err)
	require.Contains(t, cnInfo, sourceCN)
	targetIndex := 0
	if sourceCN == cns[0].ServiceID() {
		targetIndex = 1
	}
	targetCN := cns[targetIndex].ServiceID()
	require.NotEqual(t, sourceCN, targetCN)
	sourceStatus, err := proxy.ExportQuerySession(ctx, server, cnInfo[sourceCN].QueryAddress, connectionID)
	require.NoError(t, err)
	require.Equal(t, sourceCN, sourceStatus.NodeID)
	require.Equal(t, "pr31460_a", sourceStatus.User)
	require.Equal(t, "pr31460_role_a", sourceStatus.Role)
	require.True(t, sourceStatus.FromProxy)
	require.NotEmpty(t, sourceStatus.SessionID)

	var count int
	require.NoError(t, source.QueryRowContext(ctx, "select count(*) from pr31460_identity.a_only").Scan(&count))
	require.Equal(t, 1, count)
	exec("rename user pr31460_a to pr31460_moved, pr31460_b to pr31460_a")
	waitPeer()
	var movedID, reusedID uint32
	require.NoError(t, admins[0].QueryRowContext(ctx, "select user_id from mo_catalog.mo_user where user_name='pr31460_moved'").Scan(&movedID))
	require.NoError(t, admins[0].QueryRowContext(ctx, "select user_id from mo_catalog.mo_user where user_name='pr31460_a'").Scan(&reusedID))
	require.Equal(t, originalID, movedID)
	require.Equal(t, otherID, reusedID)

	// The target really authenticates the old name as B, using the same password.
	targetAddress := fmt.Sprintf("127.0.0.1:%d", cns[targetIndex].GetServiceConfig().CN.Frontend.Port)
	targetDB := open(targetAddress, login, "ProxyRenamePassword1")
	targetConn, err := targetDB.Conn(ctx)
	require.NoError(t, err)
	targetClosed := false
	t.Cleanup(func() {
		if !targetClosed {
			if closeErr := targetConn.Close(); closeErr != nil {
				t.Errorf("close target connection: %v", closeErr)
			}
		}
	})
	require.NoError(t, targetConn.QueryRowContext(ctx, "select count(*) from pr31460_identity.b_only").Scan(&count))
	require.Equal(t, 1, count)
	err = targetConn.QueryRowContext(ctx, "select count(*) from pr31460_identity.a_only").Scan(&count)
	require.ErrorContains(t, err, "privilege")
	require.NoError(t, targetConn.Close())
	targetClosed = true
	require.NoError(t, targetDB.Close())
	require.NoError(t, source.PingContext(ctx))

	transferCtx, stopTransfer := context.WithTimeout(ctx, 30*time.Second)
	transferDone = make(chan error, 1)
	t.Cleanup(func() {
		probe.Release()
		stopTransfer()
		if !transferJoined {
			select {
			case <-transferDone:
				transferJoined = true
			case <-time.After(10 * time.Second):
				t.Error("transfer did not stop during cleanup")
			}
		}
	})
	go func() { transferDone <- proxy.ExportTransferConnection(transferCtx, server, sourceCN, connectionID) }()

	var from proxy.ExportMigrationSource
	select {
	case from = <-probe.Source:
	case transferErr := <-transferDone:
		transferJoined = true
		t.Fatalf("transfer ended before source migration RPC: %v", transferErr)
	case <-transferCtx.Done():
		t.Fatal("source migration RPC was not observed")
	}
	require.NoError(t, from.Err)
	require.Equal(t, cnInfo[sourceCN].QueryAddress, from.Address)
	require.Equal(t, catalog.System_Account, from.AccountID)
	require.Equal(t, originalID, from.UserID)
	require.True(t, from.PrincipalExported)
	require.True(t, from.LastInsertIDExported)

	var before proxy.ExportMigrationTarget
	select {
	case before = <-probe.BeforeTarget:
	case transferErr := <-transferDone:
		transferJoined = true
		t.Fatalf("transfer ended before target migration RPC: %v", transferErr)
	case <-transferCtx.Done():
		t.Fatal("target migration RPC was not reached")
	}
	require.Equal(t, cnInfo[targetCN].QueryAddress, before.Address)
	require.Equal(t, connectionID, before.ConnID)
	require.Equal(t, catalog.System_Account, before.AccountID)
	require.Equal(t, originalID, before.UserID)
	require.True(t, before.PrincipalExported)
	require.True(t, before.LastInsertIDExported)

	// At the barrier the new backend handshake has completed, but the real
	// MigrateConnTo RPC has not yet run. Its current role identifies B.
	targetStatus, err := proxy.ExportQuerySession(transferCtx, server, before.Address, connectionID)
	require.NoError(t, err)
	require.Equal(t, targetCN, targetStatus.NodeID)
	require.Equal(t, "pr31460_a", targetStatus.User)
	require.Equal(t, "pr31460_role_b", targetStatus.Role)
	require.True(t, targetStatus.FromProxy)
	require.NotEmpty(t, targetStatus.SessionID)
	require.NotEqual(t, sourceStatus.SessionID, targetStatus.SessionID)
	probe.Release()

	var after proxy.ExportMigrationTarget
	select {
	case after = <-probe.AfterTarget:
	case <-transferCtx.Done():
		t.Fatal("target migration RPC did not complete")
	}
	require.False(t, after.Success)
	require.True(t, moerr.IsMoErrCode(after.Err, moerr.OkExpectedNotSafeToStartTransfer), "target must reject the changed principal")
	select {
	case err = <-transferDone:
		transferJoined = true
		require.NoError(t, err)
	case <-transferCtx.Done():
		t.Fatal("proxy transfer did not terminate")
	}

	currentCN, err := proxy.ExportConnectionCN(server, connectionID)
	require.NoError(t, err)
	require.Equal(t, sourceCN, currentCN)
	sourceAfter, err := proxy.ExportQuerySession(ctx, server, cnInfo[sourceCN].QueryAddress, connectionID)
	require.NoError(t, err)
	require.Equal(t, sourceStatus.SessionID, sourceAfter.SessionID)
	require.Equal(t, "pr31460_role_a", sourceAfter.Role)
	var afterConnectionID uint32
	require.NoError(t, source.QueryRowContext(ctx, "select connection_id()").Scan(&afterConnectionID))
	require.Equal(t, connectionID, afterConnectionID)
	require.NoError(t, source.QueryRowContext(ctx, "select count(*) from pr31460_identity.a_only").Scan(&count))
	require.Equal(t, 1, count)
	err = source.QueryRowContext(ctx, "select count(*) from pr31460_identity.b_only").Scan(&count)
	require.ErrorContains(t, err, "privilege")
}
