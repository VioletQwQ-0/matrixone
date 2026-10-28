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

package embed

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// TestRenameUserIdentity exercises public SQL against the task-owned cluster.
// Engine race/multi-CN/restart validation is separate from this single-CN BVT.
func TestRenameUserIdentity(t *testing.T) {
	defer func() { require.NoError(t, CloseBaseClusterTests()) }()
	RunBaseClusterTests(t, func(cluster Cluster) {
		cn, err := cluster.GetCNService(0)
		require.NoError(t, err)
		db, err := sql.Open("mysql", fmt.Sprintf("dump:111@tcp(127.0.0.1:%d)/", cn.GetServiceConfig().CN.Frontend.Port))
		require.NoError(t, err)
		defer db.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
		defer cancel()
		conn, err := db.Conn(ctx)
		require.NoError(t, err)
		defer conn.Close()
		defer func() {
			cleanup, c := context.WithTimeout(context.Background(), time.Minute)
			defer c()
			_, _ = conn.ExecContext(cleanup, "drop database if exists rename_identity_db")
			_, _ = conn.ExecContext(cleanup, "drop user if exists rename_src, rename_dst, rename_end, rename_taken, rename_moved")
			_, _ = conn.ExecContext(cleanup, "drop role if exists rename_role")
			_, _ = conn.ExecContext(cleanup, "drop role if exists rename_other_role")
		}()
		exec := func(query string) { t.Helper(); _, err := conn.ExecContext(ctx, query); require.NoError(t, err, query) }
		count := func(query string, want int) {
			t.Helper()
			var n int
			require.NoError(t, conn.QueryRowContext(ctx, query).Scan(&n))
			require.Equal(t, want, n, query)
		}
		exec("drop database if exists rename_identity_db")
		exec("drop user if exists rename_src, rename_dst, rename_end, rename_taken, rename_moved")
		exec("drop role if exists rename_role")
		exec("drop role if exists rename_other_role")
		exec("create role rename_role")
		exec("create role rename_other_role")
		exec("create database rename_identity_db")
		exec("create table rename_identity_db.source_only (id int)")
		exec("insert into rename_identity_db.source_only values (1)")
		exec("create table rename_identity_db.other_only (id int)")
		exec("insert into rename_identity_db.other_only values (2)")
		exec("create user rename_src identified by 'RenameTestPassword1'")
		exec("create user rename_taken identified by 'RenameTestPassword1'")
		exec("grant rename_role to rename_src")
		exec("grant rename_other_role to rename_taken")
		exec("grant select on table rename_identity_db.source_only to rename_role")
		exec("grant select on table rename_identity_db.other_only to rename_other_role")
		exec("set default role rename_role to rename_src")
		exec("set default role rename_other_role to rename_taken")
		var id int64
		var password string
		require.NoError(t, conn.QueryRowContext(ctx, "select user_id, authentication_string from mo_catalog.mo_user where user_name='rename_src'").Scan(&id, &password))
		var otherID int64
		require.NoError(t, conn.QueryRowContext(ctx, "select user_id from mo_catalog.mo_user where user_name='rename_taken'").Scan(&otherID))
		require.NotEqual(t, id, otherID)
		_, err = conn.ExecContext(ctx, "rename user rename_src to rename_dst, rename_taken to rename_dst")
		require.Error(t, err)
		count("select count(*) from mo_catalog.mo_user where user_name='rename_src'", 1)
		count("select count(*) from mo_catalog.mo_user where user_name='rename_dst'", 0)
		exec("rename user rename_src to rename_dst, rename_dst to rename_end")
		var newID int64
		var newPassword string
		require.NoError(t, conn.QueryRowContext(ctx, "select user_id, authentication_string from mo_catalog.mo_user where user_name='rename_end'").Scan(&newID, &newPassword))
		require.Equal(t, id, newID)
		require.Equal(t, password, newPassword)
		count(fmt.Sprintf("select count(*) from mo_catalog.mo_user_grant g join mo_catalog.mo_role r on g.role_id=r.role_id where g.user_id=%d and r.role_name='rename_role'", id), 1)

		// Keep a same-password connection to the original user open across a
		// name exchange. Its default role must remain attached to the original ID.
		sourceDB, err := sql.Open("mysql", fmt.Sprintf("rename_end:RenameTestPassword1@tcp(127.0.0.1:%d)/", cn.GetServiceConfig().CN.Frontend.Port))
		require.NoError(t, err)
		defer sourceDB.Close()
		sourceConn, err := sourceDB.Conn(ctx)
		require.NoError(t, err)
		defer sourceConn.Close()
		require.NoError(t, sourceConn.PingContext(ctx))
		var rows int
		require.NoError(t, sourceConn.QueryRowContext(ctx, "select count(*) from rename_identity_db.source_only").Scan(&rows))
		require.Equal(t, 1, rows)
		// The implicit forms keep their historical column name before rename.
		readGrantRows := func(result *sql.Rows, err error) ([]string, []string) {
			t.Helper()
			require.NoError(t, err)
			defer result.Close()
			columns, err := result.Columns()
			require.NoError(t, err)
			var grants []string
			for result.Next() {
				var grant string
				require.NoError(t, result.Scan(&grant))
				grants = append(grants, grant)
			}
			require.NoError(t, result.Err())
			return columns, grants
		}
		readGrants := func(query string) ([]string, []string) {
			t.Helper()
			result, err := sourceConn.QueryContext(ctx, query)
			return readGrantRows(result, err)
		}
		preparedGrants, err := sourceConn.PrepareContext(ctx, "show grants")
		require.NoError(t, err)
		defer preparedGrants.Close()
		for _, query := range []string{"show grants", "show grants for current_user()"} {
			columns, grants := readGrants(query)
			require.Equal(t, []string{"Grants for rename_end@localhost"}, columns, query)
			require.Contains(t, grants, "GRANT select ON table d.t `rename_end`@`localhost`", query)
		}
		preparedRows, err := preparedGrants.QueryContext(ctx)
		columns, grants := readGrantRows(preparedRows, err)
		require.Equal(t, []string{"Grants for rename_end@localhost"}, columns)
		require.Contains(t, grants, "GRANT select ON table d.t `rename_end`@`localhost`")

		exec("rename user rename_end to rename_moved, rename_taken to rename_end")
		var movedID, currentNameID int64
		require.NoError(t, conn.QueryRowContext(ctx, "select user_id from mo_catalog.mo_user where user_name='rename_moved'").Scan(&movedID))
		require.NoError(t, conn.QueryRowContext(ctx, "select user_id from mo_catalog.mo_user where user_name='rename_end'").Scan(&currentNameID))
		require.Equal(t, id, movedID)
		require.Equal(t, otherID, currentNameID)
		count(fmt.Sprintf("select count(*) from mo_catalog.mo_user_grant g join mo_catalog.mo_role r on g.role_id=r.role_id where g.user_id=%d and r.role_name='rename_role'", movedID), 1)
		count(fmt.Sprintf("select count(*) from mo_catalog.mo_user_grant g join mo_catalog.mo_role r on g.role_id=r.role_id where g.user_id=%d and r.role_name='rename_other_role'", currentNameID), 1)
		require.NoError(t, sourceConn.QueryRowContext(ctx, "select count(*) from rename_identity_db.source_only").Scan(&rows))
		require.Equal(t, 1, rows)
		err = sourceConn.QueryRowContext(ctx, "select count(*) from rename_identity_db.other_only").Scan(&rows)
		require.Error(t, err)

		// Implicit grants follow the authenticated ID; explicit targets use names.
		for _, query := range []string{"show grants", "show grants for current_user()"} {
			columns, grants := readGrants(query)
			require.Equal(t, []string{"Grants for rename_moved@localhost"}, columns, query)
			require.Contains(t, grants, "GRANT select ON table d.t `rename_moved`@`localhost`", query)
			for _, grant := range grants {
				require.NotContains(t, grant, "`rename_end`", query)
			}
		}
		preparedRows, err = preparedGrants.QueryContext(ctx)
		columns, grants = readGrantRows(preparedRows, err)
		require.Equal(t, []string{"Grants for rename_moved@localhost"}, columns)
		require.Contains(t, grants, "GRANT select ON table d.t `rename_moved`@`localhost`")
		for _, grant := range grants {
			require.NotContains(t, grant, "`rename_end`")
		}
		columns, grants = readGrants("show grants for 'rename_end'@'localhost'")
		require.Equal(t, []string{"Grants for rename_end@localhost"}, columns)
		require.Contains(t, grants, "GRANT select ON table d.t `rename_end`@`localhost`")

		for _, query := range []string{"rename user rename_end to root", "rename user root to rename_src", "rename user rename_end@'remote' to rename_src", "rename user missing_user to rename_src", "rename user rename_end to rename_moved"} {
			_, err = conn.ExecContext(ctx, query)
			require.Error(t, err, query)
		}
		userDB, err := sql.Open("mysql", fmt.Sprintf("rename_end:RenameTestPassword1@tcp(127.0.0.1:%d)/", cn.GetServiceConfig().CN.Frontend.Port))
		require.NoError(t, err)
		defer userDB.Close()
		require.NoError(t, userDB.PingContext(ctx))
		var currentRows int
		require.NoError(t, userDB.QueryRowContext(ctx, "select count(*) from rename_identity_db.other_only").Scan(&currentRows))
		require.Equal(t, 1, currentRows)
		err = userDB.QueryRowContext(ctx, "select count(*) from rename_identity_db.source_only").Scan(&currentRows)
		require.Error(t, err)
		_, err = userDB.ExecContext(ctx, "rename user rename_moved to rename_src")
		require.Error(t, err)
	})
}
