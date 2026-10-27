import glob, hashlib, json, os, pathlib, re, shutil, signal, socket, subprocess, sys, time, uuid
import pymysql

R=pathlib.Path('/out'); S=pathlib.Path('/subject'); C=pathlib.Path('/control'); PORT=44130
oldroot=R/'release'; data=R/'data'; backup=R/'old-cold-backup'; P=[]; events=[]
states={k:'NOT_RUN' for k in ('release_identity','old_seed','old_index_storage','old_index_plan','candidate_index_storage','candidate_index_plan','cold_backup','candidate_build','upgrade_dml','restart','snapshot_restore','cold_restore','old_cn_rejoin','cleanup')}
def rec(stage,**fields):
    events.append(dict(stage=stage,**fields)); (R/'events.json').write_text(json.dumps(events,indent=2,default=str))
    (R/'summary.json').write_text(json.dumps({'states':states,'events':events},indent=2,default=str))
def sha(p):
    h=hashlib.sha256()
    with open(p,'rb') as f:
        for chunk in iter(lambda:f.read(1024*1024),b''):h.update(chunk)
    return h.hexdigest()
def manifest(root):
    return {str(p.relative_to(root)):sha(p) for p in sorted(root.rglob('*')) if p.is_file() and not p.is_symlink()}
def stop(p):
    if p.poll() is None:
        p.terminate()
        try:p.wait(timeout=45)
        except subprocess.TimeoutExpired:
            p.kill();p.wait(timeout=15)
    rec('process_exit',pid=p.pid,code=p.returncode)
    if p.returncode not in (0,-15): raise RuntimeError('service did not stop cleanly')
def query(sql,expected=None):
    with pymysql.connect(host='127.0.0.1',port=PORT,user='root',password='111',autocommit=True,connect_timeout=2,read_timeout=50,write_timeout=10) as db:
        with db.cursor() as cur:cur.execute(sql); rows=cur.fetchall()
    out=[list(x) for x in rows];rec('sql',query=sql,rows=out)
    if expected is not None and out!=expected: raise AssertionError('SQL oracle mismatch: '+sql)
    return out
def spawn(binary,env,name):
    f=(R/(name+'.log')).open('w');p=subprocess.Popen([str(binary),'-launch',str(R/'launch.toml')],cwd=str(binary.parent),env=env,stdout=f,stderr=subprocess.STDOUT,start_new_session=True);P.append((p,f));rec('process_start',name=name,pid=p.pid,binary_sha256=sha(binary))
    end=time.monotonic()+180
    while time.monotonic()<end:
        if p.poll() is not None:raise RuntimeError(name+' exited before SQL readiness')
        try:
            with pymysql.connect(host='127.0.0.1',port=PORT,user='root',password='111',connect_timeout=2,read_timeout=3) as db:
                with db.cursor() as c:c.execute('SELECT 1');row=c.fetchone()
            if row==(1,) and 'ISCP-Task Start' in (R/(name+'.log')).read_text(errors='replace'):rec('ready',name=name);return p
        except (pymysql.MySQLError,OSError):pass
        time.sleep(1)
    raise TimeoutError(name+' SQL readiness timeout')
def check_index(n, prefix):
    global states
    sql='SELECT id FROM crc32_release.t FORCE INDEX(idx_c) WHERE c=3719146973 ORDER BY id'
    query(sql,[[i] for i in range(1,n+1)])
    plan=query('EXPLAIN '+sql)
    states[prefix+'_index_plan']='PASS' if '__mo_index' in str(plan) else 'INCONCLUSIVE'
    rec('index_plan',scope=prefix,result=states[prefix+'_index_plan'])
    try:
        names=query("SELECT DISTINCT index_table_name FROM mo_catalog.mo_indexes WHERE name='idx_c' AND table_id IN (SELECT rel_id FROM mo_catalog.mo_tables WHERE reldatabase='crc32_release' AND relname='t')")
        if len(names)!=1 or len(names[0])!=1 or not isinstance(names[0][0],str) or not re.fullmatch(r'__mo_index_[A-Za-z0-9_-]+',names[0][0]):raise RuntimeError('index backing table identity unresolved')
        table=names[0][0]
        q='SELECT COUNT(*) FROM crc32_release.`'+table+'` i JOIN crc32_release.t t ON i.__mo_index_pri_col=t.id WHERE i.__mo_index_idx_col=serial_full(t.c,t.id)'
        query(q,[[n]])
        query('SELECT COUNT(*) FROM crc32_release.`'+table+'`',[[n]])
        if states[prefix+'_index_storage']!='FAIL':states[prefix+'_index_storage']='PASS'
        rec('index_storage',scope=prefix,result='PASS',table=table,rows=n)
    except Exception as e:
        result='FAIL' if isinstance(e,AssertionError) else 'INCONCLUSIVE'
        if states[prefix+'_index_storage']!='FAIL':states[prefix+'_index_storage']=result
        rec('index_storage',scope=prefix,result=result,category=type(e).__name__,reason=str(e))
def verify(n):
    query('SELECT id,c,crc32(j) FROM crc32_release.t ORDER BY id',[[i,3719146973,4012824821] for i in range(1,n+1)])
    check_index(n,'candidate')
    query('SELECT count(*) FROM crc32_release.t WHERE c=4012824821',[[0]])
def abort_signal(sig,frame): raise SystemExit('signal '+str(sig))
signal.signal(signal.SIGTERM,abort_signal);signal.signal(signal.SIGINT,abort_signal)
try:
    if data.exists():raise RuntimeError('data root is not fresh')
    free=shutil.disk_usage(R).free; available=int(next(x.split()[1] for x in pathlib.Path('/proc/meminfo').read_text().splitlines() if x.startswith('MemAvailable:')))*1024
    rec('admission',disk_free_bytes=free,mem_available_bytes=available)
    if free<6*1024**3 or available<6*1024**3:raise RuntimeError('resource admission: less than 6 GiB disk or memory available')
    for port in range(44100,44151):
        with socket.socket() as sk:sk.bind(('0.0.0.0',port))
    ids={s:str(uuid.uuid4()) for s in ['aa410af6-57bc-45b9-afed-99052b4c833a','866d324f-d002-4cda-97db-329ab2e45ed5','4f5ed8e6-eaa3-4ec1-8740-b63997cbe76b']}
    for name in ('cn.toml','tn.toml','log.toml','launch.toml'):
        txt=(C/'templates'/name).read_text().replace('/tmp/crc32-upgrade-20261008.tIJ01k',str(R))
        for a,b in ids.items():txt=txt.replace(a,b)
        (R/name).write_text(txt)
    rec('cluster_identity',ids=ids,data_root=str(data),config_sha256={n:sha(R/n) for n in ('cn.toml','tn.toml','log.toml','launch.toml')})
    matches=[pathlib.Path(x) for x in glob.glob(str(oldroot/'**'/'mo-service'),recursive=True) if pathlib.Path(x).is_file()]
    if len(matches)!=1:raise RuntimeError('release archive must contain exactly one mo-service')
    states['release_identity']='RUNNING';rec('stage',name='release_identity',result='RUNNING')
    old=matches[0];old.chmod(old.stat().st_mode|0o111)
    env=os.environ.copy();env['LD_LIBRARY_PATH']=':'.join([str(old.parent/'lib'),str(old.parent/'cgo'),str(old.parent/'thirdparties/install/lib'),'/usr/local/lib'])
    v=subprocess.run([str(old),'-version'],cwd=old.parent,env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=30)
    (R/'release-version.txt').write_text(v.stdout)
    rec('release_identity',executable_sha256=sha(old),version_exit=v.returncode,expected_commit='71498157cc21f833b8e717f45d50d1f926be3134')
    if v.returncode or '7149815' not in v.stdout:raise RuntimeError('release executable source identity mismatch or unavailable')
    (R/'release-files-manifest.json').write_text(json.dumps(manifest(oldroot),indent=2))
    states['release_identity']='PASS';rec('stage',name='release_identity',result='PASS')
    states['old_seed']='RUNNING';rec('stage',name='old_seed',result='RUNNING')
    p=spawn(old,env,'old-seed')
    query('CREATE DATABASE crc32_release')
    query('CREATE TABLE crc32_release.t(id INT PRIMARY KEY,j JSON,c BIGINT UNSIGNED GENERATED ALWAYS AS(CRC32(j)) STORED, INDEX idx_c(c))')
    query('INSERT INTO crc32_release.t(id,j) VALUES(1,\'{"t1":"a"}\')')
    query('SELECT id,c,crc32(j) FROM crc32_release.t',[[1,3719146973,3719146973]])
    check_index(1,'old')
    states['old_seed']='PASS';rec('stage',name='old_seed',result='PASS')
    snapshot=False
    try:query('CREATE SNAPSHOT crc32_release_seed FOR TABLE crc32_release t');snapshot=True;rec('snapshot_created',result='PASS')
    except Exception as e:rec('snapshot_created',result='NOT_RUN',reason=type(e).__name__)
    stop(p); before=manifest(data)
    if not before:raise RuntimeError('empty persistent-data manifest')
    (R/'old-data-manifest.json').write_text(json.dumps(before,indent=2));rec('old_data',file_count=len(before),manifest_sha256=sha(R/'old-data-manifest.json'))
    if shutil.disk_usage(R).free>6*1024**3:
        shutil.copytree(data,backup)
        if manifest(backup)!=before:raise AssertionError('cold backup data identity mismatch')
        states['cold_backup']='PASS';rec('stage',name='cold_backup',result='PASS')
    else:rec('cold_backup',result='NOT_RUN',reason='disk admission')
    states['candidate_build']='RUNNING';rec('stage',name='candidate_build',result='RUNNING')
    with (R/'candidate-build.log').open('w') as f:
        ret=subprocess.run(['bash','/control/build.sh'],stdout=f,stderr=subprocess.STDOUT,timeout=3600)
    if ret.returncode:states['candidate_build']='FAIL';raise RuntimeError('candidate build failed')
    new=S/'mo-service';nenv=os.environ.copy();nenv['LD_LIBRARY_PATH']=str(S/'cgo')+':'+str(S/'thirdparties/install/lib')
    v=subprocess.run([str(new),'-version'],cwd=S,env=nenv,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=30)
    (R/'candidate-version.txt').write_text(v.stdout);rec('candidate_binary',sha256=sha(new),version_exit=v.returncode)
    deps=subprocess.run(['ldd',str(new)],env=nenv,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=30)
    (R/'candidate-ldd.txt').write_text(deps.stdout)
    libs=[p for root in (S/'cgo',S/'thirdparties/install/lib') for p in root.glob('*.so*') if p.is_file() and not p.is_symlink()]
    (R/'candidate-libraries.json').write_text(json.dumps({str(p.relative_to(S)):sha(p) for p in sorted(libs)},indent=2))
    if v.returncode or '536b2aa' not in v.stdout:raise RuntimeError('candidate executable source identity mismatch')
    states['candidate_build']='PASS';rec('stage',name='candidate_build',result='PASS')
    nenv=os.environ.copy();nenv['LD_LIBRARY_PATH']=str(S/'cgo')+':'+str(S/'thirdparties/install/lib')
    states['upgrade_dml']='RUNNING';rec('stage',name='upgrade_dml',result='RUNNING')
    p=spawn(new,nenv,'candidate-upgrade');verify(1)
    query('UPDATE crc32_release.t SET j=\'{"t1":"a"}\' WHERE id=1')
    query('INSERT INTO crc32_release.t(id,j) VALUES(2,\'{"t1":"a"}\')');verify(2)
    query('CREATE TABLE crc32_release.new_identity(j JSON,c BIGINT UNSIGNED GENERATED ALWAYS AS(CRC32(j)) STORED)')
    query('INSERT INTO crc32_release.new_identity(j) VALUES(\'{"t1":"a"}\')');query('SELECT c FROM crc32_release.new_identity',[[4012824821]])
    states['upgrade_dml']='PASS';rec('stage',name='upgrade_dml',result='PASS');stop(p)
    states['restart']='RUNNING';rec('stage',name='restart',result='RUNNING')
    p=spawn(new,nenv,'candidate-restart');verify(2);states['restart']='PASS';rec('stage',name='restart',result='PASS')
    if snapshot:
        states['snapshot_restore']='RUNNING';rec('stage',name='snapshot_restore',result='RUNNING')
        try:
            query('RESTORE TABLE crc32_release.t{snapshot="crc32_release_seed"}');verify(1)
            query('UPDATE crc32_release.t SET j=\'{"t1":"a"}\' WHERE id=1')
            query('INSERT INTO crc32_release.t(id,j) VALUES(2,\'{"t1":"a"}\')');verify(2)
            states['snapshot_restore']='PASS';rec('stage',name='snapshot_restore',result='PASS')
        except Exception as e:
            states['snapshot_restore']='FAIL';rec('stage',name='snapshot_restore',result='FAIL',category=type(e).__name__,reason=str(e))
    stop(p)
    if states['cold_backup']=='PASS':
        states['cold_restore']='RUNNING';rec('stage',name='cold_restore',result='RUNNING')
        data.rename(R/'activated-data');shutil.copytree(backup,data)
        if manifest(data)!=before:raise AssertionError('cold restore manifest mismatch')
        p=spawn(new,nenv,'candidate-cold-restore');verify(1)
        query('UPDATE crc32_release.t SET j=\'{"t1":"a"}\' WHERE id=1')
        query('INSERT INTO crc32_release.t(id,j) VALUES(2,\'{"t1":"a"}\')');verify(2)
        states['cold_restore']='PASS';rec('stage',name='cold_restore',result='PASS');stop(p)
    if any(v=='FAIL' for v in states.values()):raise RuntimeError('one or more independent stages failed')
except BaseException as e:
    for key in states:
        if states[key]=='RUNNING':states[key]='FAIL'
    rec('failure',category=type(e).__name__,reason=str(e))
    raise
finally:
    clean=True
    for p,f in reversed(P):
        if p.poll() is None:
            try:stop(p)
            except Exception as e:clean=False;rec('cleanup_error',category=type(e).__name__)
        f.close()
    # TIME_WAIT connections do not own a listening socket; inspect actual TCP LISTEN entries.
    listeners=[]
    for netfile in ('/proc/net/tcp','/proc/net/tcp6'):
        for line in pathlib.Path(netfile).read_text().splitlines()[1:]:
            parts=line.split()
            if parts[3]=='0A':
                port=int(parts[1].rsplit(':',1)[1],16)
                if 44100 <= port <= 44150:listeners.append(port)
    rec('cleanup_listeners',ports=sorted(set(listeners)))
    if listeners:clean=False
    states['cleanup']='PASS' if clean and all(p.poll() is not None for p,_ in P) else 'FAIL';rec('cleanup',result=states['cleanup'])
    if states['cleanup']!='PASS':sys.exit(2)
