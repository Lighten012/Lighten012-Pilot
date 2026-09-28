# Lighten012-Pilot

一个面向个人内网的轻量路由服务。网页可添加和删除自定义 A/AAAA 解析记录、设置首选与备用上游 DNS，也可选择一次 WAN/LAN 网卡、查看 LAN 设备并修改 LAN IPv4 地址。WAN 始终只读；LAN 经 WAN 的 IPv4 转发和 NAT 自动生效，LAN 提供最小 DHCPv4，不提供端口转发。DNS 只监听所选 LAN 网卡的 IPv4 地址，并只接受该 LAN 网段的客户端；LAN IP 修改后 DNS 会同步切换，WAN 不提供 DNS。未命中自定义记录的域名转发给上游（默认 `119.29.29.29`）；首选失败时尝试备用，两者至少填写一个。DNS 支持 UDP 和 TCP。

服务是一个 Go 程序，网页已经嵌入二进制文件。**可以在本机编译，服务器只运行编译结果**，无需安装 Go、Node.js 或放置源代码。

## 代码结构

根目录的 `main.go` 只嵌入网页并启动服务。`internal/pilot` 组装各模块、处理 Web API 和服务生命周期；`internal/dns` 管理解析记录、上游查询与 LAN DNS 监听；`internal/dhcp` 管理地址池、租约和 DHCP 协议；`internal/network` 管理网卡、LAN 地址、设备探测、转发与代理规则；`internal/mihomo` 管理订阅和 Mihomo 控制接口。`internal/httpx` 与 `internal/storage` 放置共用的 HTTP 和文件写入工具。网页资源在 `web/`，部署单元在 `deploy/`。

## 本机编译

需要 Go 1.24 或更新版本。先运行测试：

```sh
go test ./...
```

在 Windows 命令提示符中为 x86_64 Linux 编译：

```bat
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=amd64
go build -trimpath -o pilot-linux-amd64 .
```

若目标是树莓派 5，将 `GOARCH` 改为 `arm64`，同时换一个输出文件名。

## 运行

将编译好的文件复制到服务器。53 和 67 端口需要相应权限；长期运行可以参考 [`deploy/lighten012-pilot.service`](deploy/lighten012-pilot.service)。示例命令：

```sh
./pilot-linux-amd64 \
  -web 0.0.0.0:80 \
  -config /var/lib/lighten012-pilot/config.json \
  -network-config /var/lib/lighten012-pilot/network.json \
  -dhcp-leases /var/lib/lighten012-pilot/dhcp-leases.json
```

当前部署在 `192.168.50.24`：Web 监听所有 IPv4 接口的 80 端口，可通过 `http://192.168.50.24/` 或 `http://10.0.0.1/` 打开。DNS 和 DHCP 由保存的 LAN 网卡决定，目前 LAN 地址是 `10.0.0.1`。未选择 WAN/LAN 角色时，两项服务都不监听；保存角色后立即启动。管理页面没有账号或密钥，仅应在可信网络中运行。

## WAN / LAN

在页面中选择两张不同的网卡并保存。选择写入 `network.json`，后续打开页面直接显示 WAN/LAN，不再要求重复选择。程序会阻止把管理页面所在接口选为 LAN；WAN 只显示地址和状态，没有修改接口。

LAN 页面只接受新的 IPv4 地址，例如从 `192.168.60.1` 改为 `192.168.70.1`，原前缀 `/24` 保持不变。程序先添加新地址、保存静态配置，再移除旧地址；中途出错会尝试恢复。新网段不能与 WAN 网段重叠。当前只支持由 ifupdown 管理、具有单个 IPv4 地址且配置位于 `/etc/network/interfaces.d/pilot-<网卡名>` 的静态 LAN 接口；不会修改复杂网络配置或管理网卡。

修改 LAN 地址需要 `CAP_NET_ADMIN` 和对该接口配置目录的写权限，仓库中的 systemd 单元已限定这些权限。保存后 DHCP 地址池、网关和 DNS 选项会随新 LAN 网段更新；已有客户端需要重新获取地址。

## LAN 上网转发

保存网卡角色后，程序会启用 IPv4 转发，并用独立的 `PILOT_FWD`、`PILOT_NAT` iptables 链配置 LAN → WAN 转发、源地址伪装及返回流量放行。WAN 主动发往 LAN 的新连接会被丢弃。规则仅匹配所选网卡和当前 LAN 网段，不清空其他 iptables 规则；服务重启时自动恢复，LAN 网段变化后同步更新。

LAN 客户端可以使用 DHCP，或手动设置地址、默认网关（当前为 `10.0.0.1`）和 DNS（同为 `10.0.0.1`）。上游连接仍由服务器现有默认路由管理。当前只支持 IPv4；不提供 IPv6 转发或端口转发。

## LAN DHCP

DHCPv4 只绑定所选 LAN 网卡；WAN 不响应。当前 `10.0.0.1/24` 的自动地址池为 `10.0.0.100–10.0.0.200`，默认租期 12 小时，同时下发子网掩码、当前 LAN 地址作为默认网关和 DNS。LAN 地址改变后，地址池自动随新网段调整。手动配置的设备应使用地址池之外的 IP，例如 `10.0.0.2`。

租约保存在 `dhcp-leases.json`，服务重启后仍有效。这个最小实现支持分配、续租、释放和冲突拒绝；暂不支持 DHCP 中继、IPv6 或网页修改地址池。一个 LAN 网段应只运行一个 DHCP 服务。

## LAN 设备

「LAN 设备」合并有效 DHCP 租约和 LAN 网卡的 IPv4 邻居记录，点击“重新探测”时通过 ARP 请求检查这些已知地址。页面显示在线、离线/未响应、MAC 不符或待确认，并优先采用当前租约的 MAC，避免旧邻居记录对应到错误设备。服务器需安装 `iputils-arping`。探测不会扫描整个网段；没有租约且未留在邻居表中的设备不会列出。休眠或过滤 ARP 的设备也可能显示为未响应。

## 按设备 MAC 解析

添加 DNS 记录时可选择「LAN 设备」，填入作为服务端的设备 MAC（例如 `aa:bb:cc:dd:ee:ff`）。Pilot 根据该 MAC 查询自己发放的有效 DHCP 租约，将域名解析为设备当前 LAN IPv4。所有 LAN 客户端得到相同的结果；设备换地址后取得新租约，解析结果也会跟着改变。此模式只支持 A 记录，每个域名和类型只能有一条记录；把已有固定记录改成设备绑定时，先删除旧记录，再添加新记录并保存。

设备必须从 Pilot 获取 DHCP 地址；手动配置 IP 或使用其他 DHCP 服务时，Pilot 无法从租约找到它，A 查询会返回 SERVFAIL，避免指向旧地址。客户端应直接使用 Pilot 的 LAN DNS；若经过其他 DNS 代理或缓存，旧答案可能在 TTL 期间继续出现。固定 IP 模式仍可用于不需要跟随 DHCP 的记录。

## 使用

在网页输入域名、类型、目标 IP，点击“添加记录”，再点击“保存并应用”。删除记录或更改上游后也要保存。例如添加 `lighten012.home → 10.0.0.1` 后，在 LAN 网段客户端运行：

```sh
nslookup lighten012.home 10.0.0.1
nslookup example.com 10.0.0.1
```

第一条验证自定义记录，第二条验证上游转发。自定义记录只做**精确域名匹配**；同一域名的其他查询类型返回空答案，不会意外转发到上游。

接口：`GET /api/state` 读取 DNS 配置和计数；`GET /api/interfaces` 只读网卡列表；`GET /api/network` 读取 WAN/LAN 选择；`GET /api/lan/devices` 读取 LAN 邻居表；`PUT /api/network/roles` 保存接口角色；`PUT /api/network/lan-ip` 修改 LAN IPv4；`PUT /api/config` 保存 DNS 配置。修改请求需带 `X-Pilot-Request: 1` 请求头，网页会自动处理。

## Mihomo 管理模块

侧边栏的 Mihomo 页面管理独立运行的 Mihomo 核心：查看版本、运行模式、流量速率、活动连接与代理组；切换手动代理组、测试当前节点延迟、重载配置，以及保存和更新订阅链接。Pilot 通过本机 `127.0.0.1:9090` 访问核心，浏览器不会直接连接控制接口。

部署时从 [Mihomo 官方发行版](https://github.com/MetaCubeX/mihomo/releases)选择匹配架构的二进制，安装为 `/usr/local/bin/mihomo`，将 [`deploy/mihomo.yaml`](deploy/mihomo.yaml) 复制到 `/var/lib/lighten012-pilot/mihomo.yaml`，将 [`deploy/lighten012-mihomo.service`](deploy/lighten012-mihomo.service) 复制到 `/etc/systemd/system/`，然后执行 `systemctl daemon-reload && systemctl enable --now lighten012-mihomo`。当前 Debian x86-64 虚拟机使用 v1.19.31 的 `linux-amd64-v1` 发行包，已按 GitHub SHA-256 校验。

网页只需输入 HTTP(S) 订阅链接。Pilot 下载最多 1 MiB 的 Mihomo/Clash YAML，生成 Pilot 使用的本机 DNS、透明代理和控制端口配置，再调用 `mihomo -t` 校验、应用并重载；失败会恢复旧配置。订阅链接保存在配置旁的 `.subscription` 文件中，权限为 `0600`，页面只显示域名，之后可点“更新订阅”重新拉取。当前不支持只返回 Base64 节点列表的订阅。导入后，代理组与节点会出现在页面上。

全局域名白名单只接受域名，例如 `google.com`，并匹配其子域名；不能按 HTTPS URL 路径分流。白名单保存在 `/var/lib/lighten012-pilot/mihomo.yaml.whitelist.json`。LAN 设备仍向 Pilot 的 53 端口查询 DNS；Pilot 将白名单域名转给 Mihomo 在 `127.0.0.1:1053` 的 DNS，其他域名继续用本地记录或 Pilot 上游 DNS。Mihomo 对白名单域名返回 fake-IP，Pilot 把应答中的 A 地址放入带 TTL 的 `PILOT_PROXY_IPS` ipset；来自 LAN、目标命中该集合的 TCP 连接由专用 `PILOT_PROXY` iptables 链转到 Mihomo 的 7893 端口，UDP 连接经 TProxy 7896 入口交给 Mihomo，其余流量照常由 Pilot 转发。Mihomo 使用独立的节点引导 DNS，避免查询循环。订阅更新后会重新生成这些规则，不采用订阅自带的分流规则。运行环境需要 `ipset` 和内核的 REDIRECT/TProxy 支持。当前仅支持 IPv4；白名单模式的客户端必须使用 Pilot DNS，其他 DNS、缓存或直接连 IP 的连接可能绕过白名单。

LAN 设备列表还可按 MAC 地址切换“全部经 Mihomo”。开启后，该设备的公网 TCP 连接重定向到 Mihomo 的 7894 入口，UDP 连接经 TProxy 7895 入口，均直接使用白名单页面选择的代理组，不再检查域名白名单；关闭后回到原有白名单分流。设置保存在 `/var/lib/lighten012-pilot/mihomo.yaml.whitelist.json.devices.json`，DHCP 重新分配 IP 后仍生效。LAN 私有地址保持直连。UDP TProxy 使用 fwmark `0x102` 和路由表 `102`；Pilot 仍是设备网关，本地 DNS 查询仍由 Pilot 处理。
