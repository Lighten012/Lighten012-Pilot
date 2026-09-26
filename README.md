# Lighten012-Pilot

一个专注于 DNS 的个人内网服务。网页可添加和删除自定义 A/AAAA 解析记录，并设置上游 DNS；也可只读查看当前网卡及其地址、状态。命中自定义域名时直接回答指定地址；其他域名转发给上游（默认 `119.29.29.29`）。配置保存在 JSON 文件中，服务重启后仍然生效。支持 UDP 和 TCP DNS。

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
  -dns 192.168.50.178:53 \
  -lan 192.168.50.0/24 \
  -config /var/lib/lighten012-pilot-v2/config.json
```

当前部署在 `192.168.50.178`：管理页面为 `http://192.168.50.178:8080/`，DNS 地址为 `192.168.50.178`。根据自己的网络修改监听地址和允许访问 DNS 的子网。管理页面没有账号或密钥，应只绑定可信内网地址。

## 使用

在网页输入域名、类型、目标 IP，点击“添加记录”，再点击“保存并应用”。删除记录或更改上游后也要保存。例如添加 `lighten012.home → 192.168.50.178` 后，在同网段客户端运行：

```sh
nslookup lighten012.home 192.168.50.178
nslookup example.com 192.168.50.178
```

第一条验证自定义记录，第二条验证上游转发。自定义记录只做**精确域名匹配**；同一域名的其他查询类型返回空答案，不会意外转发到上游。

接口：`GET /api/state` 读取配置和查询计数；`GET /api/interfaces` 使用 Go 标准库读取当前网卡列表；`PUT /api/config` 保存配置。修改请求需带 `X-Pilot-Request: 1` 请求头，网页会自动处理。网卡接口只读，不修改系统网络配置，也不需要 root 权限。
