"""Root-only DHCP/DNS integration, with isolated router, LAN and WAN namespaces.
Run: python3 tests/services_integration.py ./bin/pilot-netd
No host network configuration is changed. No external DNS traffic is generated.
"""
import copy,json,os,pathlib,subprocess,sys,tempfile,time
from network_integration import UnixHTTP,run

def main():
    assert os.geteuid()==0
    binary=str(pathlib.Path(sys.argv[1]).resolve());client=str(pathlib.Path(__file__).with_name('services_client.py').resolve())
    suffix=str(os.getpid());router='psr'+suffix;lan='psl'+suffix;wan='psw'+suffix;names=[];proc=None;blocker=None
    with tempfile.TemporaryDirectory(prefix='pilot-services-') as temp:
        root=pathlib.Path(temp);etc=root/'network';(etc/'interfaces.d').mkdir(parents=True);(root/'ifstate').mkdir();journal=root/'journal';journal.mkdir()
        config={'wan':{'interface':'wan0','mode':'static','address':'10.234.0.2/24','gateway':'10.234.0.1'},'lan':{'interface':'lan0','mode':'static','address':'10.233.0.1/24','gateway':''}}
        (journal/'network.json').write_text(json.dumps({'saved':config,'draft':config,'pending':None,'event':'test fixture'}))
        (etc/'interfaces').write_text('source /etc/network/interfaces.d/*\nauto wan0\niface wan0 inet static\n    address 10.234.0.2/24\n    gateway 10.234.0.1\nauto lan0\niface lan0 inet static\n    address 10.233.0.1/24\n')
        sock=str(root/'control.sock')
        def ip(ns,*args):return run('ip','netns','exec',ns,'ip',*args)
        def api(path,body=None,expected=200):
            c=UnixHTTP(sock);c.request('POST' if body is not None else 'GET','/'+path,json.dumps(body) if body is not None else None,{'Content-Type':'application/json'});r=c.getresponse();v=json.loads(r.read());c.close();assert r.status==expected,(path,r.status,v);return v
        log=open(root/'helper.log','w+')
        def start():
            p=subprocess.Popen(['ip','netns','exec',router,binary,'-socket',sock,'-state',str(journal),'-network-root',str(etc),'-ifstate-dir',str(root/'ifstate')],stdout=log,stderr=log)
            for _ in range(100):
                if p.poll() is not None:log.seek(0);raise AssertionError(log.read())
                try:api('services/state');return p
                except (FileNotFoundError,ConnectionError):time.sleep(.05)
            raise AssertionError('helper timeout')
        def apply(c):
            p=api('services/preview',c);api('services/apply',{'config':p['config'],'revision':p['revision'],'token':p['token']})
        try:
            for ns in (router,lan,wan):run('ip','netns','add',ns);names.append(ns);ip(ns,'link','set','lo','up')
            for peer,dev,subnet in [(lan,'lan0','10.233.0'),(wan,'wan0','10.234.0')]:
                ip(router,'link','add',dev,'type','veth','peer','name','peer0');ip(router,'link','set','peer0','netns',peer)
                ip(router,'addr','add',subnet+('.1/24' if dev=='lan0' else '.2/24'),'dev',dev);ip(router,'link','set',dev,'up');ip(peer,'link','set','peer0','up')
            ip(lan,'link','set','peer0','address','02:00:00:30:00:02');ip(wan,'addr','add','10.234.0.1/24','dev','peer0')
            proc=start()
            c={'interface':'lan0','address':'10.233.0.1/24','dns':{'enabled':True,'upstream':'10.234.0.1','records':[{'name':'lighten012.home','type':'A','value':'192.168.1.1','ttl':60}]},'dhcp':{'enabled':True,'start':'10.233.0.100','end':'10.233.0.120','leaseMinutes':720}}
            bad=copy.deepcopy(c);bad['interface']='wan0';api('services/preview',bad,409)
            apply(c)
            state=api('services/state');assert state['dnsRunning'] and state['dhcpRunning'],state
            result=json.loads(run('ip','netns','exec',lan,'python3',client,'dhcp','peer0'));address=result['address'];assert '10.233.0.' in address
            ip(lan,'addr','add',address+'/24','dev','peer0')
            print('PASS: DHCP DISCOVER/OFFER/REQUEST/ACK, correct pool, gateway, DNS and lease duration')
            print(run('ip','netns','exec',lan,'python3',client,'dns','10.233.0.1').strip())
            print(run('ip','netns','exec',wan,'python3',client,'silent','peer0').strip())
            print(run('ip','netns','exec',wan,'python3',client,'blocked-dns','10.234.0.2').strip())
            # Even targeting the LAN address from WAN must not bypass device binding.
            ip(wan,'route','add','10.233.0.0/24','via','10.234.0.2')
            print(run('ip','netns','exec',wan,'python3',client,'blocked-dns','10.233.0.1').strip())
            p=api('preview',config);api('apply',{'config':config,'revision':p['revision'],'token':p['token']},409)
            assert any(l['address']==address for l in api('services/state')['leases'])
            proc.kill();proc.wait();time.sleep(.3);proc=start()
            state=api('services/state');assert state['dnsRunning'] and state['dhcpRunning'],state
            assert any(l['address']==address for l in state['leases'])
            print(run('ip','netns','exec',lan,'python3',client,'dns','10.233.0.1').strip())
            print('PASS: crash/restart restores DNS, DHCP and persisted leases; network edits interlocked')
            c['dns']['enabled']=False;c['dhcp']['enabled']=False;apply(c)
            s=api('services/state');assert not s['dnsRunning'] and not s['dhcpRunning']
            print(run('ip','netns','exec',lan,'python3',client,'silent','peer0').strip())
            print('PASS: service stop removes DHCP listener')
            # An occupied DNS port must not commit the new enabled config.
            ready=root/'occupied'
            blocker=subprocess.Popen(['ip','netns','exec',router,'python3','-c',
                'import socket,time,pathlib,sys;s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(("0.0.0.0",53));pathlib.Path(sys.argv[1]).touch();time.sleep(30)',str(ready)],stdout=log,stderr=log)
            for _ in range(100):
                if ready.exists():break
                assert blocker.poll() is None
                time.sleep(.02)
            assert ready.exists()
            before=api('services/state')['config']
            c['dns']['enabled']=True;c['dhcp']['enabled']=True
            p=api('services/preview',c)
            api('services/apply',{'config':p['config'],'revision':p['revision'],'token':p['token']},409)
            s=api('services/state');assert s['config']==before and not s['dnsRunning'] and not s['dhcpRunning'],s
            blocker.terminate();blocker.wait();blocker=None
            apply(c);s=api('services/state');assert s['dnsRunning'] and s['dhcpRunning']
            print('PASS: occupied DNS port rejects apply without saving enabled config; retry succeeds after port is released')
        except Exception:
            log.flush();log.seek(0);print(log.read())
            raise
        finally:
            if blocker and blocker.poll() is None:blocker.terminate();blocker.wait(timeout=10)
            if proc and proc.poll() is None:proc.terminate();proc.wait(timeout=10)
            for ns in reversed(names):
                for pid in run('ip','netns','pids',ns).split():
                    try:os.kill(int(pid),9)
                    except ProcessLookupError:pass
                run('ip','netns','delete',ns)
            log.close()

if __name__=='__main__':main()
