package embed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestPR31460FrozenCanonicalBVT(t *testing.T) {
	out := os.Getenv("MO_MAINTENANCE_EVIDENCE")
	require.NotEmpty(t, out)
	require.NoError(t, os.MkdirAll(out, 0700))
	summary := map[string]any{"candidate_tree": "c862219d65240ce822aa6bc06f5c69763f5fba32", "metadata_comparison": true, "same_instance": true}
	runs := []map[string]any{}
	t.Cleanup(func() {
		summary["runs"] = runs
		summary["failed"] = t.Failed()
		b, e := json.MarshalIndent(summary, "", "  ")
		if e != nil {
			t.Error(e)
			return
		}
		if e = os.WriteFile(filepath.Join(out, "canonical-summary.json"), append(b, '\n'), 0600); e != nil {
			t.Error(e)
		}
	})
	var port int
	t.Cleanup(func() {
		e := CloseSingleCNBaseClusterTests()
		summary["cluster_close_error"] = fmt.Sprint(e)
		if e != nil {
			t.Error(e)
			return
		}
		summary["cluster_closed"] = true
		if port != 0 {
			c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
			if c != nil {
				c.Close()
			}
			summary["sql_listener_closed"] = e != nil
			if e == nil {
				t.Error("SQL listener survived fixture close")
			}
		}
	})
	stage, e := os.MkdirTemp("/tmp", "mo-pr31460-")
	require.NoError(t, e)
	t.Cleanup(func() {
		e := os.RemoveAll(stage)
		summary["owned_input_staging_removed"] = e == nil
		if e != nil {
			t.Error(e)
		}
	})
	const root = "/go/src/github.com/matrixorigin/matrixone/test/distributed/cases/zz_accesscontrol/"
	digest := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	input, e := os.ReadFile(root + "rename_user.sql")
	require.NoError(t, e)
	oracle, e := os.ReadFile(root + "rename_user.result")
	require.NoError(t, e)
	require.Equal(t, "ec93d3bf9863eb6a897c9dc8bae63b24a710ee8920ef86d0cd5d7013b3590be1", digest(input))
	require.Equal(t, "7dba153b8f78f6d633c43e91d10e563d75ac517994982ec6f44db16f4d505abd", digest(oracle))
	require.NotRegexp(t, regexp.MustCompile(`(?mi)^\s*--\s*@(?:skip|bvt:issue)`), string(input))
	require.NoError(t, os.WriteFile(filepath.Join(stage, "rename_user.sql"), input, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(stage, "rename_user.result"), oracle, 0600))
	jars, e := filepath.Glob("/tester/lib/*.jar")
	require.NoError(t, e)
	require.NotEmpty(t, jars)
	conf, e := os.ReadFile("/tester/mo.yml")
	require.NoError(t, e)
	rc, e := os.ReadFile("/tester/run.yml")
	require.NoError(t, e)
	rc = regexp.MustCompile(`(?m)^builtindb: `).ReplaceAll(rc, []byte("builtindb: qa_pr31460_entry,rename_user_bvt_db,"))
	require.Contains(t, string(rc), "qa_pr31460_entry,rename_user_bvt_db,")
	logger, e := os.ReadFile("/tester/log4j.properties")
	require.NoError(t, e)
	logger = []byte(strings.Replace(string(logger), "log4j.rootLogger=INFO", "log4j.rootLogger=DEBUG", 1))
	RunSingleCNBaseClusterTests(t, func(c Cluster) {
		cn, e := c.GetCNService(0)
		require.NoError(t, e)
		require.False(t, cn.GetServiceConfig().CN.Frontend.SkipCheckUser, "catalog authentication required")
		summary["skip_check_user"] = false
		summary["cn_service_id"] = cn.ServiceID()
		port = int(cn.GetServiceConfig().CN.Frontend.Port)
		cfg := mysql.NewConfig()
		cfg.User = "dump"
		cfg.Passwd = "111"
		cfg.Net = "tcp"
		cfg.Addr = fmt.Sprintf("127.0.0.1:%d", port)
		cfg.Timeout = 10 * time.Second
		cfg.ReadTimeout = 30 * time.Second
		cfg.WriteTimeout = 10 * time.Second
		db, e := sql.Open("mysql", cfg.FormatDSN())
		require.NoError(t, e)
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		require.NoError(t, db.PingContext(ctx))
		for {
			b, e := os.ReadFile(os.Getenv("MO_MAINTENANCE_SERVICE_LOG"))
			require.NoError(t, e)
			if strings.Contains(string(b), "ISCP-Task Start") {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("ISCP readiness missing")
			case <-time.After(250 * time.Millisecond):
			}
		}
		queries := map[string]string{
			"users":           "select count(*) from mo_catalog.mo_user where user_name in ('rename_bvt_src','rename_bvt_dst','rename_bvt_moved','rename_bvt_taken','rename_bvt_tmp','rename_bvt_end')",
			"roles":           "select count(*) from mo_catalog.mo_role where role_name in ('rename_bvt_reader','rename_bvt_other','rename_bvt_extra')",
			"accounts":        "select count(*) from mo_catalog.mo_account where account_name='rename_bvt_tenant'",
			"case_database":   "select count(*) from mo_catalog.mo_database where datname='rename_user_bvt_db'",
			"case_tables":     "select count(*) from mo_catalog.mo_tables where reldatabase='rename_user_bvt_db'",
			"case_columns":    "select count(*) from mo_catalog.mo_columns where att_database='rename_user_bvt_db'",
			"tester_database": "select count(*) from mo_catalog.mo_database where datname='rename_user'",
		}
		snapshot := func() (map[string]int, error) {
			s := map[string]int{}
			qctx, qcancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer qcancel()
			for k, q := range queries {
				var n int
				if e := db.QueryRowContext(qctx, q).Scan(&n); e != nil {
					return s, fmt.Errorf("%s: %w", k, e)
				}
				s[k] = n
			}
			return s, nil
		}
		empty := func(s map[string]int) bool {
			if len(s) != len(queries) {
				return false
			}
			for _, n := range s {
				if n != 0 {
					return false
				}
			}
			return true
		}
		initial, e := snapshot()
		require.NoError(t, e)
		summary["initial"] = initial
		require.True(t, empty(initial), "initial residue")
		defer func() {
			before, e := snapshot()
			summary["before_fallback"] = before
			if e != nil {
				t.Errorf("fallback observation: %v", e)
			}
			fallback := []string{"drop account if exists rename_bvt_tenant", "drop user if exists rename_bvt_src, rename_bvt_dst, rename_bvt_moved, rename_bvt_taken, rename_bvt_tmp, rename_bvt_end", "drop role if exists rename_bvt_reader, rename_bvt_other, rename_bvt_extra", "drop database if exists rename_user_bvt_db", "drop database if exists rename_user", "drop database if exists qa_pr31460_entry"}
			errs := []string{}
			clctx, clcancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer clcancel()
			for _, q := range fallback {
				if _, err := db.ExecContext(clctx, q); err != nil {
					errs = append(errs, fmt.Sprint(err))
				}
			}
			summary["fallback_errors"] = errs
			final, err := snapshot()
			summary["final"] = final
			if err != nil || !empty(final) || len(errs) > 0 {
				t.Errorf("owned fixture fallback incomplete: %v", err)
			}
		}()
		_, e = db.ExecContext(ctx, "create database qa_pr31460_entry")
		require.NoError(t, e)
		for i := 1; i <= 2; i++ {
			pre, e := snapshot()
			require.NoError(t, e)
			if !empty(pre) {
				t.Errorf("repetition %d not admitted due to residue", i)
				break
			}
			wd := filepath.Join(out, fmt.Sprintf("run%d", i))
			require.NoError(t, os.MkdirAll(filepath.Join(wd, "log"), 0700))
			config := regexp.MustCompile(`(?m)^  - addr:.*$`).ReplaceAllString(string(conf), fmt.Sprintf("  - addr: 127.0.0.1:%d", port))
			config = regexp.MustCompile(`(?m)^    default:.*$`).ReplaceAllString(config, "    default: mo_catalog")
			config = regexp.MustCompile(`(?m)^  port:.*$`).ReplaceAllString(config, fmt.Sprintf("  port: %d", port))
			require.NoError(t, os.WriteFile(filepath.Join(wd, "mo.yml"), []byte(config), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(wd, "run.yml"), rc, 0600))
			require.NoError(t, os.WriteFile(filepath.Join(wd, "log4j.properties"), logger, 0600))
			log, e := os.Create(filepath.Join(wd, "tester.log"))
			require.NoError(t, e)
			args := []string{"-Xms256M", "-Xmx1024M", "-cp", strings.Join(append([]string{wd}, jars...), string(os.PathListSeparator)), "-Dconf.yml=" + filepath.Join(wd, "mo.yml"), "-Drun.yml=" + filepath.Join(wd, "run.yml"), "io.mo.Tester", "path=" + filepath.Join(stage, "rename_user.sql"), "method=run", "rate=100", "ignore", "pprof"}
			rctx, rcancel := context.WithTimeout(context.Background(), 240*time.Second)
			cmd := exec.CommandContext(rctx, "/jdk/bin/java", args...)
			cmd.Dir = wd
			cmd.Stdout = log
			cmd.Stderr = log
			cmd.WaitDelay = 5 * time.Second
			err := cmd.Run()
			rcancel()
			require.NoError(t, log.Close())
			exit := -1
			if cmd.ProcessState != nil {
				exit = cmd.ProcessState.ExitCode()
			}
			data, e := os.ReadFile(filepath.Join(wd, "tester.log"))
			require.NoError(t, e)
			pat := regexp.MustCompile(`(?m)^.*The script file\[[^\n]*rename_user\.sql\][^\n]*total:([0-9]+), success:([0-9]+), failed:([0-9]+), ignored:([0-9]+), abnoraml:([0-9]+)`)
			m := pat.FindStringSubmatch(string(data))
			counts := map[string]int{}
			if len(m) == 6 {
				for j, k := range []string{"total", "success", "failed", "ignored", "abnormal"} {
					counts[k], _ = strconv.Atoi(m[j+1])
				}
			}
			post, postErr := snapshot()
			var reports []string
			_ = filepath.WalkDir(wd, func(p string, d os.DirEntry, e error) error {
				if e == nil && !d.IsDir() && (d.Name() == "report.txt" || d.Name() == "report.xml" || d.Name() == "error.txt" || d.Name() == "success.txt") {
					reports = append(reports, p)
				}
				return nil
			})
			run := map[string]any{"repetition": i, "preconditions": pre, "postconditions_before_fallback": post, "postcondition_error": fmt.Sprint(postErr), "exit_code": exit, "command_error": fmt.Sprint(err), "counts": counts, "reports": reports}
			runs = append(runs, run)
			stagedSQL, e := os.ReadFile(filepath.Join(stage, "rename_user.sql"))
			require.NoError(t, e)
			stagedResult, e := os.ReadFile(filepath.Join(stage, "rename_user.result"))
			require.NoError(t, e)
			require.Equal(t, input, stagedSQL)
			require.Equal(t, oracle, stagedResult)
			reportOK := false
			for _, p := range reports {
				if filepath.Base(p) == "report.txt" {
					b, _ := os.ReadFile(p)
					if strings.Contains(string(b), "rename_user.sql") {
						reportOK = true
					}
				}
			}
			ok := err == nil && postErr == nil && empty(post) && len(m) == 6 && counts["total"] == 77 && counts["success"] == 77 && counts["failed"] == 0 && counts["ignored"] == 0 && counts["abnormal"] == 0 && reportOK
			run["pass"] = ok
			if !ok {
				t.Errorf("canonical comparison or postcondition failed in repetition %d; see retained evidence", i)
			}
			if postErr != nil || !empty(post) {
				break
			}
		}
	})
}
