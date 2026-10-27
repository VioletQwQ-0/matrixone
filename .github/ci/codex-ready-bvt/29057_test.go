package embed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// This external overlay reuses the existing single-CN fixture and the actual
// mo-tester comparator. It does not replace product code or canonical bytes.
func TestPR29057FrozenCanonicalBVT(t *testing.T) {
	evidence := os.Getenv("MO_MAINTENANCE_EVIDENCE")
	require.NotEmpty(t, evidence)
	require.NoError(t, os.MkdirAll(evidence, 0700))
	const entryDB = "qa_pr29057_validation1028_entry"
	tester := "/tester"
	digest := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	summary := map[string]any{"mode": "canonical method=run, ignore/pprof; metadata enabled", "same_instance": true, "process_pid": os.Getpid(), "started_at": time.Now().UTC(), "metadata_comparison": true, "product_override": false}
	pgid, err := syscall.Getpgid(os.Getpid())
	require.NoError(t, err)
	summary["process_pgid"] = pgid
	var runs []map[string]any
	t.Cleanup(func() {
		summary["runs"], summary["failed"], summary["finished_at"] = runs, t.Failed(), time.Now().UTC()
		b, err := json.MarshalIndent(summary, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(evidence, "canonical-summary.json"), append(b, '\n'), 0600))
	})
	listeners := func() []string {
		entries, err := os.ReadDir("/proc/self/fd")
		require.NoError(t, err)
		owned := map[string]bool{}
		for _, e := range entries {
			target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
			if err == nil && strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
				owned[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
			}
		}
		var result []string
		for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 10 || fields[3] != "0A" || !owned[fields[9]] {
					continue
				}
				parts := strings.Split(fields[1], ":")
				require.Len(t, parts, 2)
				port, err := strconv.ParseUint(parts[1], 16, 16)
				require.NoError(t, err)
				result = append(result, net.JoinHostPort("127.0.0.1", strconv.FormatUint(port, 10)))
			}
		}
		return result
	}
	before := listeners()
	summary["listeners_before"] = before
	var during []string
	t.Cleanup(func() {
		err := CloseSingleCNBaseClusterTests()
		summary["cluster_close_error"] = fmt.Sprint(err)
		require.NoError(t, err)
		after := listeners()
		summary["listeners_during"], summary["listeners_after"] = during, after
		for _, addr := range during {
			existed := false
			for _, old := range before {
				existed = existed || old == addr
			}
			if existed {
				continue
			}
			for _, live := range after {
				require.NotEqual(t, addr, live, "fixture listener survived close")
			}
			port := addr[strings.LastIndex(addr, ":")+1:]
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 300*time.Millisecond)
			if conn != nil {
				require.NoError(t, conn.Close())
			}
			require.Error(t, err, "fixture port remained reachable after close")
		}
		summary["cluster_closed"], summary["fixture_listeners_closed"] = true, true
	})
	jars, err := filepath.Glob(filepath.Join(tester, "lib", "*.jar"))
	require.NoError(t, err)
	require.NotEmpty(t, jars)
	jarHashes := map[string]string{}
	for _, jar := range jars {
		b, err := os.ReadFile(jar)
		require.NoError(t, err)
		jarHashes[jar] = digest(b)
	}
	summary["tester_jars"] = jarHashes
	conf, err := os.ReadFile(filepath.Join(tester, "mo.yml"))
	require.NoError(t, err)
	runConfig, err := os.ReadFile(filepath.Join(tester, "run.yml"))
	require.NoError(t, err)
	runConfig = regexp.MustCompile(`(?m)^builtindb: `).ReplaceAll(runConfig, []byte("builtindb: "+entryDB+","))
	require.Contains(t, string(runConfig), "builtindb: "+entryDB+",")
	logger, err := os.ReadFile(filepath.Join(tester, "log4j.properties"))
	require.NoError(t, err)
	cases := strings.Split(os.Getenv("MO_MAINTENANCE_CASES"), ",")
	require.Len(t, cases, 1)
	require.Equal(t, "/go/src/github.com/matrixorigin/matrixone/test/distributed/cases/dtype/binary_string_function_semantics.test", cases[0])
	var databases []string
	for _, file := range cases {
		databases = append(databases, strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)))
	}
	databases = append(databases, "binary_string_function_semantics")
	summary["basename_derived_and_explicit_databases"] = databases
	// Only transient byte-identical input copies live at a dot-free path:
	// the existing tester rewrites dot-letter sequences in the complete path.
	staging, err := os.MkdirTemp("/tmp", "mo-pr29057-canonical-")
	require.NoError(t, err)
	summary["owned_input_staging"] = staging
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(staging)); summary["owned_input_staging_removed"] = true })
	RunSingleCNBaseClusterTests(t, func(c Cluster) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			log, err := os.ReadFile(os.Getenv("MO_MAINTENANCE_SERVICE_LOG"))
			require.NoError(t, err)
			if strings.Contains(string(log), "ISCP-Task Start") {
				summary["iscp_ready_at"] = time.Now().UTC()
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("ISCP readiness was not observed")
			case <-tick.C:
			}
		}
		cn, err := c.GetCNService(0)
		require.NoError(t, err)
		port := cn.GetServiceConfig().CN.Frontend.Port
		summary["sql_port_private"] = port
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net, cfg.Addr = "dump", "111", "tcp", fmt.Sprintf("127.0.0.1:%d", port)
		cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 10*time.Second, 30*time.Second, 10*time.Second
		db, err := sql.Open("mysql", cfg.FormatDSN())
		require.NoError(t, err)
		defer db.Close()
		sqlCtx, sqlCancel := context.WithTimeout(context.Background(), 15*time.Second)
		var one int
		err = db.QueryRowContext(sqlCtx, "select 1").Scan(&one)
		sqlCancel()
		require.NoError(t, err)
		require.Equal(t, 1, one)
		summary["sql_ready_at"] = time.Now().UTC()
		during = listeners()
		var observeMu sync.Mutex
		dbIDs, tableIDs := map[uint64]bool{}, map[uint64]bool{}
		observedNames := map[string]bool{}
		observe := func(ctx context.Context) error {
			for _, name := range databases {
				rows, e := db.QueryContext(ctx, "select dat_id from mo_catalog.mo_database where datname = ?", name)
				if e != nil {
					return e
				}
				for rows.Next() {
					var id uint64
					if e = rows.Scan(&id); e != nil {
						rows.Close()
						return e
					}
					observeMu.Lock()
					dbIDs[id] = true
					observedNames[name] = true
					observeMu.Unlock()
				}
				e = rows.Err()
				rows.Close()
				if e != nil {
					return e
				}
				rows, e = db.QueryContext(ctx, "select rel_id from mo_catalog.mo_tables where reldatabase = ?", name)
				if e != nil {
					return e
				}
				for rows.Next() {
					var id uint64
					if e = rows.Scan(&id); e != nil {
						rows.Close()
						return e
					}
					observeMu.Lock()
					tableIDs[id] = true
					observeMu.Unlock()
				}
				e = rows.Err()
				rows.Close()
				if e != nil {
					return e
				}
			}
			return nil
		}
		absence := func(names []string) map[string]int {
			t.Helper()
			checkCtx, checkCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer checkCancel()
			result := map[string]int{}
			for _, name := range names {
				var dbCount, tableCount int
				require.NoError(t, db.QueryRowContext(checkCtx, "select count(*) from mo_catalog.mo_database where datname = ?", name).Scan(&dbCount))
				require.NoError(t, db.QueryRowContext(checkCtx, "select count(*) from mo_catalog.mo_tables where reldatabase = ?", name).Scan(&tableCount))
				result[name+".databases"], result[name+".tables"] = dbCount, tableCount
				var columnCount int
				require.NoError(t, db.QueryRowContext(checkCtx, "select count(*) from mo_catalog.mo_columns where att_database = ?", name).Scan(&columnCount))
				result[name+".columns"] = columnCount
			}
			observeMu.Lock()
			var dIDs, tIDs []string
			for id := range dbIDs {
				dIDs = append(dIDs, strconv.FormatUint(id, 10))
			}
			for id := range tableIDs {
				tIDs = append(tIDs, strconv.FormatUint(id, 10))
			}
			observeMu.Unlock()
			queries := map[string]string{}
			if len(dIDs) > 0 {
				queries["observed_ids.indexes"] = "select count(*) from mo_catalog.mo_indexes where database_id in (" + strings.Join(dIDs, ",") + ")"
			}
			if len(tIDs) > 0 {
				queries["observed_ids.columns"] = "select count(*) from mo_catalog.mo_columns where att_relname_id in (" + strings.Join(tIDs, ",") + ")"
				queries["observed_ids.active_iscp_jobs"] = "select count(*) from mo_catalog.mo_iscp_log where account_id=0 and drop_at is null and table_id in (" + strings.Join(tIDs, ",") + ")"
			}
			for key, q := range queries {
				var count int
				require.NoError(t, db.QueryRowContext(checkCtx, q).Scan(&count))
				result[key] = count
			}
			return result
		}
		assertAbsent := func(counts map[string]int) {
			t.Helper()
			for name, count := range counts {
				require.Zero(t, count, "residue at "+name)
			}
		}
		initial := absence(append(append([]string{}, databases...), entryDB))
		summary["initial_admission"] = initial
		assertAbsent(initial)
		// This fallback is registered before CREATE, but is not a success oracle.
		// Preserve catalog state before recovery, attempt every exact-owned DROP,
		// and only then record final absence. Never let fallback mask a failed run.
		defer func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cleanupCancel()
			var recovery []map[string]any
			for _, name := range append(append([]string{}, databases...), entryDB) {
				var count int
				queryErr := db.QueryRowContext(cleanupCtx, "select count(*) from mo_catalog.mo_database where datname = ?", name).Scan(&count)
				row := map[string]any{"database": name, "present_before_fallback": count, "query_error": fmt.Sprint(queryErr)}
				if queryErr != nil {
					t.Errorf("fallback catalog observation failed for owned database %s", name)
				} else if count != 0 {
					_, dropErr := db.ExecContext(cleanupCtx, "drop database if exists "+name)
					row["drop_error"] = fmt.Sprint(dropErr)
					if dropErr != nil {
						t.Errorf("fallback DROP failed for owned database %s", name)
					}
				}
				recovery = append(recovery, row)
			}
			summary["recovery_after_original_postconditions"] = recovery
			final := map[string]int{}
			for _, name := range append(append([]string{}, databases...), entryDB) {
				for _, item := range []struct{ suffix, query string }{
					{".databases", "select count(*) from mo_catalog.mo_database where datname = ?"},
					{".tables", "select count(*) from mo_catalog.mo_tables where reldatabase = ?"},
				} {
					var count int
					checkErr := db.QueryRowContext(cleanupCtx, item.query, name).Scan(&count)
					final[name+item.suffix] = count
					if checkErr != nil || count != 0 {
						t.Errorf("owned catalog cleanup incomplete: %s", name+item.suffix)
					}
				}
			}
			final = absence(append(append([]string{}, databases...), entryDB))
			assertAbsent(final)
			summary["final_catalog_absence"] = final
		}()
		setupCtx, setupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, err = db.ExecContext(setupCtx, "create database "+entryDB)
		setupCancel()
		require.NoError(t, err)
		expected := map[string]struct {
			Input, Oracle string
			Total         int
		}{
			"binary_string_function_semantics.test": {"930e205caceb30f68ca6914e4666579acf0df5715c2f30247a785b3a0d369823", "112b2e4ceb9dbd83b5bf6231022445852b659c947b699bf2a112628fc675ce15", 158},
		}
		for _, casePath := range cases {
			base := filepath.Base(casePath)
			oraclePath := strings.TrimSuffix(casePath, filepath.Ext(casePath)) + ".result"
			input, err := os.ReadFile(casePath)
			require.NoError(t, err)
			oracle, err := os.ReadFile(oraclePath)
			require.NoError(t, err)
			require.Equal(t, expected[base].Input, digest(input))
			require.Equal(t, expected[base].Oracle, digest(oracle))
			executionPath := filepath.Join(staging, base)
			stagedOracle := strings.TrimSuffix(executionPath, filepath.Ext(executionPath)) + ".result"
			require.NoError(t, os.WriteFile(executionPath, input, 0600))
			require.NoError(t, os.WriteFile(stagedOracle, oracle, 0600))
			for repetition := range 3 {
				cwd := filepath.Join(evidence, "canonical", base, fmt.Sprintf("run%d", repetition+1))
				require.NoError(t, os.MkdirAll(filepath.Join(cwd, "log"), 0700))
				run := map[string]any{"case": casePath, "repetition": repetition + 1, "basename_derived_database": strings.TrimSuffix(base, filepath.Ext(base)), "input_sha256": digest(input), "oracle_sha256": digest(oracle), "started_at": time.Now().UTC(), "preconditions": absence(databases)}
				runs = append(runs, run)
				assertAbsent(run["preconditions"].(map[string]int))
				localConfig := regexp.MustCompile(`(?m)^  - addr:.*$`).ReplaceAllString(string(conf), fmt.Sprintf("  - addr: 127.0.0.1:%d", port))
				localConfig = regexp.MustCompile(`(?m)^    default:.*$`).ReplaceAllString(localConfig, "    default: "+entryDB)
				localConfig = regexp.MustCompile(`(?m)^  port:.*$`).ReplaceAllString(localConfig, fmt.Sprintf("  port: %d", port))
				require.NoError(t, os.WriteFile(filepath.Join(cwd, "mo.yml"), []byte(localConfig), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(cwd, "run.yml"), runConfig, 0600))
				require.NoError(t, os.WriteFile(filepath.Join(cwd, "log4j.properties"), logger, 0600))
				logPath := filepath.Join(cwd, "tester.private.log")
				log, err := os.Create(logPath)
				require.NoError(t, err)
				args := []string{"-Xms256M", "-Xmx1024M", "-cp", strings.Join(append([]string{cwd}, jars...), string(os.PathListSeparator)), "-Dconf.yml=" + filepath.Join(cwd, "mo.yml"), "-Drun.yml=" + filepath.Join(cwd, "run.yml"), "io.mo.Tester", "path=" + executionPath, "method=run", "rate=100", "ignore", "pprof"}
				testCtx, testCancel := context.WithTimeout(context.Background(), 240*time.Second)
				cmd := exec.CommandContext(testCtx, "/jdk/bin/java", args...)
				cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.WaitDelay = cwd, log, log, 5*time.Second
				run["command"], run["log"], run["cwd"] = append([]string{cmd.Path}, args...), logPath, cwd
				observerCtx, observerCancel := context.WithCancel(context.Background())
				observerDone := make(chan error, 1)
				go func() {
					ticker := time.NewTicker(100 * time.Millisecond)
					defer ticker.Stop()
					for {
						if e := observe(observerCtx); e != nil {
							if observerCtx.Err() != nil {
								observerDone <- nil
							} else {
								observerDone <- e
							}
							return
						}
						select {
						case <-observerCtx.Done():
							observerDone <- nil
							return
						case <-ticker.C:
						}
					}
				}()
				runErr := cmd.Run()
				observerCancel()
				observerErr := <-observerDone
				require.NoError(t, observerErr)
				observeMu.Lock()
				dbCopy, tableCopy, namesCopy := map[uint64]bool{}, map[uint64]bool{}, map[string]bool{}
				for k, v := range dbIDs {
					dbCopy[k] = v
				}
				for k, v := range tableIDs {
					tableCopy[k] = v
				}
				for k, v := range observedNames {
					namesCopy[k] = v
				}
				observed := observedNames[map[string]string{"binary_string_function_semantics.test": "binary_string_function_semantics"}[base]]
				observeMu.Unlock()
				run["observed_database_ids"], run["observed_table_ids"], run["observed_database_names"] = dbCopy, tableCopy, namesCopy
				require.True(t, observed, "actual canonical database ID must be observed")
				testCancel()
				require.NoError(t, log.Close())
				exitCode := -1
				if cmd.ProcessState != nil {
					exitCode = cmd.ProcessState.ExitCode()
				}
				run["exit_code"], run["command_error"], run["finished_at"] = exitCode, fmt.Sprint(runErr), time.Now().UTC()
				output, err := os.ReadFile(logPath)
				require.NoError(t, err)
				pattern := `(?m)^.*The script file\[[^\n]*` + regexp.QuoteMeta(base) + `\][^\n]*total:([0-9]+), success:([0-9]+), failed:([0-9]+), ignored:([0-9]+), abnoraml:([0-9]+)`
				counts := regexp.MustCompile(pattern).FindStringSubmatch(string(output))
				countMap := map[string]int{}
				if len(counts) == 6 {
					for i, key := range []string{"total", "success", "failed", "ignored", "abnormal"} {
						countMap[key], err = strconv.Atoi(counts[i+1])
						require.NoError(t, err)
					}
				}
				run["filename_bound_counts"] = countMap
				var reports []map[string]any
				require.NoError(t, filepath.WalkDir(cwd, func(p string, d fs.DirEntry, walkErr error) error {
					if walkErr != nil {
						return walkErr
					}
					if !d.IsDir() && (d.Name() == "report.txt" || d.Name() == "report.xml" || d.Name() == "success.txt" || d.Name() == "error.txt") {
						b, e := os.ReadFile(p)
						if e != nil {
							return e
						}
						reports = append(reports, map[string]any{"path": p, "bytes": len(b), "sha256": digest(b), "names_case": strings.Contains(string(b), base)})
					}
					return nil
				}))
				run["reports"], run["postconditions_before_recovery"] = reports, absence(databases)
				entryStatus := absence([]string{entryDB})
				run["stable_admission_database"] = entryStatus
				actualInput, err := os.ReadFile(executionPath)
				require.NoError(t, err)
				actualOracle, err := os.ReadFile(stagedOracle)
				require.NoError(t, err)
				originalInputAfter, err := os.ReadFile(casePath)
				require.NoError(t, err)
				originalOracleAfter, err := os.ReadFile(oraclePath)
				require.NoError(t, err)
				run["staged_input_sha256"], run["staged_oracle_sha256"] = digest(actualInput), digest(actualOracle)
				run["original_input_after_sha256"], run["original_oracle_after_sha256"] = digest(originalInputAfter), digest(originalOracleAfter)
				require.Equal(t, input, actualInput)
				require.Equal(t, oracle, actualOracle)
				require.Equal(t, input, originalInputAfter)
				require.Equal(t, oracle, originalOracleAfter)
				if runErr != nil || countMap["failed"] != 0 || countMap["abnormal"] != 0 {
					run["status"] = "FAIL"
					assertAbsent(run["postconditions_before_recovery"].(map[string]int))
					t.Errorf("canonical comparator failure in repetition %d; see retained report", repetition+1)
					continue
				}
				require.Len(t, counts, 6, "filename-bound canonical terminal missing")
				require.Equal(t, expected[base].Total, countMap["total"], "the complete frozen canonical file must execute")
				require.Equal(t, countMap["total"], countMap["success"])
				for _, key := range []string{"failed", "ignored", "abnormal"} {
					require.Zero(t, countMap[key], key)
				}
				foundTextReport := false
				for _, report := range reports {
					if filepath.Base(report["path"].(string)) == "report.txt" {
						foundTextReport = true
						require.Equal(t, true, report["names_case"])
					}
				}
				require.True(t, foundTextReport, "canonical normal report.txt is required")
				require.Equal(t, 1, entryStatus[entryDB+".databases"])
				require.Zero(t, entryStatus[entryDB+".tables"])
				assertAbsent(run["postconditions_before_recovery"].(map[string]int))
				run["status"] = "PASS"
				t.Logf("canonical case=%s round=%d total=%d success=%d teardown=PASS", base, repetition+1, countMap["total"], countMap["success"])
			}
		}
	})
}
