"""Read real journal records through an isolated helper; no network changes."""
import json,os,pathlib,subprocess,sys,tempfile,time,urllib.parse,uuid
from network_integration import UnixHTTP

def main():
    assert os.geteuid()==0
    binary=str(pathlib.Path(sys.argv[1]).resolve())
    marker='pilot-log-test-'+uuid.uuid4().hex
    # Bounded identifiable fixtures; use system journal without altering retention settings.
    subprocess.run(['systemd-cat','-t','pilot-log-test','-p','warning'],input=''.join(f'{marker} item={i:03d}\n' for i in range(125)),text=True,check=True)
    subprocess.run(['journalctl','--sync'],check=True)
    with tempfile.TemporaryDirectory(prefix='pilot-logs-') as temp:
        sock=temp+'/control.sock'
        log=open(temp+'/helper.log','w+')
        p=subprocess.Popen([binary,'-socket',sock,'-state',temp+'/state'],stdout=log,stderr=log)
        def api(params,expected=200):
            conn=UnixHTTP(sock);conn.request('GET','/logs?'+urllib.parse.urlencode(params));r=conn.getresponse();body=json.loads(r.read());conn.close();assert r.status==expected,(r.status,body);return body
        try:
            query={'source':'system','window':'1h','level':'warning','search':marker,'limit':30}
            for _ in range(100):
                try:first=api(query);break
                except (FileNotFoundError,ConnectionError):
                    assert p.poll() is None;time.sleep(.05)
            else:raise AssertionError('helper timeout')
            entries=[];page=first;seen=set();times=[]
            for _ in range(20):
                for e in page['entries']:
                    assert e['cursor'] not in seen;seen.add(e['cursor']);entries.append(e);times.append(e['time'])
                    assert marker in e['message'] and e['priority']==4
                if not page['hasMore']:break
                query['until']=page['until'];query['cursor']=page['nextCursor'];page=api(query)
            assert len(entries)==125,(len(entries),first)
            assert times==sorted(times,reverse=True)
            assert set(int(e['message'].split('item=')[1]) for e in entries)==set(range(125))
            print('PASS: real journal search, severity filter, reverse cursor pagination without gaps/duplicates')
            query.pop('cursor',None);query['level']='error';assert not api(query)['entries']
            query['source']='web';query['level']='all';assert not api(query)['entries']
            api({'source':'../../etc/shadow'},400);api({'limit':'100000'},400);api({'cursor':'--file=/etc/shadow'},400)
            print('PASS: source and error filters, invalid query limits and cursor rejection')
        finally:
            p.terminate();p.wait(timeout=10);log.close()

if __name__=='__main__':main()
