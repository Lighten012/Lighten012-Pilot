"""DHCP switch/rollback test using two private namespaces and dnsmasq-base.
Requires root, iproute2, ifupdown, dhcpcd and dnsmasq-base. No DHCP server is
started on a host interface. The test client's resolver is namespace-specific.
"""
import json, os, pathlib, signal, subprocess, sys, tempfile, time
from network_integration import UnixHTTP, run

def main():
    assert os.geteuid() == 0
    binary = str(pathlib.Path(sys.argv[1]).resolve())
    suffix = str(os.getpid()); ns = 'pilot-dhcp-' + suffix; gateway = 'pilot-gw-' + suffix
    wan = 'pw' + suffix; lan = 'pl' + suffix; peer = 'pg' + suffix
    resolver_dir = pathlib.Path('/etc/netns') / ns
    resolver_dir.mkdir(parents=True)
    (resolver_dir / 'resolv.conf').write_text('nameserver 127.0.0.1\n')
    host_resolver = pathlib.Path('/etc/resolv.conf').read_bytes()
    processes = []; namespaces = []
    with tempfile.TemporaryDirectory(prefix='pilot-dhcp-test-') as temp:
        root = pathlib.Path(temp); etc = root / 'network'; (etc / 'interfaces.d').mkdir(parents=True); (root / 'ifstate').mkdir()
        (etc / 'interfaces').write_text(f'source /etc/network/interfaces.d/*\nauto {wan}\niface {wan} inet static\n address 10.232.0.2/24\n gateway 10.232.0.1\n')
        sock = str(root / 'control.sock')
        def api(path, body=None):
            c = UnixHTTP(sock); c.request('GET' if body is None else 'POST', '/'+path, json.dumps(body) if body is not None else None, {'Content-Type':'application/json'})
            r=c.getresponse(); value=json.loads(r.read()); c.close(); assert r.status==200,(path,value); return value
        try:
            for name in [ns,gateway]:run('ip','netns','add',name);namespaces.append(name)
            run('ip','-n',ns,'link','add',wan,'type','veth','peer','name',peer)
            run('ip','-n',ns,'link','set',peer,'netns',gateway)
            run('ip','-n',gateway,'addr','add','10.232.0.1/24','dev',peer)
            run('ip','-n',gateway,'link','set',peer,'up')
            run('ip','-n',ns,'addr','add','10.232.0.2/24','dev',wan)
            run('ip','-n',ns,'link','set',wan,'up')
            run('ip','-n',ns,'link','add',lan,'type','dummy')
            server=subprocess.Popen(['ip','netns','exec',gateway,'dnsmasq','--no-daemon','--conf-file=/dev/null','--port=0','--interface='+peer,'--bind-interfaces','--dhcp-range=10.232.0.100,10.232.0.110,255.255.255.0,1h','--dhcp-option=3,10.232.0.1','--dhcp-option=6,10.232.0.1','--dhcp-leasefile='+str(root/'leases'),'--pid-file='+str(root/'dnsmasq.pid')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);processes.append(server)
            daemon=subprocess.Popen(['ip','netns','exec',ns,binary,'-socket',sock,'-state',str(root/'state'),'-network-root',str(etc),'-ifstate-dir',str(root/'ifstate'),'-confirm-timeout','30s'],stdout=subprocess.DEVNULL,stderr=subprocess.PIPE);processes.append(daemon)
            for _ in range(100):
                try:api('state');break
                except (ConnectionError,FileNotFoundError):time.sleep(.05)
            config={'wan':{'interface':wan,'mode':'dhcp','address':'','gateway':''},'lan':{'interface':lan,'mode':'static','address':'192.168.70.1/24','gateway':''}}
            plan=api('preview',config);tid=api('apply',{'config':config,'revision':plan['revision'],'token':plan['token']})['id']
            for _ in range(160):
                state=api('state');pending=state['pending']
                if pending and pending['phase']=='awaiting_confirmation':break
                if pending is None:raise AssertionError(state['event'])
                time.sleep(.2)
            else:raise AssertionError(state)
            devices=state['inventory']['devices'];found=next(d for d in devices if d['name']==wan)
            assert any(a.startswith('10.232.0.10') for a in found['addresses']),found
            assert any(r['dst']=='default' and r['gateway']=='10.232.0.1' for r in state['inventory']['routes'])
            api('rollback',{'id':tid})
            addresses=json.loads(run('ip','-n',ns,'-j','-4','addr','show','dev',wan))[0]['addr_info']
            assert [a['local'] for a in addresses]==['10.232.0.2'],addresses
            assert pathlib.Path('/etc/resolv.conf').read_bytes()==host_resolver,'Host resolver was changed'
            print('PASS: WAN static -> DHCP gets lease/default route; rollback restores static address; host resolver unchanged')
        finally:
            for name in namespaces:
                for pid in run('ip','netns','pids',name).split():
                    try:os.kill(int(pid),signal.SIGTERM)
                    except ProcessLookupError:pass
            for p in processes:
                try:p.wait(timeout=5)
                except subprocess.TimeoutExpired:p.kill();p.wait()
            for name in reversed(namespaces):run('ip','netns','delete',name)
            (resolver_dir/'resolv.conf').unlink();resolver_dir.rmdir()

if __name__=='__main__':main()
