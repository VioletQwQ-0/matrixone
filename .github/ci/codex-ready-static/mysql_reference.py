import json,os,pymysql
c=pymysql.connect(host='127.0.0.1',port=int(os.environ['QA_MYSQL_PORT']),user='root',password=os.environ['QA_MYSQL_PASSWORD'],autocommit=True)
try:
 with c.cursor() as q:
  q.execute('SELECT VERSION()');version=q.fetchone()[0]
  q.execute('CREATE DATABASE qa_issue_28162');q.execute('USE qa_issue_28162')
  q.execute('CREATE TABLE t(id BIGINT AUTO_INCREMENT PRIMARY KEY,k VARCHAR(32) UNIQUE,v INT)')
  q.execute("INSERT INTO t(k,v) VALUES ('x',10)");initial=q.lastrowid
  cases=[]
  for v in [10,11]:
   q.execute("INSERT INTO t(k,v) VALUES ('x',%s) ON DUPLICATE KEY UPDATE v=VALUES(v)",(v,));packet=q.lastrowid;affected=q.rowcount
   q.execute('SELECT LAST_INSERT_ID()');session=q.fetchone()[0]
   q.execute("SELECT id,v FROM t WHERE k='x'");row=q.fetchone()
   cases.append({'input_v':v,'packet_last_insert_id':packet,'affected_rows':affected,'session_last_insert_id':session,'row':row})
  print(json.dumps({'version':version,'initial_packet_last_insert_id':initial,'cases':cases}))
finally:c.close()
