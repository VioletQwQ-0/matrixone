package embed

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIssue28162ExactMergedOriginalSQL(t *testing.T) {
	defer func() { require.NoError(t, CloseBaseClusterTests()) }()
	RunBaseClusterTests(t, func(cluster Cluster) {
		cn, err := cluster.GetCNService(0)
		require.NoError(t, err)
		db, err := sql.Open("mysql", fmt.Sprintf("dump:111@tcp(127.0.0.1:%d)/", cn.GetServiceConfig().CN.Frontend.Port))
		require.NoError(t, err)
		defer db.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		conn, err := db.Conn(ctx)
		require.NoError(t, err)
		defer conn.Close()
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
			defer stop()
			_, cleanupErr := conn.ExecContext(cleanup, "drop database if exists qa_issue_28162_exact")
			if cleanupErr != nil {
				t.Errorf("drop owned database: %v", cleanupErr)
			}
		}()
		exec := func(q string) sql.Result {
			t.Helper()
			v, e := conn.ExecContext(ctx, q)
			require.NoError(t, e)
			return v
		}
		exec("drop database if exists qa_issue_28162_exact")
		exec("create database qa_issue_28162_exact")
		exec("use qa_issue_28162_exact")
		exec("create table t (id bigint auto_increment primary key, k varchar(32) unique, v int)")
		inserted := exec("insert into t(k,v) values ('x',10)")
		id, err := inserted.LastInsertId()
		require.NoError(t, err)
		require.Equal(t, int64(1), id)
		for i, q := range []string{
			"insert into t(k,v) values ('x',10) on duplicate key update v=values(v)",
			"insert into t(k,v) values ('x',11) on duplicate key update v=values(v)",
		} {
			result := exec(q)
			packetID, e := result.LastInsertId()
			require.NoError(t, e)
			var sessionID, rowID, value int64
			require.NoError(t, conn.QueryRowContext(ctx, "select last_insert_id()").Scan(&sessionID))
			require.NoError(t, conn.QueryRowContext(ctx, "select id,v from t where k='x'").Scan(&rowID, &value))
			require.Equal(t, int64(1), rowID)
			require.Equal(t, int64(10+i), value)
			require.Equal(t, int64(1), sessionID, "issue requires LAST_INSERT_ID to refer to the updated row")
			t.Logf("case=%d packet_last_insert_id=%d session_last_insert_id=%d row_id=%d", i, packetID, sessionID, rowID)
		}
	})
}
