package embed

import (
 "context"
 "crypto/sha256"
 "database/sql"
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

func TestPR31460LegacyGolden(t *testing.T) {
 out := os.Getenv("MO_MAINTENANCE_EVIDENCE")
 casePath := os.Getenv("CASE_PATH")
 require.Contains(t, []string{"test/distributed/cases/dml/show/show.test", "test/distributed/cases/zz_accesscontrol/inner_object.sql"}, casePath)
 require.NoError(t, os.MkdirAll(out, 0700))
 in, err := os.ReadFile(casePath); require.NoError(t, err)
 resultPath := strings.TrimSuffix(casePath, filepath.Ext(casePath)) + ".result"
 golden, err := os.ReadFile(resultPath); require.NoError(t, err)
 require.NoError(t, os.WriteFile(filepath.Join(out,"input-hashes.txt"), []byte(fmt.Sprintf("case=%s sha256=%x\nresult=%s sha256=%x\n",casePath,sha256.Sum256(in),resultPath,sha256.Sum256(golden))),0600))
 var port int
 t.Cleanup(func() {
  err := CloseSingleCNBaseClusterTests(); if err != nil { t.Errorf("cluster close: %v",err); return }
  c, err := net.DialTimeout("tcp",fmt.Sprintf("127.0.0.1:%d",port),300*time.Millisecond); if c != nil { c.Close() }; if port != 0 && err == nil { t.Error("SQL listener remained open") }
 })
 conf, err := os.ReadFile("/tester/mo.yml"); require.NoError(t, err)
 rc, err := os.ReadFile("/tester/run.yml"); require.NoError(t, err)
 logger, err := os.ReadFile("/tester/log4j.properties"); require.NoError(t, err)
 jars, err := filepath.Glob("/tester/lib/*.jar"); require.NoError(t,err); require.NotEmpty(t,jars)
 RunSingleCNBaseClusterTests(t,func(c Cluster) {
  cn, err := c.GetCNService(0); require.NoError(t,err)
  require.False(t,cn.GetServiceConfig().CN.Frontend.SkipCheckUser)
  port=int(cn.GetServiceConfig().CN.Frontend.Port)
  cfg:=mysql.NewConfig(); cfg.User="dump"; cfg.Passwd="111"; cfg.Net="tcp"; cfg.Addr=fmt.Sprintf("127.0.0.1:%d",port); cfg.Timeout=10*time.Second
  db, err:=sql.Open("mysql",cfg.FormatDSN()); require.NoError(t,err); defer db.Close()
  ctx,cancel:=context.WithTimeout(context.Background(),90*time.Second); defer cancel(); require.NoError(t,db.PingContext(ctx))
  for { b,err:=os.ReadFile(os.Getenv("MO_MAINTENANCE_SERVICE_LOG")); require.NoError(t,err); if strings.Contains(string(b),"ISCP-Task Start") { break }; select { case <-ctx.Done(): t.Fatal("ISCP readiness missing"); case <-time.After(250*time.Millisecond): } }
  wd:=filepath.Join(out,"tester"); require.NoError(t,os.MkdirAll(filepath.Join(wd,"log"),0700))
  config:=regexp.MustCompile(`(?m)^  - addr:.*$`).ReplaceAllString(string(conf),fmt.Sprintf("  - addr: 127.0.0.1:%d",port))
  config=regexp.MustCompile(`(?m)^    default:.*$`).ReplaceAllString(config,"    default: mo_catalog")
  config=regexp.MustCompile(`(?m)^  port:.*$`).ReplaceAllString(config,fmt.Sprintf("  port: %d",port))
  require.NoError(t,os.WriteFile(filepath.Join(wd,"mo.yml"),[]byte(config),0600)); require.NoError(t,os.WriteFile(filepath.Join(wd,"run.yml"),rc,0600)); require.NoError(t,os.WriteFile(filepath.Join(wd,"log4j.properties"),logger,0600))
  log,err:=os.Create(filepath.Join(wd,"tester.log")); require.NoError(t,err)
  abs,err:=filepath.Abs(casePath); require.NoError(t,err)
  args:=[]string{"-Xms256M","-Xmx1024M","-cp",strings.Join(append([]string{wd},jars...),string(os.PathListSeparator)),"-Dconf.yml="+filepath.Join(wd,"mo.yml"),"-Drun.yml="+filepath.Join(wd,"run.yml"),"io.mo.Tester","path="+abs,"method=run","rate=100","ignore","pprof"}
  runctx,stop:=context.WithTimeout(context.Background(),15*time.Minute); defer stop()
  cmd:=exec.CommandContext(runctx,"/jdk/bin/java",args...); cmd.Dir=wd; cmd.Stdout=log; cmd.Stderr=log; cmd.WaitDelay=5*time.Second
  runErr:=cmd.Run(); require.NoError(t,log.Close())
  data,err:=os.ReadFile(filepath.Join(wd,"tester.log")); require.NoError(t,err)
  pattern:=regexp.MustCompile(`(?m)^.*The script file\[[^\n]*`+regexp.QuoteMeta(filepath.Base(casePath))+`\][^\n]*total:([0-9]+), success:([0-9]+), failed:([0-9]+), ignored:([0-9]+), abnoraml:([0-9]+)`)
  m:=pattern.FindStringSubmatch(string(data)); require.Len(t,m,6,"selected case summary missing")
  counts:=make([]int,5); for i:=range counts { counts[i],err=strconv.Atoi(m[i+1]); require.NoError(t,err) }
  require.NoError(t,os.WriteFile(filepath.Join(out,"counts.txt"),[]byte(fmt.Sprintf("total=%d success=%d failed=%d ignored=%d abnormal=%d\n",counts[0],counts[1],counts[2],counts[3],counts[4])),0600))
  require.NoError(t,runErr); require.Positive(t,counts[0]); require.Equal(t,0,counts[2]); require.Equal(t,0,counts[4]); require.Equal(t,counts[0],counts[1]+counts[3])
  // Any existing skip is retained and explicitly reported; it is not a pass for skipped statements.
  after,err:=os.ReadFile(casePath); require.NoError(t,err); require.Equal(t,in,after)
  afterGolden,err:=os.ReadFile(resultPath); require.NoError(t,err); require.Equal(t,golden,afterGolden)
 })
}
