import os,subprocess,json,pathlib
root=pathlib.Path('/tmp/new-api-rc36-review')
engines=[('sqlite',None,None),('mysql','newapi-rc36-review-mysql',23306),('postgres','newapi-rc36-review-postgres',25432),('postgres96','newapi-rc36-review-postgres-min',25433)]
if os.environ.get('ONLY_ENGINE'):engines=[e for e in engines if e[0]==os.environ['ONLY_ENGINE']]
if os.environ.get('MYSQL57'):engines=[('mysql57','newapi-rc36-review-mysql-min',23307)]
for engine,container,port in engines:
 for baseline in ['fresh','rc30','upstream']:
  case=f'{engine}-{baseline}';env=os.environ.copy();env.pop('SQL_DSN',None);env.pop('LOG_SQL_DSN',None)
  for key in ['REDIS_CONN_STRING','CLICKHOUSE_DSN']:env.pop(key,None)
  if engine=='sqlite':
   env.update(SQL_DSN='local',LOG_SQL_DSN='local',TEST_SQLITE_MAIN=str(root/f'{case}-main.db'),TEST_SQLITE_LOG=str(root/f'{case}-log.db'))
  else:
   names=[f'rc36_v2_{baseline}_main',f'rc36_v2_{baseline}_log']
   for db in names:
    if engine.startswith('mysql'):
     subprocess.run(['docker','exec',container,'mysql','-uroot','-prc36-test-only','-e',f'CREATE DATABASE {db} CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci'],check=True,stdout=subprocess.DEVNULL)
    else:subprocess.run(['docker','exec',container,'createdb','-U','postgres',db],check=True)
   if engine.startswith('mysql'):dsns=[f'root:rc36-test-only@tcp(127.0.0.1:{port})/{db}?charset=utf8mb4&parseTime=true' for db in names]
   else:dsns=[f'postgres://postgres:rc36-test-only@127.0.0.1:{port}/{db}?sslmode=disable' for db in names]
   env.update(SQL_DSN=dsns[0],LOG_SQL_DSN=dsns[1])
  stages=[('custom' if baseline=='fresh' else baseline,'seed','seed'),('custom','verify','upgrade1'),('custom','verify','upgrade2')]
  for binary,mode,stage in stages:
   log=root/f'{case}-{stage}.log';snap=root/f'{case}-{stage}.json'
   with log.open('w') as out:
    result=subprocess.run([str(root/f'check-{binary}'),mode,str(snap)],env=env,stdout=out,stderr=subprocess.STDOUT)
   if result.returncode:print('FAIL',case,stage,log,flush=True);break
  else:
   a=json.loads((root/f'{case}-upgrade1.json').read_text());b=json.loads((root/f'{case}-upgrade2.json').read_text())
   if a!=b:print('FAIL schema changed on repeat',case,flush=True);continue
   before=json.loads((root/f'{case}-seed.json').read_text())
   # Every original index/constraint remains represented after upgrade. New upstream tables/columns are allowed.
   for db in ['main','log']:
    if engine=='sqlite':old=[x for x in before[db+'_schema'] if json.loads(x)[0]=='index']
    elif engine.startswith('mysql'):old=[x for x in before[db+'_schema'] if len(json.loads(x))==5 and isinstance(json.loads(x)[2],int)]
    else:old=[x for x in before[db+'_schema'] if len(json.loads(x))==3]
    missing=set(old)-set(b[db+'_schema'])
    if missing:raise RuntimeError(f'{case} missing indexes/constraints: {missing}')
   print('PASS',case,'data, unique constraints, indexes, separate log DB, repeat schema',b['main_version'],flush=True)
