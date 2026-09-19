"""Full restore and rollback in private network namespaces; no host interfaces changed."""
import copy,json,os,pathlib,subprocess,sys,tempfile,time
from network_integration import UnixHTTP,run

def main():
    assert os.geteuid()==0
    binary=str(pathlib.Path(sys.argv[1]).resolve())
    router='pbr'+str(os.getpid());peer='pbp'+str(os.getpid());names=[];proc=None;blocker=None
    with tempfile.TemporaryDirectory(prefix='pilot-backup-') as temp:
        root=pathlib.Path(temp);etc=root/'network';(etc/'interfaces.d').mkdir(parents=True);(root/'ifstate').mkdir();journal=root/'journal';journal.mkdir()
        network={'wan':{'interface':'wan0','mode':'static','address':'10.234.0.2/24','gateway':'10.234.0.1'},'lan':{'interface':'lan0','mode':'static','address':'10.233.0.1/24','gateway':''}}
        (journal/'network.json').write_text(json.dumps({'saved':network,'draft':network,'pending':None,'event':'fixture'}))
        (etc/'interfaces').write_text('source /etc/network/interfaces.d/*\nauto wan0\niface wan0 inet static\n address 10.234.0.2/24\n gateway 10.234.0.1\nauto lan0\niface lan0 inet static\n address 10.233.0.1/24\n')
        sock=str(root/'control.sock');log=open(root/'log','w+')
        def ns(n,*args):return run('ip','netns','exec',n,*args)
        def api(path,body=None,expected=200):
            c=UnixHTTP(sock);c.request('POST' if body is not None else 'GET','/'+path,json.dumps(body) if body is not None else None,{'Content-Type':'application/json'});r=c.getresponse();v=json.loads(r.read());c.close();assert r.status==expected,(path,r.status,v);return v
        def start():
            p=subprocess.Popen(['ip','netns','exec',router,binary,'-socket',sock,'-state',str(journal),'-network-root',str(etc),'-ifstate-dir',str(root/'ifstate'),'-confirm-timeout','5s','-protected','wan0'],stdout=log,stderr=log)
            for _ in range(200):
                if p.poll() is not None:log.seek(0);raise AssertionError(log.read())
                try:api('backup/state');return p
                except (FileNotFoundError,ConnectionError):time.sleep(.05)
            raise AssertionError('startup')
        def wait(phase):
            for _ in range(400):
                s=api('backup/state');p=s['pending']
                if (phase is None and p is None) or (p and p['phase']==phase):return s
                time.sleep(.05)
            raise AssertionError(s)
        def apply(b):return api('backup/apply',api('backup/preview',b))
        def addresses():return ns(router,'ip','-4','addr','show','dev','lan0')
        def same_backup(a,b):
            a=copy.deepcopy(a);b=copy.deepcopy(b);a.pop('createdAt');b.pop('createdAt');assert a==b,(a,b)
        try:
            for n in (router,peer):run('ip','netns','add',n);names.append(n);ns(n,'ip','link','set','lo','up')
            ns(router,'ip','link','add','wan0','type','dummy');ns(router,'ip','addr','add','10.234.0.2/24','dev','wan0');ns(router,'ip','link','set','wan0','up')
            ns(router,'ip','link','add','lan0','type','veth','peer','name','peer0');ns(router,'ip','link','set','peer0','netns',peer)
            ns(router,'ip','addr','add','10.233.0.1/24','dev','lan0');ns(router,'ip','link','set','lan0','up');ns(peer,'ip','addr','add','10.233.0.2/24','dev','peer0');ns(peer,'ip','link','set','peer0','up')
            ns(router,'sysctl','-w','net.ipv4.ip_forward=0');proc=start()
            before=api('backup/export');assert api('backup/preview',before)['changes']==[]
            bad=copy.deepcopy(before);bad['version']=99;api('backup/preview',bad,409)
            bad=copy.deepcopy(before);bad['network']['wan']['address']='10.234.0.3/24';api('backup/preview',bad,409)
            bad=copy.deepcopy(before);bad['path']='/etc/passwd';api('backup/preview',bad,409)
            bad=copy.deepcopy(before);bad['network']['lan']['interface']='other0';api('backup/preview',bad,409)
            target=copy.deepcopy(before);target['network']['lan']['address']='10.233.0.10/24'
            target['services']={'interface':'lan0','address':'10.233.0.10/24','dns':{'enabled':True,'upstream':'10.234.0.1','records':[{'name':'lighten012.home','type':'A','value':'192.168.1.1','ttl':60}]},'dhcp':{'enabled':True,'start':'10.233.0.100','end':'10.233.0.120','leaseMinutes':720}}
            target['firewall'].update(enabled=True,nat=True,forwards=[{'enabled':True,'protocol':'tcp','externalPort':18080,'target':'10.233.0.2','internalPort':80,'source':''}])
            p=api('backup/preview',target);target=p['backup'];assert len(p['changes'])==3
            stale=copy.deepcopy(p);stale['token']='stale';api('backup/apply',stale,409)
            tx=api('backup/apply',p);wait('awaiting_confirmation')
            assert '10.233.0.10/24' in addresses() and '10.233.0.1/24' not in addresses()
            s=api('services/state');assert s['dnsRunning'] and s['dhcpRunning'],s
            dnsclient=str(pathlib.Path(__file__).with_name('services_client.py').resolve());print(ns(peer,'python3',dnsclient,'dns','10.233.0.10').strip())
            assert '10.233.0.2:80' in ns(router,'iptables','-t','nat','-S','PILOT_DNAT')
            api('draft',network,409);api('backup/export',expected=409)
            api('backup/confirm',tx);same_backup(api('backup/export'),target)
            print('PASS: bundle/schema/protected interface checks; real LAN/DNS/DHCP/firewall restore and confirmation')
            tx=apply(before);wait('awaiting_confirmation');api('backup/rollback',tx);wait(None);same_backup(api('backup/export'),target);assert '10.233.0.10/24' in addresses()
            apply(before);wait('awaiting_confirmation');time.sleep(6);wait(None);same_backup(api('backup/export'),target)
            print('PASS: manual and timeout rollback restore all modules')
            apply(before);wait('awaiting_confirmation');proc.kill();proc.wait();proc=start();wait(None);same_backup(api('backup/export'),target)
            print('PASS: interrupted restore returns all modules to previous settings after daemon restart')
            tx=apply(before);wait('awaiting_confirmation');api('backup/confirm',tx);same_backup(api('backup/export'),before)
            ready=root/'occupied';blocker=subprocess.Popen(['ip','netns','exec',router,'python3','-c','import socket,time,pathlib,sys;s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(("0.0.0.0",53));pathlib.Path(sys.argv[1]).touch();time.sleep(30)',str(ready)],stdout=log,stderr=log)
            for _ in range(100):
                if ready.exists():break
                time.sleep(.02)
            assert ready.exists();apply(target);s=wait(None);assert '恢复失败' in s['event'],s;same_backup(api('backup/export'),before);assert '10.233.0.1/24' in addresses()
            print('PASS: DNS bind failure after LAN change restores original LAN and every saved module')
        except Exception:
            log.flush();log.seek(0);print(log.read());raise
        finally:
            for p in (blocker,proc):
                if p and p.poll() is None:p.terminate();p.wait(timeout=10)
            for n in reversed(names):
                for pid in run('ip','netns','pids',n).split():
                    try:os.kill(int(pid),9)
                    except ProcessLookupError:pass
                run('ip','netns','delete',n)
            log.close()

if __name__=='__main__':main()
