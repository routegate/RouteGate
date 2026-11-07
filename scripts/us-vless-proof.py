"""Incident-only, nonmutating US test-account proof. Never log credentials."""
import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import urllib.parse

COMMITMENT = '2b94ac17344ba19f87a2c9bb0806626fc4226d3a6b424f7e8c07bafed0433a12'
HOST = '139.60.162.138'

def remote():
    u = urllib.parse.urlsplit(os.environ['ROUTEGATE_DATABASE_URL'])
    env = dict(os.environ, PGHOST=u.hostname, PGPORT=str(u.port or 5432),
               PGUSER=urllib.parse.unquote(u.username),
               PGPASSWORD=urllib.parse.unquote(u.password or ''),
               PGDATABASE=urllib.parse.unquote(u.path[1:]), PGCONNECT_TIMEOUT='10',
               PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=10000')
    query = """SELECT json_build_object('id',a.id,'uuid',a.vless_uuid,
      'settings',cv.client_settings) FROM vpn_accounts a
      JOIN servers s ON s.id=a.server_id
      JOIN config_versions cv ON cv.id=s.active_config_version_id
      WHERE a.display_name='New test US' AND host(s.public_ip)='139.60.162.138'
      AND a.status='active'"""
    p = subprocess.run(['psql','-X','-qAt','-v','ON_ERROR_STOP=1','-c',query],
                       env=env,capture_output=True,text=True,timeout=15)
    if p.returncode or len(p.stdout.strip().splitlines()) != 1:
        raise RuntimeError('account_selection')
    row = json.loads(p.stdout)
    s = row['settings']
    profile = dict(uuid=row['uuid'],host=HOST,port=s['vlessPort'],
                   key=s['realityPublicKey'],sid=s['realityShortId'],
                   sni=s['realityServerName'],flow=s['vlessFlow'],fp='firefox')
    report = {'subscription_matches_applied_database':
              hashlib.sha256(json.dumps(profile,sort_keys=True).encode()).hexdigest()==COMMITMENT}
    config = json.loads(Path('/etc/sing-box/config.json').read_text())
    candidates = [i for i in config.get('inbounds',[]) if i.get('type')=='vless'
                  and i.get('listen_port')==profile['port']]
    report['runtime_listener_unique'] = len(candidates)==1
    if len(candidates)!=1:
        print(json.dumps({'report':report})); return
    inbound = candidates[0]
    users = [v for v in inbound.get('users',[]) if v.get('uuid')==profile['uuid']]
    reality = inbound.get('tls',{}).get('reality',{})
    raw_key = base64.urlsafe_b64decode(reality['private_key']+'===')
    if len(raw_key)!=32: raise RuntimeError('key_length')
    # X25519 PKCS#8 DER; only derived public key leaves openssl.
    der = bytes.fromhex('302e020100300506032b656e04220420')+raw_key
    pub = subprocess.run(['openssl','pkey','-inform','DER','-pubout','-outform','DER'],
                         input=der,capture_output=True,timeout=5)
    if pub.returncode: raise RuntimeError('public_key_derivation')
    key = base64.urlsafe_b64encode(pub.stdout[-32:]).decode().rstrip('=')
    report.update(runtime_uuid_present=len(users)==1,
                  runtime_flow_matches=len(users)==1 and users[0].get('flow','')==profile['flow'],
                  runtime_public_key_matches=key==profile['key'],
                  runtime_short_id_matches=profile['sid'] in reality.get('short_id',[]),
                  runtime_sni_matches=inbound.get('tls',{}).get('server_name')==profile['sni'],
                  runtime_reality_enabled=reality.get('enabled') is True,
                  applied_account_present=row['id'] in s.get('accounts',{}))
    tls = {'enabled':True,'server_name':profile['sni'],
           'utls':{'enabled':True,'fingerprint':profile['fp']},
           'reality':{'enabled':True,'public_key':profile['key'],'short_id':profile['sid']}}
    client = {'log':{'disabled':True},
              'inbounds':[{'type':'socks','listen':'127.0.0.1','listen_port':19081}],
              'outbounds':[{'type':'vless','server':HOST,'server_port':profile['port'],
                            'uuid':profile['uuid'],'flow':profile['flow'],'tls':tls}]}
    # SSH caller redirects this private payload to a mode-0600 temporary file.
    print(json.dumps({'report':report,'client':client if all(report.values()) else None}))

def runner(payload, binary):
    data = json.loads(Path(payload).read_text())
    Path(payload).unlink()
    print(json.dumps({'parity':data['report']},sort_keys=True),flush=True)
    for port in (22,443,8443):
        try:
            with socket.create_connection((HOST,port),timeout=8): pass
            state='connected'
        except (OSError,TimeoutError): state='failed'
        print(json.dumps({'tcp_port':port,'result':state}),flush=True)
    if not data.get('client'): return
    with tempfile.TemporaryDirectory(prefix='rg-private-client-') as directory:
        path = Path(directory)/'config.json'
        path.write_text(json.dumps(data['client'])); path.chmod(0o600)
        check=subprocess.run([binary,'check','-c',str(path)],capture_output=True,timeout=10)
        print(json.dumps({'client_config_valid':check.returncode==0}),flush=True)
        if check.returncode: return
        process=subprocess.Popen([binary,'run','-c',str(path)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        try:
            ready=False
            for _ in range(30):
                if process.poll() is not None: break
                try:
                    with socket.create_connection(('127.0.0.1',19081),timeout=.2): ready=True
                    break
                except OSError: time.sleep(.1)
            print(json.dumps({'client_started':ready}),flush=True)
            if not ready: return
            for label,url in [('example','https://example.com/'),('youtube','https://www.youtube.com/generate_204')]:
                args=['curl','--silent','--output','/dev/null','--write-out','%{http_code}',
                      '--connect-timeout','10','--max-time','25','--noproxy','',
                      '--proxy','socks5h://127.0.0.1:19081',url]
                r=subprocess.run(args,capture_output=True,text=True,timeout=30)
                code=r.stdout.strip()
                print(json.dumps({'via_us':label,'curl_exit':r.returncode,
                                  'http_status':code if code.isdigit() else 'unknown'}),flush=True)
        finally:
            process.terminate()
            try: process.wait(timeout=5)
            except subprocess.TimeoutExpired: process.kill(); process.wait()

if __name__=='__main__':
    try:
        if sys.argv[1:] == ['remote']: remote()
        elif len(sys.argv)==4 and sys.argv[1]=='runner': runner(sys.argv[2],sys.argv[3])
        else: raise RuntimeError('invalid_mode')
    except Exception as exc:
        # No exception text, database output, config or URL may reach Actions logs.
        print(json.dumps({'proof_error_type':type(exc).__name__}),file=sys.stderr)
        sys.exit(1)
