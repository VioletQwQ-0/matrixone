package embed

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPR31460SnapshotAndSQLPrepare keeps one authenticated session and one
// transaction across a rename and reuse of its original login name.
func TestPR31460SnapshotAndSQLPrepare(t *testing.T) {
	defer func() { require.NoError(t, CloseBaseClusterTests()) }()
	RunBaseClusterTests(t, func(cluster Cluster) {
		cn, err := cluster.GetCNService(0)
		require.NoError(t, err)
		port := cn.GetServiceConfig().CN.Frontend.Port
		admin, err := sql.Open("mysql", fmt.Sprintf("dump:111@tcp(127.0.0.1:%d)/", port))
		require.NoError(t, err)
		defer admin.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		a, err := admin.Conn(ctx)
		require.NoError(t, err)
		defer a.Close()
		exec := func(s string) { t.Helper(); _, err := a.ExecContext(ctx, s); require.NoError(t, err, s) }
		cleanup := func() {
			for _, s := range []string{"drop user if exists pr31460_snap_a, pr31460_snap_b, pr31460_snap_moved", "drop role if exists pr31460_snap_role_a, pr31460_snap_role_b", "drop database if exists pr31460_snap_db"} {
				_, _ = a.ExecContext(context.Background(), s)
			}
		}
		cleanup()
		defer cleanup()
		exec("create database pr31460_snap_db")
		exec("create table pr31460_snap_db.ta (v int)")
		exec("create table pr31460_snap_db.tb (v int)")
		exec("create role pr31460_snap_role_a")
		exec("create role pr31460_snap_role_b")
		exec("grant connect on account * to pr31460_snap_role_a")
		exec("grant connect on account * to pr31460_snap_role_b")
		exec("grant select on table pr31460_snap_db.ta to pr31460_snap_role_a")
		exec("grant insert on table pr31460_snap_db.tb to pr31460_snap_role_b")
		exec("create user pr31460_snap_a identified by 'pw31460'")
		exec("create user pr31460_snap_b identified by 'pw31460'")
		exec("grant pr31460_snap_role_a to pr31460_snap_a")
		exec("grant pr31460_snap_role_b to pr31460_snap_b")
		exec("set default role pr31460_snap_role_a to pr31460_snap_a")
		exec("set default role pr31460_snap_role_b to pr31460_snap_b")
		user, err := sql.Open("mysql", fmt.Sprintf("pr31460_snap_a:pw31460@tcp(127.0.0.1:%d)/", port))
		require.NoError(t, err)
		defer user.Close()
		u, err := user.Conn(ctx)
		require.NoError(t, err)
		defer u.Close()
		check := func(q, name string) {
			t.Helper()
			rows, err := u.QueryContext(ctx, q)
			require.NoError(t, err, q)
			defer rows.Close()
			cols, err := rows.Columns()
			require.NoError(t, err)
			require.Equal(t, []string{"Grants for " + name + "@localhost"}, cols, q)
			var grants []string
			for rows.Next() {
				var s string
				require.NoError(t, rows.Scan(&s))
				grants = append(grants, s)
			}
			require.NoError(t, rows.Err())
			joined := strings.Join(grants, "\n")
			require.Contains(t, joined, "GRANT select ON table d.t `"+name+"`@`localhost`")
			require.NotContains(t, joined, "GRANT insert ON table d.t")
		}
		_, err = u.ExecContext(ctx, "prepare pr31460_grants from 'show grants'")
		require.NoError(t, err)
		_, err = u.ExecContext(ctx, "begin")
		require.NoError(t, err)
		check("show grants", "pr31460_snap_a")
		exec("rename user pr31460_snap_a to pr31460_snap_moved, pr31460_snap_b to pr31460_snap_a")
		check("show grants", "pr31460_snap_a")
		check("execute pr31460_grants", "pr31460_snap_a")
		_, err = u.ExecContext(ctx, "commit")
		require.NoError(t, err)
		check("show grants", "pr31460_snap_moved")
		check("show grants for current_user()", "pr31460_snap_moved")
		check("execute pr31460_grants", "pr31460_snap_moved")
		_, err = u.ExecContext(ctx, "deallocate prepare pr31460_grants")
		require.NoError(t, err)
	})
}
