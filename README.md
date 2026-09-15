# Lighten012-Pilot

Go + Vue 3 + TypeScript + Vite 构建的个人软路由管理项目。当前只实现 Linux 系统监控，尚未启用路由配置能力。

## 默认部署方式

- 页面：http://127.0.0.1:8080
- 运行环境：Debian 13 / x86_64
- 建议部署目录：`/opt/lighten012-pilot`
- systemd 服务：`lighten012-pilot`
- 服务使用 systemd DynamicUser 运行，无 root 权限。
- 当前是可信局域网内的只读开发版本，无登录认证及 HTTPS；后续开放配置能力前需要实现认证与授权。

## 已实现

- CPU 平均使用率：每两秒计算 `/proc/stat` 差值，排除 guest 的重复计数；idle 和 iowait 视为非忙碌时间。
- 内存：`MemTotal - MemAvailable`，包含对可回收缓存的考虑，与简单 free 数值不同。
- 系统运行时间、1/5/15 分钟负载、主机名、系统架构。
- 非回环网卡名称、IPv4/IPv6 地址、链路状态和收发速率。
- 内存中的最近 120 个采样点，约四分钟历史；服务重启后重新积累，不伪造历史。
- 总览、系统监控、功能规划；刷新、暂停与恢复、时间范围选择、断连提示。

接口速率单位为字节/秒，不是 bit/s，也不是宽带测速；所有非回环接口合计在桥接或多接口转发时可能重复计数。未自动判定 WAN/LAN。首页拓扑明确为规划示意。

## 目录

```text
cmd/pilot/          HTTP 服务入口与接口测试
internal/monitor/   Linux 采样与计算测试
web/src/            Vue 页面、API 类型与功能目录
deploy/             systemd 服务文件
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
./bin/pilot -listen 127.0.0.1:8080 -web web/dist
```

若 Windows 沙箱中默认 Vite 配置加载器遇到父目录访问限制，可使用 `npm run build -- --configLoader runner`。

Go 通过 HTTP 提供构建后的静态网页与 API，生产运行不需要 Node.js。Go 无第三方依赖。

## 服务管理

将项目及构建产物放到 `/opt/lighten012-pilot` 后安装服务。默认只监听本机 `127.0.0.1:8080`；需要局域网访问时，通过 systemd override 设置 `PILOT_LISTEN=<服务器局域网IP>:8080`。具体部署地址不写入仓库。

```sh
sudo install -m 644 deploy/lighten012-pilot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now lighten012-pilot
```

```sh
systemctl status lighten012-pilot
systemctl restart lighten012-pilot
journalctl -u lighten012-pilot -n 50 --no-pager
```

后端 API：`GET /api/health`、`GET /api/monitor`。首次两秒采样完成前，监控返回 503；采集出错同样返回 503，前端保留旧数据并显示异常。未实现的 API 返回 404，当前没有任何修改系统的 API。

## 后续功能

WAN/LAN 配置、DHCP、DNS、防火墙与 NAT、端口转发、日志、备份恢复均处于规划状态。网络配置变更需要配置校验、限时确认与自动回滚。认证、权限和审计应在引入修改操作时一并设计。
