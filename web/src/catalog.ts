export const features = [
  {
    id: "monitor",
    name: "系统监控",
    group: "可观测性",
    desc: "CPU、内存、运行时间与网卡实时状态。",
    scope: "读取 /proc 与系统网络接口，定时采样并展示历史趋势。",
    level: "基础",
    recommended: true,
  },
  {
    id: "network",
    name: "WAN / LAN 配置",
    group: "网络基础",
    desc: "管理接口角色、地址、网关与上网方式。",
    scope: "先支持静态 IPv4 和 DHCP 客户端；包含配置校验、限时确认与自动回滚。",
    level: "核心",
    recommended: true,
  },
  {
    id: "dhcp",
    name: "DHCP 服务",
    group: "网络基础",
    desc: "自动分配 IPv4 地址，查看有效租约。",
    scope: "地址池、租期、网关和 Pilot DNS 下发、租约列表；静态绑定后续实现。",
    level: "核心",
    recommended: false,
  },
  {
    id: "dns",
    name: "DNS 设置",
    group: "网络基础",
    desc: "配置自定义域名记录与上游 DNS 转发。",
    scope:
      "Go DNS 服务：精确 A/AAAA 覆盖、上游转发、UDP/TCP、基础计数；尚无缓存和加密 DNS。",
    level: "基础",
    recommended: false,
  },
  {
    id: "firewall",
    name: "防火墙与 NAT",
    group: "访问控制",
    desc: "定义访问边界，让内网设备共享出口。",
    scope:
      "基于 iptables 实现 IPv4 转发过滤、出口 NAT 与端口映射；支持顺序规则、90 秒确认和回滚。本机入站与 IPv6 后续实现。",
    level: "核心",
    recommended: false,
  },
  {
    id: "forward",
    name: "端口转发",
    group: "访问控制",
    desc: "将指定入口端口映射到内网服务。",
    scope:
      "TCP/UDP 单端口映射、来源限制、LAN 目标校验、端口冲突检测与开关；共用防火墙预览、确认与回滚。",
    level: "进阶",
    recommended: false,
  },
  {
    id: "logs",
    name: "系统日志",
    group: "可观测性",
    desc: "集中查看服务事件与网络故障线索。",
    scope:
      "读取 systemd journal，按服务、级别、时间和关键词筛选，支持游标分页、自动刷新及已加载日志导出。",
    level: "基础",
    recommended: false,
  },
  {
    id: "backup",
    name: "配置备份与恢复",
    group: "系统管理",
    desc: "保存可恢复的配置，放心尝试新设置。",
    scope:
      "下载版本化 JSON 备份，预览差异并恢复 WAN/LAN、DHCP/DNS、防火墙与映射；支持 90 秒确认、失败及重启回滚，保留现有接口角色。",
    level: "基础",
    recommended: false,
  },
];
