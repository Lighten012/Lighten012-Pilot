# Lighten012-Pilot

Go + Vue 3 + TypeScript + Vite 构建的个人软路由管理项目。v0.4 提供系统监控、WAN/LAN IPv4 配置、DHCP、自定义 Go DNS，以及 iptables 转发防火墙与 NAT。

## 默认部署方式

- 页面：http://127.0.0.1:8080
- 运行环境：Debian 13 / x86_64
- 建议部署目录：`/opt/lighten012-pilot`
- systemd 服务：`lighten012-pilot`
- 服务使用 systemd DynamicUser 运行，无 root 权限。
- 单人内网项目，打开页面即可管理，无管理员密钥或登录流程。

## 已实现

- CPU 平均使用率：每两秒计算 `/proc/stat` 差值，排除 guest 的重复计数；idle 和 iowait 视为非忙碌时间。
- 内存：`MemTotal - MemAvailable`，包含对可回收缓存的考虑，与简单 free 数值不同。
- 系统运行时间、1/5/15 分钟负载、主机名、系统架构。
- 非回环网卡名称、IPv4/IPv6 地址、链路状态和收发速率。
- 内存中的最近 120 个采样点，约四分钟历史；服务重启后重新积累，不伪造历史。
- 总览、系统监控、功能规划；刷新、暂停与恢复、时间范围选择、断连提示。
- WAN/LAN 页面：选择接口，WAN DHCP/静态 IPv4，LAN 静态 IPv4。
- 地址与网段校验、变更预览、保存草稿、临时应用、90 秒确认、手动及超时回滚。
- 网络助手持久化回滚记录；助手重启后先恢复未确认的变更。可保护当前管理接口。
- DHCP：LAN IPv4 地址池、租期、网关与 Pilot DNS 下发、有效租约列表。
- DNS：精确 A/AAAA 本地记录、上游转发、UDP/TCP 53、查询与命中计数。

接口速率单位为字节/秒，不是 bit/s，也不是宽带测速；所有非回环接口合计在桥接或多接口转发时可能重复计数。未自动判定 WAN/LAN。首页拓扑明确为规划示意。

## 目录

```text
cmd/pilot/          HTTP 服务入口与接口测试
cmd/pilot-netd/     特权网络助手，仅监听 Unix socket
internal/control/  请求来源校验、网络 API 代理
internal/network/  ifupdown 配置与持久化变更事务
internal/services/ Go DNS 解析逻辑、DHCP 进程及配置生命周期
internal/firewall/ iptables 规则生成、NAT 与持久化回滚事务
internal/monitor/   Linux 采样与计算测试
web/src/            Vue 页面、API 类型与功能目录
deploy/             systemd 服务文件
tests/              Linux 隔离网络集成测试
```

## 开发与构建

需要 Go 1.23+，Node.js 20.19+ 或 22.12+。监控后端需运行在 Linux，Windows/macOS 不具备所需的 `/proc` 数据源。

```sh
git clone https://github.com/Lighten012/Lighten012-Pilot.git
cd Lighten012-Pilot
```

```sh
# 前端
cd web
npm ci
npm run dev
# 开发服务器将 /api 代理到 127.0.0.1:8080

# 另一个终端，在项目根目录
go run ./cmd/pilot
```

构建与测试：

```sh
cd web
npm run build
cd ..
go test ./...
go vet ./...
go build -trimpath -o bin/pilot ./cmd/pilot
go build -trimpath -o bin/pilot-netd ./cmd/pilot-netd
./bin/pilot -listen 127.0.0.1:8080 -web web/dist
```

若 Windows 沙箱中默认 Vite 配置加载器遇到父目录访问限制，可使用 `npm run build -- --configLoader runner`。

Go 通过 HTTP 提供构建后的静态网页与 API，生产运行不需要 Node.js。DNS 使用 `github.com/miekg/dns` 处理协议报文；本地记录规则、转发策略、接口限制与配置管理由本项目实现。

## 服务管理

将项目及构建产物放到 `/opt/lighten012-pilot` 后安装服务。默认只监听本机 `127.0.0.1:8080`；需要局域网访问时，通过 systemd override 设置 `PILOT_LISTEN=<服务器局域网IP>:8080`。具体部署地址不写入仓库。

```sh
sudo groupadd --system pilot-net # 已存在时跳过
sudo install -m 644 deploy/pilot-netd.service /etc/systemd/system/
sudo install -m 644 deploy/lighten012-pilot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now pilot-netd lighten012-pilot
```

部署需要 `ifupdown`、`iproute2` 和可用的 DHCP 客户端。仅支持 `/etc/network/interfaces` 使用 `source /etc/network/interfaces.d/*` 的标准布局，以及简单的 DHCP/静态 IPv4 stanza；复杂钩子、桥接及其他网络管理器不在当前支持范围内。

远程开发时先保护当前 SSH/Web 所用网卡：运行 `sudo systemctl edit pilot-netd`，填写以下内容（将 `enp0s3` 替换为实际接口），再重启助手。保护接口允许保留原配置，不允许修改地址或 DHCP 模式。

```ini
[Service]
Environment=PILOT_PROTECTED_INTERFACES=enp0s3
```

页面直接提供网络管理。网络助手使用 root 权限，Unix socket 仅允许 `pilot-net` 组访问，Web 服务本身不具有 root 权限。先预览，再临时应用，连通性正常时在 90 秒内确认；未确认会恢复应用前配置。草稿不会改动系统。

```sh
systemctl status lighten012-pilot
systemctl restart lighten012-pilot
journalctl -u lighten012-pilot -n 50 --no-pager
```

只读 API：`GET /api/health`、`GET /api/monitor`。首次两秒采样完成前，监控返回 503；采集出错同样返回 503，前端保留旧数据并显示异常。网络状态及修改接口位于 `/api/network/`，无需登录。修改请求要求同源 Origin 和 `X-Pilot-Request: 1`。

## 后续功能

DHCP/DNS、防火墙与 NAT 默认关闭，需在对应页面启用。启用 DHCP/DNS 和出口 NAT 后，LAN 客户端可以通过 Pilot 获取地址、解析域名和共享 WAN 出口。端口转发、日志和备份恢复仍处于规划状态。

本版不管理 IPv6、PPPoE、VLAN、网桥或多 WAN，也不支持迁移已确认的 WAN/LAN 接口角色；确认后可以继续修改原接口的地址设置。正式使用前仍需补充长期运行、完整主机重启和实际下游设备测试。

## DHCP 与 DNS 使用

安装 DHCP 二进制 `sudo apt install dnsmasq-base`。不要同时启用系统级 dnsmasq 服务。Pilot 使用独立配置启动 `/usr/sbin/dnsmasq`，`port=0` 禁用其 DNS 功能，只在选定 LAN 接口提供 DHCP；租约写入 `/var/lib/pilot-netd/services/dhcp.leases`。

1. 先在 WAN/LAN 页面确认 LAN 网卡和静态地址。
2. 打开 DHCP 与 DNS。
3. 开启 DNS，设置上游 IPv4 地址，默认 `119.29.29.29`、端口 53。
4. 添加记录，例如域名 `lighten012.home`、类型 `A`、地址 `192.168.1.1`、TTL `60` 秒。
5. 按需开启 DHCP，设置同 LAN 网段且不包含路由器地址的地址池和租期。
6. 点击“保存并应用”。启动失败会尝试恢复旧服务，失败信息显示在页面中；应用会短暂重启 DNS/DHCP。

客户端必须把 Pilot 的 LAN 地址作为 DNS。DHCP 会同时下发 Pilot LAN 地址作为网关与唯一 DNS，不下发公共 DNS 作为第二项，防止客户端绕过本地记录。当前不会拦截指定其他 DNS 的流量，也不会拦截 DoH/DoT。开发虚拟机的内部网络需要另一台连接相同内部网络的虚拟机作为客户端；宿主机通常无法直接访问这个 LAN 地址。

本地记录精确匹配、忽略大小写及末尾点，支持同名多条 A/AAAA。命中域名但查询类型未配置时返回 NOERROR 空答案（NODATA），不会继续请求上游；不会自动匹配子域名。上游查询支持 UDP 截断后 TCP 重试，校验事务 ID 和问题，超时返回 SERVFAIL。没有上游缓存、通配符、CNAME、DNSSEC 验证或加密 DNS；AAAA 记录可通过 IPv4 DNS 服务查询，但这不代表已配置 IPv6 网络。

DNS 的 UDP/TCP socket 同时绑定 LAN IPv4 和网卡，并限制来源网段；不监听 WAN。网络助手负责恢复保存的服务和异常退出重试。服务运行时，WAN/LAN 应用接口会要求先停用 DHCP/DNS，避免地址池及 DNS 地址失效；重新配置网络后再启用。配置保存在 `/var/lib/pilot-netd/services/services.json`。计数随 DNS 服务重启清零。

DHCP 目前不提供静态租约绑定和 DHCPv6。勿将手动设置的客户端固定地址放在动态地址池内。可用 `nslookup lighten012.home <Pilot-LAN-IP>` 验证自定义记录，使用未配置域名验证上游转发；解析成功不代表目标主机存在或其 Web 服务已启动。

实现参考：[miekg/dns](https://github.com/miekg/dns)、[dnsmasq 手册](https://thekelleys.org.uk/dnsmasq/docs/dnsmasq-man.html)。家庭内网域名也可以使用专门保留的 [home.arpa](https://www.rfc-editor.org/info/rfc8375/)。

## 防火墙与 NAT

安装 `sudo apt install iptables`。使用系统提供的 `iptables` 和 `iptables-restore` 命令；Debian 默认的 iptables-nft 兼容后端也受支持，无需切换 alternatives。

1. 打开“防火墙与 NAT”，启用转发防火墙；共享上网时同时开启出口 NAT。
2. 默认选择“放行未匹配流量”。规则仅作用于 LAN→WAN，可按来源/目标 IPv4 或网段、TCP/UDP 目标端口（例如 `443`、`8000-8010`）及 ICMP 匹配。
3. 多条规则从上到下匹配，以首条启用且匹配的规则为准；支持上移、下移与停用。
4. 校验并预览，点击“应用并开始测试”。在 LAN 客户端用新连接测试联网，90 秒内点击“网络正常，确认保留”。未确认、助手重启或应用失败会恢复旧配置。

规则自动绑定已确认的 WAN/LAN，启用期间须先停用并确认才能修改网络。仅替换 `PILOT_FWD`、`PILOT_NAT` 专用链，不清空全局规则；已建立连接及返回流量放行，WAN 主动连接 LAN 被阻止。NAT 限定 LAN 网段经 WAN 出口的源地址伪装。本版不管理本机 INPUT/OUTPUT、IPv6 或 DNAT 端口映射，因此页面规则不会限制访问路由器自身的 SSH、Web、DNS/DHCP。已建立连接不会因新增阻止规则立即断开。

配置与未确认事务保存在 `/var/lib/pilot-netd/firewall/firewall.json`。确认配置由网络助手在启动时恢复；停用恢复首次启用前的 `net.ipv4.ip_forward` 值，保留其他系统规则。不要手动改动 Pilot 专用链；本版不持续检查外部工具造成的规则漂移。

实现参考：[iptables-restore 手册](https://man7.org/linux/man-pages/man8/iptables-restore.8.html)、[iptables 扩展手册](https://man7.org/linux/man-pages/man8/iptables-extensions.8.html)。

## 隔离网络集成测试

以下命令需要 root，在临时 Linux 网络命名空间中创建测试客户端与网卡，并在结束后清理；不会修改主机真实网卡配置。依赖 Python 3、iproute2、ifupdown、dnsmasq 二进制和 DHCP 客户端。

```sh
sudo python3 tests/services_integration.py ./bin/pilot-netd
sudo python3 tests/firewall_integration.py ./bin/pilot-netd # 另需 iptables
sudo python3 tests/network_integration.py ./bin/pilot-netd
sudo python3 tests/network_dhcp_integration.py ./bin/pilot-netd
```

测试详情及未覆盖范围见 [VALIDATION.md](VALIDATION.md)。
