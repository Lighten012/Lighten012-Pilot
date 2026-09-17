"""Root-only Linux integration test. Uses a private namespace and temporary files.

Run: python3 tests/network_integration.py ./bin/pilot-netd
No host interface, default route, or /etc/network configuration is changed.
"""
import http.client
import json
import os
import pathlib
import socket
import subprocess
import sys
import tempfile
import time

class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__('localhost', timeout=30)
        self.path = path
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)

def run(*args):
    return subprocess.check_output(args, text=True, stderr=subprocess.STDOUT)

def main():
    if os.geteuid() != 0:
        raise SystemExit('This isolated network integration test requires root.')
    binary = str(pathlib.Path(sys.argv[1]).resolve())
    ns = 'pilot-test-' + str(os.getpid())
    proc = None
    with tempfile.TemporaryDirectory(prefix='pilot-net-test-') as temp:
        root = pathlib.Path(temp)
        etc = root / 'network'
        (etc / 'interfaces.d').mkdir(parents=True)
        (root / 'ifstate').mkdir()
        (etc / 'interfaces').write_text('source /etc/network/interfaces.d/*\nauto wan0\niface wan0 inet static\n    address 10.231.0.2/24\n    gateway 10.231.0.1\n')
        sock = str(root / 'control.sock')
        def api(path, body=None, expected=200):
            client = UnixHTTP(sock)
            client.request('POST' if body is not None else 'GET', '/' + path,
                           json.dumps(body) if body is not None else None,
                           {'Content-Type': 'application/json'})
            response = client.getresponse()
            value = json.loads(response.read())
            client.close()
            assert response.status == expected, (path, response.status, value)
            return value
        def start():
            p = subprocess.Popen(['ip', 'netns', 'exec', ns, binary,
                '-socket', sock, '-state', str(root / 'journal'),
                '-network-root', str(etc), '-ifstate-dir', str(root / 'ifstate'),
                '-confirm-timeout', '5s'], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            for _ in range(100):
                if p.poll() is not None:
                    raise AssertionError(p.stderr.read().decode())
                try:
                    api('state')
                    return p
                except (ConnectionError, FileNotFoundError):
                    time.sleep(.05)
            raise AssertionError('helper did not start')
        def await_state(phase):
            for _ in range(200):
                state = api('state')
                pending = state['pending']
                if (phase is None and pending is None) or (pending and pending['phase'] == phase):
                    return state
                time.sleep(.05)
            raise AssertionError(('phase not reached', phase, state))
        def addresses(name):
            data = json.loads(run('ip', 'netns', 'exec', ns, 'ip', '-j', '-4', 'addr', 'show', 'dev', name))
            return [a['local'] for d in data for a in d.get('addr_info', [])]
        def apply(config):
            plan = api('preview', config)
            return api('apply', {'config': config, 'revision': plan['revision'], 'token': plan['token']})['id']
        run('ip', 'netns', 'add', ns)
        try:
            for name in ['wan0', 'lan0']:
                run('ip', 'netns', 'exec', ns, 'ip', 'link', 'add', name, 'type', 'dummy')
            run('ip', 'netns', 'exec', ns, 'ip', 'addr', 'add', '10.231.0.2/24', 'dev', 'wan0')
            run('ip', 'netns', 'exec', ns, 'ip', 'link', 'set', 'wan0', 'up')
            proc = start()
            config = {'wan': {'interface': 'wan0', 'mode': 'static', 'address': '10.231.0.2/24', 'gateway': '10.231.0.1'},
                      'lan': {'interface': 'lan0', 'mode': 'static', 'address': '192.168.60.1/24', 'gateway': ''}}
            bad = json.loads(json.dumps(config)); bad['lan']['address'] = '10.231.0.5/24'
            api('preview', bad, 409)
            api('draft', config)
            assert addresses('lan0') == [], 'saving draft modified the interface'
            first = apply(config)
            await_state('awaiting_confirmation')
            assert addresses('lan0') == ['192.168.60.1']
            api('confirm', {'id': 'stale-id'}, 409)
            await_state(None)
            assert addresses('lan0') == []
            assert not (etc / 'interfaces.d' / 'pilot-lan0').exists()
            print('PASS: real apply, timeout rollback and removal of new configuration file')
            second = apply(config); await_state('awaiting_confirmation')
            api('confirm', {'id': second})
            assert api('state')['saved'] == config
            changed = json.loads(json.dumps(config)); changed['lan']['address'] = '192.168.61.1/24'
            third = apply(changed); await_state('awaiting_confirmation')
            assert addresses('lan0') == ['192.168.61.1']
            api('rollback', {'id': third})
            assert addresses('lan0') == ['192.168.60.1']
            print('PASS: confirmation and manual rollback preserve confirmed settings')
            fourth = apply(changed); await_state('awaiting_confirmation')
            proc.kill(); proc.wait(); proc = start()
            assert api('state')['pending'] is None
            assert addresses('lan0') == ['192.168.60.1']
            assert '192.168.60.1/24' in (etc / 'interfaces.d' / 'pilot-lan0').read_text()
            print('PASS: daemon crash/restart recovers unconfirmed on-disk and runtime settings')
            static = json.loads(json.dumps(config)); static['wan']['address'] = '10.231.0.3/24'
            fifth = apply(static); await_state('awaiting_confirmation')
            assert addresses('wan0') == ['10.231.0.3']
            routes = json.loads(run('ip', 'netns', 'exec', ns, 'ip', '-j', 'route'))
            assert any(r.get('dst') == 'default' and r.get('gateway') == '10.231.0.1' for r in routes)
            api('rollback', {'id': fifth})
            assert addresses('wan0') == ['10.231.0.2']
            print('PASS: WAN static address/default gateway and rollback')
        finally:
            if proc and proc.poll() is None:
                proc.terminate(); proc.wait(timeout=10)
            run('ip', 'netns', 'delete', ns)

if __name__ == '__main__':
    main()
