"""Real iptables/NAT in isolated router, LAN and WAN namespaces. Run as root."""
import copy,json,os,pathlib,subprocess,sys,tempfile,time
from network_integration import UnixHTTP,run

def main():
    assert os.geteuid()==0
    binary=str(pathlib.Path(sys.argv[1]).resolve())
    suffix=str(os.getpid());router='pfr'+suffix;lan='pfl'+suffix;wan='pfw'+suffix
    names=[];children=[];proc=None
    with tempfile.TemporaryDirectory(prefix='pilot-firewall-') as temp:
        root=pathlib.Path(temp);etc=root/'network';(etc/'interfaces.d').mkdir(parents=True);(root/'ifstate').mkdir();journal=root/'journal';journal.mkdir()
        network={'wan':{'interface':'wan0','mode':'static','address':'10.234.0.2/24','gateway':'10.234.0.1'},'lan':{'interface':'lan0','mode':'static','address':'10.233.0.1/24','gateway':''}}
        (journal/'network.json').write_text(json.dumps({'saved':network,'draft':network,'pending':None,'event':'test'}))
        (etc/'interfaces').write_text('source /etc/network/interfaces.d/*\nauto wan0\niface wan0 inet static\n address 10.234.0.2/24\n gateway 10.234.0.1\nauto lan0\niface lan0 inet static\n address 10.233.0.1/24\n')
        sock=str(root/'control.sock');log=open(root/'log','w+')
        def ns(n,*args):return run('ip','netns','exec',n,*args)
        def api(path,body=None,expected=200):
            c=UnixHTTP(sock);c.request('POST' if body is not None else 'GET','/'+path,json.dumps(body) if body is not None else None,{'Content-Type':'application/json'});r=c.getresponse();v=json.loads(r.read());c.close();assert r.status==expected,(path,r.status,v);return v
        def start():
            p=subprocess.Popen(['ip','netns','exec',router,binary,'-socket',sock,'-state',str(journal),'-network-root',str(etc),'-ifstate-dir',str(root/'ifstate'),'-confirm-timeout','8s'],stdout=log,stderr=log)
            for _ in range(150):
                if p.poll() is not None:log.seek(0);raise AssertionError(log.read())
                try:api('firewall/state');return p
                except (FileNotFoundError,ConnectionError):time.sleep(.05)
            raise AssertionError('startup timeout')
        def apply(c,confirm=True):
            p=api('firewall/preview',c);tx=api('firewall/apply',p)
            if confirm:api('firewall/confirm',tx)
            return tx
        def connect(n,ip,port=9000,blocked=False):
            code='import socket,sys\ns=socket.socket();s.settimeout(1)\ntry:\n s.connect((sys.argv[1],int(sys.argv[2])));print(s.recv(100).decode())\nexcept (TimeoutError,ConnectionError,OSError):\n print("BLOCKED")\n'
            result=ns(n,'python3','-c',code,ip,str(port)).strip()
            if blocked:assert result=='BLOCKED',result
            else:assert result!='BLOCKED',result
            return result
        try:
            for n in (router,lan,wan):run('ip','netns','add',n);names.append(n);ns(n,'ip','link','set','lo','up')
            for peer,dev,subnet in ((lan,'lan0','10.233.0'),(wan,'wan0','10.234.0')):
                ns(router,'ip','link','add',dev,'type','veth','peer','name','peer0');ns(router,'ip','link','set','peer0','netns',peer)
                ns(router,'ip','addr','add',subnet+('.1/24' if peer==lan else '.2/24'),'dev',dev);ns(router,'ip','link','set',dev,'up')
                ns(peer,'ip','addr','add',subnet+('.2/24' if peer==lan else '.1/24'),'dev','peer0');ns(peer,'ip','link','set','peer0','up')
            ns(lan,'ip','route','add','default','via','10.233.0.1')
            ns(router,'sysctl','-w','net.ipv4.ip_forward=0')
            ns(router,'iptables','-N','UNRELATED');ns(router,'iptables','-A','UNRELATED','-j','RETURN')
            before_input=ns(router,'iptables','-S','INPUT');before_output=ns(router,'iptables','-S','OUTPUT')
            server='import socket,threading,time\ndef serve(port):\n s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(("0.0.0.0",port));s.listen()\n while True:\n  c,a=s.accept();c.sendall(a[0].encode());c.close()\nfor p in (9000,9001):threading.Thread(target=serve,args=(p,),daemon=True).start()\ntime.sleep(120)'
            for n in (lan,wan):children.append(subprocess.Popen(['ip','netns','exec',n,'python3','-c',server],stdout=log,stderr=log))
            udp_server='import socket\ns=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(("0.0.0.0",9002))\nwhile True:\n b,a=s.recvfrom(1024);s.sendto(a[0].encode(),a)'
            children.append(subprocess.Popen(['ip','netns','exec',wan,'python3','-c',udp_server],stdout=log,stderr=log))
            time.sleep(.2);proc=start()
            c={'enabled':True,'nat':True,'defaultAction':'ACCEPT','wan':'','lan':'','subnet':'','rules':[]}
            apply(c)
            assert connect(lan,'10.234.0.1')=='10.234.0.2'
            print('PASS: LAN TCP reaches WAN; WAN observes router address (actual MASQUERADE)')
            ns(wan,'ip','route','add','10.233.0.0/24','via','10.234.0.2');connect(wan,'10.233.0.2',blocked=True)
            print('PASS: unsolicited WAN -> LAN blocked')
            c['rules']=[{'enabled':True,'action':'DROP','protocol':'tcp','source':'10.233.0.2','destination':'10.234.0.1','port':'9000'}];apply(c)
            connect(lan,'10.234.0.1',blocked=True);assert connect(lan,'10.234.0.1',9001)=='10.234.0.2'
            print('PASS: source/destination/TCP port block; other port still passes')
            c['rules'].insert(0,dict(c['rules'][0],action='ACCEPT'));apply(c);connect(lan,'10.234.0.1')
            c['rules'][0]['enabled']=False;apply(c);connect(lan,'10.234.0.1',blocked=True)
            print('PASS: first matching rule wins; disabled rules ignored')
            udp_client='import socket\ns=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(1);s.sendto(b"test",("10.234.0.1",9002))\ntry:print(s.recv(1024).decode())\nexcept TimeoutError:print("BLOCKED")'
            assert ns(lan,'python3','-c',udp_client).strip()=='10.234.0.2'
            c['rules']=[{'enabled':True,'action':'DROP','protocol':'udp','source':'','destination':'','port':'9002-9003'}];apply(c)
            assert ns(lan,'python3','-c',udp_client).strip()=='BLOCKED'
            print('PASS: actual UDP NAT and UDP port-range filtering')
            c['rules']=[];c['defaultAction']='DROP';apply(c);connect(lan,'10.234.0.1',9001,blocked=True)
            c['defaultAction']='ACCEPT';c['nat']=False;apply(c);assert connect(lan,'10.234.0.1')=='10.233.0.2'
            print('PASS: default deny and routing without NAT')
            c['nat']=True;apply(c)
            changed=copy.deepcopy(c);changed['defaultAction']='DROP';tx=apply(changed,False);api('firewall/rollback',tx);connect(lan,'10.234.0.1')
            tx=apply(changed,False);time.sleep(9);assert api('firewall/state')['pending'] is None;connect(lan,'10.234.0.1')
            print('PASS: manual and timeout rollback restore connectivity')
            apply(changed,False);proc.kill();proc.wait();proc=start();assert api('firewall/state')['pending'] is None;connect(lan,'10.234.0.1')
            proc.terminate();proc.wait();proc=start();connect(lan,'10.234.0.1')
            print('PASS: crash rolls back pending transaction; restart restores confirmed NAT')
            p=api('preview',network);api('apply',{'config':p['config'],'revision':p['revision'],'token':p['token']},409)
            stale=api('firewall/preview',c);stale['token']='stale';api('firewall/apply',stale,409)
            assert ns(router,'iptables','-S','INPUT')==before_input and ns(router,'iptables','-S','OUTPUT')==before_output
            assert '-A UNRELATED -j RETURN' in ns(router,'iptables','-S','UNRELATED')
            assert ns(router,'iptables','-S','FORWARD').count('-j PILOT_FWD')==1
            c['enabled']=False;c['nat']=False;apply(c)
            assert ns(router,'cat','/proc/sys/net/ipv4/ip_forward').strip()=='0'
            connect(lan,'10.234.0.1',blocked=True)
            print('PASS: protected network edit interlock, stale rejection, unrelated rules preserved, unique hooks, disable restores forwarding')
        except Exception:
            log.flush();log.seek(0);print(log.read());raise
        finally:
            if proc and proc.poll() is None:proc.terminate();proc.wait(timeout=10)
            for p in children:
                if p.poll() is None:p.terminate();p.wait(timeout=10)
            for n in reversed(names):run('ip','netns','delete',n)
            log.close()

if __name__=='__main__':main()
