"""Test-only raw DHCP/DNS client. Run only inside the integration namespace."""
import json, os, socket, struct, sys

def dhcp(interface, silent=False):
    mac=bytes.fromhex('020000300002')
    xid=int.from_bytes(os.urandom(4),'big')
    s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
    s.setsockopt(socket.SOL_SOCKET,socket.SO_BROADCAST,1)
    s.setsockopt(socket.SOL_SOCKET,socket.SO_BINDTODEVICE,interface.encode()+b'\0')
    s.bind(('',68));s.settimeout(6 if not silent else 2)
    # Before an address/route exists, Linux can drop broadcast UDP at the IP
    # layer. Real DHCP clients also receive these replies through packet sockets.
    incoming=socket.socket(socket.AF_PACKET,socket.SOCK_DGRAM,socket.htons(0x0800))
    incoming.bind((interface,0));incoming.settimeout(6 if not silent else 2)
    def packet(kind,requested=None,server=None):
        b=bytearray(240);struct.pack_into('!BBBBIHH',b,0,1,1,6,0,xid,0,0x8000)
        b[28:34]=mac;b[236:240]=b'\x63\x82\x53\x63'
        b+=bytes([53,1,kind,55,5,1,3,6,51,54,12,12])+b'pilot-client'
        if requested:b+=bytes([50,4])+socket.inet_aton(requested)
        if server:b+=bytes([54,4])+socket.inet_aton(server)
        return b+b'\xff'
    def receive(kind):
        while True:
            ip,_=incoming.recvfrom(4096)
            ihl=(ip[0]&15)*4
            if len(ip)<ihl+8 or ip[9]!=17 or struct.unpack_from('!H',ip,ihl+2)[0]!=68:continue
            b=ip[ihl+8:]
            if len(b)<240 or struct.unpack_from('!I',b,4)[0]!=xid:continue
            opts={};i=240
            while i<len(b):
                code=b[i];i+=1
                if code==255:break
                if code==0:continue
                n=b[i];i+=1;opts[code]=b[i:i+n];i+=n
            if opts.get(53)!=bytes([kind]):continue
            return socket.inet_ntoa(b[16:20]),opts
    s.sendto(packet(1),('255.255.255.255',67))
    if silent:
        try:receive(2)
        except socket.timeout:print('PASS: no DHCP response on WAN');return
        raise AssertionError('DHCP leaked onto WAN')
    address,opts=receive(2);server=socket.inet_ntoa(opts[54])
    s.sendto(packet(3,address,server),('255.255.255.255',67));address,opts=receive(5)
    assert socket.inet_ntoa(opts[3])=='10.233.0.1'
    assert socket.inet_ntoa(opts[6])=='10.233.0.1'
    assert struct.unpack('!I',opts[51])[0]==720*60
    print(json.dumps({'address':address,'gateway':server,'dns':socket.inet_ntoa(opts[6])}))

def dns_query(server,name,typ=1,tcp=False):
    ident=int.from_bytes(os.urandom(2),'big')
    q=struct.pack('!6H',ident,0x100,1,0,0,0)+b''.join(bytes([len(x)])+x.encode() for x in name.rstrip('.').split('.'))+b'\0'+struct.pack('!HH',typ,1)
    s=socket.socket(socket.AF_INET,socket.SOCK_STREAM if tcp else socket.SOCK_DGRAM);s.settimeout(3);s.connect((server,53))
    if tcp:
        s.sendall(struct.pack('!H',len(q))+q)
        def read(n):
            b=b''
            while len(b)<n:
                x=s.recv(n-len(b))
                if not x:raise EOFError()
                b+=x
            return b
        b=read(struct.unpack('!H',read(2))[0])
    else:s.send(q);b=s.recv(4096)
    s.close();header=struct.unpack_from('!6H',b)
    assert header[0]==ident and header[1]&0x8000
    return header,b

if __name__=='__main__':
    if sys.argv[1]=='dhcp':dhcp(sys.argv[2])
    elif sys.argv[1]=='silent':dhcp(sys.argv[2],True)
    elif sys.argv[1]=='dns':
        for tcp in (False,True):
            h,b=dns_query(sys.argv[2],'LiGhTeN012.home',tcp=tcp)
            assert h[1]&15==0 and h[3]==1 and socket.inet_aton('192.168.1.1') in b
        h,b=dns_query(sys.argv[2],'lighten012.home',28);assert h[1]&15==0 and h[3]==0
        print('PASS: real UDP/TCP local DNS, case-insensitive match, AAAA NODATA')
    elif sys.argv[1]=='blocked-dns':
        try:dns_query(sys.argv[2],'lighten012.home')
        except (socket.timeout,ConnectionError):print('PASS: DNS not reachable on WAN')
        else:raise AssertionError('DNS answered on WAN')
