# Lighten012-Pilot

一个面向个人内网的 DNS 服务。网页可添加和删除自定义 A/AAAA 解析记录、设置上游 DNS，也可选择一次 WAN/LAN 网卡并修改 LAN IPv4 地址。WAN 始终只读，不提供 DHCP、防火墙、NAT 等功能。DNS 只监听所选 LAN 网卡的 IPv4 地址，并只接受该 LAN 网段的客户端；LAN IP 修改后 DNS 会同步切换，WAN 不提供 DNS。未命中自定义记录的域名转发给上游（默认 `119.29.29.29`）；DNS 支持 UDP 和 TCP。

服务是一个 Go 程序，网页已经嵌入二进制文件。**可以在本机编译，服务器只运行编译结果**，无需安装 Go、Node.js 或放置源代码。

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

将编译好的文件复制到服务器。53 端口需要相应权限；长期运行可以参考 [`deploy/lighten012-pilot-v2.service`](deploy/lighten012-pilot-v2.service)。示例命令：

```sh
./pilot-linux-amd64 \
  -web 192.168.50.178:8080 \
  -config /var/lib/lighten012-pilot-v2/config.json \
  -network-config /var/lib/lighten012-pilot-v2/network.json
```

当前部署在 `192.168.50.178`：管理页面为 `http://192.168.50.178:8080/`。DNS 地址由保存的 LAN 网卡决定，目前是 `10.0.0.1`。未选择 WAN/LAN 角色时，DNS 不监听任何网卡；保存角色后立即启动。管理页面没有账号或密钥，应只绑定可信内网地址。

## WAN / LAN

在页面中选择两张不同的网卡并保存。选择写入 `network.json`，后续打开页面直接显示 WAN/LAN，不再要求重复选择。程序会阻止把管理页面所在接口选为 LAN；WAN 只显示地址和状态，没有修改接口。

LAN 页面只接受新的 IPv4 地址，例如从 `192.168.60.1` 改为 `192.168.70.1`，原前缀 `/24` 保持不变。程序先添加新地址、保存静态配置，再移除旧地址；中途出错会尝试恢复。新网段不能与 WAN 网段重叠。当前只支持由 ifupdown 管理、具有单个 IPv4 地址且配置位于 `/etc/network/interfaces.d/pilot-<网卡名>` 的静态 LAN 接口；不会修改复杂网络配置或管理网卡。

修改 LAN 地址需要 `CAP_NET_ADMIN` 和对该接口配置目录的写权限，仓库中的 systemd 单元已限定这些权限。保存后下游设备需要使用新网段地址，并将 DNS 指向新的 LAN 地址；此功能不提供 DHCP 或路由转发。

## 使用

在网页输入域名、类型、目标 IP，点击“添加记录”，再点击“保存并应用”。删除记录或更改上游后也要保存。例如添加 `lighten012.home → 10.0.0.1` 后，在 LAN 网段客户端运行：

```sh
nslookup lighten012.home 10.0.0.1
nslookup example.com 10.0.0.1
```

第一条验证自定义记录，第二条验证上游转发。自定义记录只做**精确域名匹配**；同一域名的其他查询类型返回空答案，不会意外转发到上游。

接口：`GET /api/state` 读取 DNS 配置和计数；`GET /api/interfaces` 只读网卡列表；`GET /api/network` 读取 WAN/LAN 选择；`PUT /api/network/roles` 保存接口角色；`PUT /api/network/lan-ip` 修改 LAN IPv4；`PUT /api/config` 保存 DNS 配置。修改请求需带 `X-Pilot-Request: 1` 请求头，网页会自动处理。
