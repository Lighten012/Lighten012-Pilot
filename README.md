# Lighten012-Pilot

基于最小 Debian 系统逐步构建的个人软路由，使用 **Go + Vue 3 + TypeScript + Vite** 提供 Web 管理界面。项目不依赖 OpenWrt，通过 Linux 现有网络能力实现自己的配置管理、DNS 解析逻辑与恢复机制。

**当前版本：v0.6.0。** 最初规划的八项基础模块均已实现，已在 Debian 13 / x86_64 虚拟机部署和验证。适合个人内网使用及学习 Linux 网络管理；打开页面即可操作，没有管理员密钥或登录流程。

[部署与构建](#部署与构建) · [第一次配置](#第一次配置) · [DNS 使用](#dhcp-与-dns-使用) · [备份恢复](#配置备份与恢复) · [常见问题](#常见问题) · [验证记录](VALIDATION.md) · [更新记录](CHANGELOG.md)

## 功能概览

| 模块 | 已实现范围 |
| --- | --- |
| 系统监控 | CPU、内存、负载、运行时间、网卡地址、收发速率与短期趋势 |
| WAN / LAN | WAN DHCP/静态 IPv4、LAN 静态 IPv4、草稿、预览、限时确认与回滚 |
| DHCP | LAN 地址池、租期、网关与 DNS 下发、有效租约 |
| DNS | Go 服务，精确 A/AAAA 本地记录覆盖，其他域名转发上游，UDP/TCP |
| 防火墙与 NAT | iptables IPv4 转发过滤、顺序规则、出口 MASQUERADE |
| 端口转发 | TCP/UDP 单端口映射、来源限制、冲突检测与开关 |
| 系统日志 | systemd journal 来源/级别/时间/关键词筛选、分页、刷新及导出 |
| 配置备份与恢复 | JSON 导出、导入校验、差异预览、整套恢复与持久化回滚 |

## 工作方式

```text
浏览器（Vue） → Go Web 服务（pilot，非 root）
                     ↓ Unix socket
               网络助手（pilot-netd，root）
                     ├─ ifupdown：接口地址与路由
                     ├─ Go DNS：本地解析与上游转发
                     ├─ dnsmasq：仅提供 DHCP
                     ├─ iptables：转发过滤、NAT 与端口映射
                     └─ journalctl / JSON 文件：日志读取与配置持久化
```

Web 服务使用 systemd DynamicUser；特权助手仅监听 `/run/pilot-netd/control.sock`，允许 `pilot-net` 组访问。修改请求检查同源 Origin 和 `X-Pilot-Request: 1`。管理页面按个人内网场景设计，请将监听地址配置为自己的管理网地址。

## 监控数据口径

- CPU 平均使用率：每两秒计算 `/proc/stat` 差值，排除 guest 的重复计数；idle 和 iowait 视为非忙碌时间。
- 内存：`MemTotal - MemAvailable`，包含对可回收缓存的考虑，与简单 free 数值不同。
- 系统运行时间、1/5/15 分钟负载、主机名、系统架构。
- 非回环网卡名称、IPv4/IPv6 地址、链路状态和收发速率。
- 内存中的最近 120 个采样点，约四分钟历史；服务重启后重新积累，不伪造历史。

接口速率单位为字节/秒，不是 bit/s，也不是宽带测速；所有非回环接口合计在桥接或多接口转发时可能重复计数。未自动判定 WAN/LAN。首页拓扑明确为规划示意。

## 目录

```text
cmd/pilot/          HTTP 服务入口与接口测试
cmd/pilot-netd/     特权网络助手，仅监听 Unix socket
internal/control/  请求来源校验、网络 API 代理
internal/network/  ifupdown 配置与持久化变更事务
internal/services/ Go DNS 解析逻辑、DHCP 进程及配置生命周期
internal/firewall/ iptables 规则生成、NAT 与持久化回滚事务
internal/systemlogs/ 受限 journal 查询、筛选与游标分页
internal/backup/    版本化配置包、整套恢复与持久化回滚事务
internal/monitor/   Linux 采样与计算测试
web/src/            Vue 页面、API 类型与功能目录
deploy/             systemd 服务文件
tests/              Linux 隔离网络集成测试
```

## 部署与构建

运行与验证环境为 Debian 13 / x86_64，推荐两张网络接口分别连接上游和独立 LAN。虚拟机可使用虚拟网卡，宿主机不需要两张物理网卡。树莓派/ARM64 尚未做实际部署验证。

构建需要 Go 1.23+（`go.mod` 指定 Go 1.24.4 工具链，允许自动下载时会选择相应工具链）、Node.js 20.19+ 或 22.12+。监控后端需运行在 Linux，Windows/macOS 不具备所需的 `/proc` 数据源。

Debian 运行依赖：

```sh
sudo apt update
sudo apt install git ca-certificates ifupdown iproute2 dhcpcd-base dnsmasq-base iptables
```

Go 和 Node.js 请预先安装满足上述要求的版本。已有 DHCP 客户端时可继续使用其受 ifupdown 支持的配置，不要同时启用多套网络管理器。只安装 `dnsmasq-base` 提供 DHCP 二进制，DNS 由 Pilot 实现。

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

该开发方式默认连接 `/run/pilot-netd/control.sock`；只运行 Web 服务可以查看监控，修改网络需要安装并启动下面的网络助手，且运行 Web 的用户具有 socket 访问权限。

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
sudo install -d /opt/lighten012-pilot/bin /opt/lighten012-pilot/web/dist
sudo install -m 755 bin/pilot bin/pilot-netd /opt/lighten012-pilot/bin/
sudo cp -a web/dist/. /opt/lighten012-pilot/web/dist/
sudo groupadd --system pilot-net # 已存在时跳过
sudo install -m 644 deploy/pilot-netd.service /etc/systemd/system/
sudo install -m 644 deploy/lighten012-pilot.service /etc/systemd/system/
sudo systemctl daemon-reload
```

运行 `sudo systemctl edit lighten012-pilot` 配置管理地址，例如：

```ini
[Service]
Environment=PILOT_LISTEN=192.168.50.10:8080
```

将示例 IP 替换为本机真实管理地址，然后设置下文的受保护网卡，再启动两个服务：

```sh
sudo systemctl enable --now pilot-netd lighten012-pilot
```

浏览器访问 `http://<管理地址>:8080/`。未设置 override 时仅可通过 `http://127.0.0.1:8080/` 从路由器本机访问。

部署需要 `ifupdown`、`iproute2` 和可用的 DHCP 客户端。仅支持 `/etc/network/interfaces` 使用 `source /etc/network/interfaces.d/*` 的标准布局，以及简单的 DHCP/静态 IPv4 stanza；复杂钩子、桥接及其他网络管理器不在当前支持范围内。

远程开发时先保护当前 SSH/Web 所用网卡：运行 `sudo systemctl edit pilot-netd`，填写以下内容（将 `enp0s3` 替换为实际接口）。服务已运行时再重启助手，使设置生效。保护接口允许保留原配置，不允许修改地址或 DHCP 模式。

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

## 第一次配置

1. **WAN / LAN**：选择两张不同网卡，设置 WAN 获取地址方式和 LAN 静态地址；预览、应用，再在 90 秒内确认。保存草稿不会改变实际网络。
2. **DHCP 与 DNS**：绑定已确认的 LAN，配置解析记录、上游 DNS、地址池和租期，再保存并应用。
3. **防火墙与 NAT**：启用转发防火墙；需要 LAN 共享上网时开启出口 NAT，应用并确认。
4. **客户端验证**：把测试客户端接入 LAN，检查地址获取、DNS 和上网；需要对外提供服务时再添加端口映射。
5. **备份与恢复**：下载一份已验证可用的配置，便于后续调整时恢复。

DHCP/DNS、防火墙与 NAT 默认关闭，需在对应页面启用。启用 DHCP/DNS 和出口 NAT 后，LAN 客户端可以通过 Pilot 获取地址、解析域名和共享 WAN 出口。八项基础模块已完成；更多网络能力仍按需求逐步扩展。

## 当前限制

- 只管理 IPv4；尚无 PPPoE、VLAN、网桥、多 WAN、IPv6 转发或 DHCPv6。
- 已确认的 WAN/LAN 不能迁移接口角色，可继续修改原接口的地址设置。
- DNS 尚无缓存、通配符、CNAME、DNSSEC 验证和加密 DNS；DHCP 尚无静态租约绑定。
- 防火墙管理转发流量，不管理路由器自身 INPUT/OUTPUT；端口映射尚无端口范围与 LAN 回环映射。
- 备份恢复面向当前接口布局，不是完整系统镜像或跨设备自动迁移工具。
- 已完成隔离网络和助手重启验证，长期运行、完整主机重启、断电与实际下游设备验证仍需补充。

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

规则自动绑定已确认的 WAN/LAN，启用期间须先停用并确认才能修改网络。仅替换 `PILOT_FWD`、`PILOT_NAT`、`PILOT_DNAT` 专用链，不清空全局规则；已建立连接及返回流量放行，WAN 仅能通过配置的映射连接 LAN。出口 NAT 限定 LAN 网段经 WAN 的源地址伪装。本版不管理本机 INPUT/OUTPUT 或 IPv6，因此转发过滤不会限制访问路由器自身的 SSH、Web、DNS/DHCP。已建立连接不会因新增阻止规则或停用映射立即断开；请用新连接测试。

配置与未确认事务保存在 `/var/lib/pilot-netd/firewall/firewall.json`。确认配置由网络助手在启动时恢复；停用恢复首次启用前的 `net.ipv4.ip_forward` 值，保留其他系统规则。不要手动改动 Pilot 专用链；本版不持续检查外部工具造成的规则漂移。

实现参考：[iptables-restore 手册](https://man7.org/linux/man-pages/man8/iptables-restore.8.html)、[iptables 扩展手册](https://man7.org/linux/man-pages/man8/iptables-extensions.8.html)。

## 端口转发

打开“端口转发”并添加映射，例如 TCP 外部端口 `18080` → LAN 设备 `192.168.60.100:80`。来源可留空（所有 WAN 来源）或填 IPv4/CIDR。只支持单个外部端口到单个内部端口；TCP/UDP 相同端口可分别映射，同一协议的启用映射不能重复占用外部端口。

转发防火墙须启用，出口源 NAT 可按上网需要独立选择。校验并预览后应用，在 WAN 侧访问 `http://<Pilot-WAN-IP>:18080` 验证，90 秒内确认保留。防火墙、NAT、映射为一份配置，两页均会整体应用。目标设备必须使用 Pilot 作为返回网关；从 LAN 访问 WAN 地址的回环映射不在本版范围。Pilot 位于上游路由器之后时，公网访问还需上游路由器提供相应入口。

预览拒绝映射到路由器自身、LAN 以外、网络地址或广播地址；启用时检查外部端口是否被本机 WAN/通配地址监听。该检查发生在应用前，不持续监控后来启动的本机服务。映射保存至既有防火墙配置，支持停用、删除、手动/超时回滚与重启恢复。

## 系统日志

系统日志页通过网络助手读取 `journalctl`，支持 Pilot 全部服务、Web 服务、网络助手、系统和内核来源；级别为全部、警告及以上、错误及以上，范围为最近 1 小时、24 小时或 7 天。关键词为普通文本，匹配消息和服务名。按时间倒序、每页 100 条；点击“加载更早日志”继续，分页保持查询时刻不变。自动刷新每 10 秒回到最新一页。可导出已加载记录为 UTF-8 文本，页面最多累计 2000 条。

读取不会修改 journal 保留设置或清空日志；跨重启历史是否存在取决于系统 journald 配置。每次查询最多扫描 1000 条，稀疏关键词可能需要继续加载更早记录；每条消息展示上限 4096 字节并标注截断。API 为 `GET /api/logs`，接受受限来源、级别、范围、关键词、分页参数，不支持任意文件路径、命令或正则表达式。网络助手记录配置操作路径和结果，不记录提交的配置正文。

## 配置备份与恢复

“备份与恢复”页面下载已确认配置为版本 1 的 JSON 文件，包含 WAN/LAN、DHCP/DNS、防火墙/NAT 与端口映射。下载不修改运行设置；未应用草稿、租约、日志、程序和其他系统文件不在备份范围。

选择 JSON 文件后先校验并查看差异与完整目标配置，再点击“开始恢复并测试”。恢复会暂停转发防火墙与 DHCP/DNS、调整地址，再恢复依赖服务。成功后有 90 秒检查网络并确认保留；未确认、应用失败、助手重启都会尝试还原导入前的整套配置。还原失败时保留事务并每 10 秒重试，可在页面手动重试；事务结束前其他配置修改被阻止。网络变化可能断开当前页面，请使用对应管理地址重新进入。

当前要求本机已经确认 WAN/LAN，备份沿用相同接口角色，受保护管理接口不能修改；不支持新机器接口自动映射或系统镜像恢复。备份最大 256 KB，不接受未知字段、任意路径或命令。恢复预览会重新检查地址池、解析记录、规则与端口冲突；实际应用前再次校验。相同配置无需恢复。

恢复事务保存在 `/var/lib/pilot-netd/backup.json`，保留导入前配置直到确认或成功回滚。恢复期间对各模块的中间确认由外层持久化事务保护。恢复网络会以导入设置更新网络草稿。API 为 `GET /api/backup/state`、`GET /api/backup/export` 和 `POST /api/backup/{preview,apply,confirm,rollback}`。

## 隔离网络集成测试

以下命令需要 root，在临时 Linux 网络命名空间中创建测试客户端与网卡，并在结束后清理；不会修改主机真实网卡配置。依赖 Python 3、iproute2、ifupdown、dnsmasq 二进制和 DHCP 客户端。

```sh
sudo python3 tests/backup_integration.py ./bin/pilot-netd # 另需 iptables
sudo python3 tests/services_integration.py ./bin/pilot-netd
sudo python3 tests/firewall_integration.py ./bin/pilot-netd # 另需 iptables
sudo python3 tests/systemlogs_integration.py ./bin/pilot-netd # 向 journal 写入带唯一标记的有限测试记录
sudo python3 tests/network_integration.py ./bin/pilot-netd
sudo python3 tests/network_dhcp_integration.py ./bin/pilot-netd
```

测试详情及未覆盖范围见 [VALIDATION.md](VALIDATION.md)。

## 配置文件与接口

| 位置 | 用途 |
| --- | --- |
| `/var/lib/pilot-netd/network.json` | WAN/LAN 已确认配置、草稿与未完成事务 |
| `/var/lib/pilot-netd/services/services.json` | DHCP/DNS 持久化设置 |
| `/var/lib/pilot-netd/services/dhcp.leases` | DHCP 运行租约 |
| `/var/lib/pilot-netd/firewall/firewall.json` | 防火墙、NAT、端口映射与事务 |
| `/var/lib/pilot-netd/backup.json` | 整套恢复的原配置与未完成事务 |

| HTTP 接口 | 用途 |
| --- | --- |
| `GET /api/health`、`GET /api/monitor` | 版本、主机与监控数据 |
| `/api/network/{state,preview,draft,apply,confirm,rollback}` | 网络状态及变更 |
| `/api/network/services/{state,preview,apply}` | DHCP/DNS |
| `/api/network/firewall/{state,preview,apply,confirm,rollback}` | 防火墙、NAT、端口映射 |
| `GET /api/logs` | 受限日志查询 |
| `/api/backup/{state,export,preview,apply,confirm,rollback}` | 备份及整套恢复 |

状态、导出和日志使用 GET，预览及修改使用 POST。优先通过页面修改配置，以便执行依赖校验和恢复事务。

## 常见问题

**宿主机为什么访问不到 LAN 地址？**

VirtualBox 的“内部网络”只连接使用同一内部网络名称的虚拟机。宿主机通常不在其中；可以添加第二台测试虚拟机，网卡连接到相同内部网络。管理页面继续通过 WAN/管理网地址访问。

**怎样确认自定义 DNS 生效？**

在能访问 LAN 的客户端执行 `nslookup lighten012.home <Pilot-LAN-IP>`，再查询一个未配置的域名验证上游转发。解析结果指向某个 IP，不代表该 IP 上真的运行着服务；不要用能否打开网页作为唯一判断。

**恢复或网络修改后页面断开了怎么办？**

先用修改后的管理地址重新打开页面。无法确认时等待 90 秒回滚；若恢复失败，助手会保留事务并重试。可以通过虚拟机控制台查看 `journalctl -u pilot-netd -n 100 --no-pager`。

**备份下载后在哪里？**

使用浏览器的默认下载位置，文件名为 `pilot-backup-<时间>.json`。恢复时重新选择该文件，先查看差异；与当前配置一致时不会重复应用。

**网络助手不可用怎么办？**

检查 `systemctl status pilot-netd lighten012-pilot` 和 `journalctl -u pilot-netd -n 100 --no-pager`。常见原因包括缺少系统依赖、存在另一网络管理器、网络配置布局不受支持，或监听端口被其他服务占用。
