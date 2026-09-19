<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import {
  Activity,
  ArrowDownLeft,
  ArrowUpRight,
  ArrowRight,
  Check,
  ChevronRight,
  CircleHelp,
  Cpu,
  Globe,
  HardDrive,
  LayoutDashboard,
  Layers,
  Network,
  RefreshCw,
  Server,
  Settings2,
  Shield,
  Terminal,
  Wifi,
  X,
} from "lucide-vue-next";
import { getHealth, getMonitor, type Health, type Sample } from "./api";
import { features } from "./catalog";
import NetworkPage from "./components/NetworkPage.vue";
import ServicesPage from "./components/ServicesPage.vue";
import FirewallPage from "./components/FirewallPage.vue";
import BackupPage from "./components/BackupPage.vue";
import LogsPage from "./components/LogsPage.vue";
const page = ref("overview"),
  health = ref<Health | null>(null),
  sample = ref<Sample | null>(null),
  error = ref(""),
  loading = ref(false),
  paused = ref(false),
  range = ref(60),
  detail = ref<(typeof features)[number] | null>(null),
  toast = ref("");
let timer: ReturnType<typeof setInterval> | undefined,
  toastTimer: ReturnType<typeof setTimeout> | undefined;
const navigation = [
  { id: "overview", label: "总览", icon: LayoutDashboard },
  { id: "monitor", label: "系统监控", icon: Activity },
  { id: "network", label: "WAN / LAN", icon: Network },
  { id: "services", label: "DHCP 与 DNS", icon: Globe },
  { id: "firewall", label: "防火墙与 NAT", icon: Shield },
  { id: "forward", label: "端口转发", icon: ArrowRight },
  { id: "logs", label: "系统日志", icon: Terminal },
  { id: "backup", label: "备份与恢复", icon: Settings2 },
  { id: "features", label: "功能规划", icon: Layers },
];

const implemented = [
  "monitor",
  "network",
  "dhcp",
  "dns",
  "firewall",
  "forward",
  "logs",
  "backup",
];
function featurePage(id: string) {
  return ["dhcp", "dns"].includes(id) ? "services" : id;
}
const title = computed(() =>
  page.value === "overview"
    ? "网络，一目了然。"
    : page.value === "monitor"
      ? "观察每一次变化。"
      : page.value === "network"
        ? "让网络，按你的方式连接。"
        : page.value === "services"
          ? "地址有序，解析随心。"
          : page.value === "firewall"
            ? "连接有边界，上网有秩序。"
            : page.value === "forward"
              ? "把服务，连接到需要它的人。"
              : page.value === "logs"
                ? "从记录中，找到答案。"
                : page.value === "backup"
                  ? "保存现在，放心尝试。"
                  : "从基础，逐步构建。",
);
const current = computed(() => sample.value?.history.at(-1));
const points = computed(() => sample.value?.history.slice(-range.value) || []);
const memory = computed(() =>
  sample.value ? (sample.value.memoryUsed / sample.value.memoryTotal) * 100 : 0,
);
const chartMax = computed(
  () => Math.max(1024, ...points.value.flatMap((p) => [p.rx, p.tx])) * 1.2,
);
const online = computed(
  () => sample.value?.interfaces.filter((i) => i.state === "up").length || 0,
);
function bytes(n: number) {
  if (n >= 1024 ** 3) return (n / 1024 ** 3).toFixed(1) + " GB";
  if (n >= 1024 ** 2) return (n / 1024 ** 2).toFixed(1) + " MB";
  if (n >= 1024) return (n / 1024).toFixed(1) + " KB";
  return n.toFixed(0) + " B";
}
function uptime(n: number) {
  const d = Math.floor(n / 86400),
    h = Math.floor((n % 86400) / 3600),
    m = Math.floor((n % 3600) / 60);
  return d ? `${d} 天 ${h} 小时` : `${h} 小时 ${m} 分钟`;
}
function line(key: "rx" | "tx" | "cpu") {
  return points.value
    .map(
      (p, i) =>
        `${(i / Math.max(1, points.value.length - 1)) * 900},${150 - (p[key] / (key === "cpu" ? 100 : chartMax.value)) * 130}`,
    )
    .join(" ");
}
function notify(s: string) {
  toast.value = s;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (toast.value = ""), 3500);
}
function openFeature(id: string) {
  detail.value = features.find((f) => f.id === id) || null;
}
async function refresh() {
  if (loading.value) return;
  loading.value = true;
  try {
    const [h, s] = await Promise.all([getHealth(), getMonitor()]);
    health.value = h;
    sample.value = s;
    error.value = "";
  } catch (e) {
    error.value = e instanceof Error ? e.message : "连接失败";
  } finally {
    loading.value = false;
  }
}
onMounted(() => {
  refresh();
  timer = setInterval(() => {
    if (!paused.value) refresh();
  }, 2000);
});
onUnmounted(() => {
  clearInterval(timer);
  clearTimeout(toastTimer);
});
</script>

<template>
  <div class="shell">
    <aside class="sidebar">
      <a class="brand" href="#" @click.prevent="page = 'overview'"
        ><span class="brand-symbol">p<span>.</span></span>
        <div>Pilot<span class="brand-sub">Lighten012-Pilot</span></div></a
      >
      <div class="workspace">
        <span class="workspace-icon"><Server :size="17" /></span>
        <div>
          <b>我的软路由</b
          ><small>{{ health?.hostname || "Debian 开发环境" }}</small>
        </div>
        <span class="env-label">DEV</span>
      </div>
      <p class="nav-caption">工作空间</p>
      <nav aria-label="主导航">
        <button
          v-for="n in navigation"
          :key="n.id"
          :class="['nav-item', { active: page === n.id }]"
          @click="page = n.id"
        >
          <component :is="n.icon" :size="19" />{{ n.label
          }}<span v-if="n.id === 'features'" class="count">8</span>
        </button>
      </nav>

      <div class="sidebar-bottom">
        <div class="build-card">
          <span class="tiny-dot"></span>从一个想法开始<b
            >构建属于自己的路由系统</b
          ><button @click="page = 'features'">
            查看功能路线 <ArrowUpRight :size="15" />
          </button>
        </div>
        <div class="version">
          <span>LIGHTEN012-PILOT</span><span>v0.6.0 · 开发版</span>
        </div>
      </div>
    </aside>
    <div class="main-shell">
      <header class="topbar">
        <div class="breadcrumb">
          工作空间 <ChevronRight :size="13" /><span>{{
            navigation.find((n) => n.id === page)?.label
          }}</span>
        </div>
        <div class="top-actions">
          <span :class="['connection', { bad: error }]"
            ><i></i
            >{{ error ? "连接异常" : sample ? "监控已连接" : "正在连接" }}</span
          ><button
            class="icon-button"
            aria-label="关于此版本"
            @click="notify('v0.6.0 · 已加入端口转发与系统日志。')"
          >
            <CircleHelp :size="19" /></button
          ><span class="avatar">P</span>
        </div>
      </header>
      <main>
        <div class="page-heading">
          <div>
            <div class="eyebrow">
              {{
                page === "features"
                  ? "BUILD YOUR NETWORK"
                  : "YOUR NETWORK, AT A GLANCE"
              }}
            </div>
            <h1>{{ title }}</h1>
            <p>
              {{
                page === "features"
                  ? "每一个模块，都是通往完整路由系统的一步。"
                  : "了解系统运行状态，让每一个连接都心中有数。"
              }}
            </p>
          </div>
          <button class="button secondary" :disabled="loading" @click="refresh">
            <RefreshCw :size="15" :class="{ spinning: loading }" />{{
              loading ? "刷新中" : "刷新状态"
            }}
          </button>
        </div>
        <div v-if="error" class="notice error" role="alert">
          {{ error }}。{{
            sample
              ? "下方保留上次成功采样，请勿视为实时数据。"
              : "页面暂不显示任何虚构数据。"
          }}<button @click="refresh">重试</button>
        </div>
        <NetworkPage v-if="page === 'network'" />
        <ServicesPage v-else-if="page === 'services'" />
        <FirewallPage
          v-else-if="page === 'firewall' || page === 'forward'"
          :key="page"
          :forwarding="page === 'forward'"
        />
        <LogsPage v-else-if="page === 'logs'" />
        <BackupPage v-else-if="page === 'backup'" />
        <template v-else-if="page !== 'features'">
          <section class="hero-grid">
            <div class="network-hero">
              <div class="hero-top">
                <span><span class="tiny-dot"></span>系统观测</span
                ><span class="hero-tag">只读监控</span>
              </div>
              <h2>
                {{
                  sample ? "你的系统，正在这里运行。" : "等待与你的系统连接。"
                }}
              </h2>
              <p>
                真实采样 · 2 秒更新 · Linux /
                {{ health?.architecture || "x86_64" }}
              </p>
              <div class="topology">
                <div>
                  <span class="topology-icon"><Globe :size="23" /></span
                  ><small>上游网络</small>
                </div>
                <div class="connection-line"><i></i><span>待配置</span></div>
                <div>
                  <span class="topology-icon central"
                    ><Server :size="25" /></span
                  ><small>Lighten012-Pilot 主机</small>
                </div>
                <div class="connection-line"><i></i><span>待配置</span></div>
                <div>
                  <span class="topology-icon"><Wifi :size="23" /></span
                  ><small>局域网络</small>
                </div>
              </div>
              <div class="hero-foot">
                <span>拓扑为规划示意，实际转发状态见防火墙与 NAT</span
                ><ArrowUpRight :size="17" />
              </div>
            </div>
            <div class="panel host-panel">
              <div class="panel-heading">
                <h3>运行环境</h3>
                <span class="small-badge">开发节点</span>
              </div>
              <div class="host-heading">
                <span class="host-icon"><Server :size="23" /></span>
                <div>
                  <h2>{{ health?.hostname || "等待连接" }}</h2>
                  <p>
                    {{
                      health
                        ? `${health.os} / ${health.architecture}`
                        : "Linux 系统"
                    }}
                  </p>
                </div>
              </div>
              <dl>
                <div>
                  <dt>管理地址</dt>
                  <dd>
                    {{
                      sample?.interfaces
                        .flatMap((i) => i.addresses)
                        .find((a) => !a.includes(":"))
                        ?.split("/")[0] || "—"
                    }}
                  </dd>
                </div>
                <div>
                  <dt>持续运行</dt>
                  <dd>{{ sample ? uptime(sample.uptime) : "—" }}</dd>
                </div>
                <div>
                  <dt>系统负载 · 1 / 5 / 15 分钟</dt>
                  <dd>{{ sample?.load || "—" }}</dd>
                </div>
                <div>
                  <dt>最近采样</dt>
                  <dd>
                    {{
                      sample
                        ? new Date(sample.time).toLocaleTimeString("zh-CN", {
                            hour12: false,
                          })
                        : "—"
                    }}
                  </dd>
                </div>
              </dl>
            </div>
          </section>
          <section class="metrics">
            <div class="metric panel">
              <div class="metric-label">
                <span>CPU 使用率</span><Cpu :size="18" />
              </div>
              <div class="metric-value">
                {{ sample ? sample.cpu.toFixed(1) : "—" }}<small>%</small>
              </div>
              <div class="meter">
                <i :style="{ width: (sample?.cpu || 0) + '%' }"></i>
              </div>
              <p>系统所有逻辑核心的平均占用</p>
            </div>
            <div class="metric panel">
              <div class="metric-label">
                <span>内存使用</span><HardDrive :size="18" />
              </div>
              <div class="metric-value">
                {{ sample ? memory.toFixed(1) : "—" }}<small>%</small>
              </div>
              <div class="meter blue">
                <i :style="{ width: memory + '%' }"></i>
              </div>
              <p>
                {{
                  sample
                    ? `${bytes(sample.memoryUsed)} / ${bytes(sample.memoryTotal)} · 可用内存口径`
                    : "等待内存采样"
                }}
              </p>
            </div>
            <div class="metric panel">
              <div class="metric-label">
                <span>接口总接收速率</span><ArrowDownLeft :size="18" />
              </div>
              <div class="metric-value rate">
                {{ current ? bytes(current.rx) : "—" }}<small>/s</small>
              </div>
              <p class="metric-bottom">
                <span class="legend-dot"></span>非回环接口合计 · 非外网测速
              </p>
            </div>
            <div class="metric panel">
              <div class="metric-label">
                <span>接口总发送速率</span><ArrowUpRight :size="18" />
              </div>
              <div class="metric-value rate">
                {{ current ? bytes(current.tx) : "—" }}<small>/s</small>
              </div>
              <p class="metric-bottom">
                <span class="legend-dot blue-dot"></span
                >{{ online }} 个接口链路处于 UP 状态
              </p>
            </div>
          </section>
          <section class="panel chart-panel">
            <div class="panel-heading">
              <div>
                <h3>
                  {{ page === "monitor" ? "CPU 使用率趋势" : "接口流量趋势" }}
                </h3>
                <p>
                  {{
                    page === "monitor"
                      ? "采样间隔内的处理器平均使用率"
                      : "所有非回环接口收发速率的合计；多接口转发可能重复计数"
                  }}
                </p>
              </div>
              <div class="chart-controls">
                <button class="pause" @click="paused = !paused">
                  {{ paused ? "继续更新" : "暂停更新" }}</button
                ><select aria-label="趋势时间范围" v-model="range">
                  <option :value="60">最近 2 分钟</option>
                  <option :value="120">最近 4 分钟</option>
                </select>
              </div>
            </div>
            <div class="chart-legends">
              <span
                ><i class="legend-dot"></i
                >{{ page === "monitor" ? "CPU 使用率" : "接收" }}</span
              ><span v-if="page !== 'monitor'"
                ><i class="legend-dot blue-dot"></i>发送</span
              ><span class="history-note">{{
                paused ? "页面更新已暂停" : "从服务启动后开始记录"
              }}</span>
            </div>
            <div class="chart">
              <div class="y-labels">
                <span>{{
                  page === "monitor" ? "100%" : bytes(chartMax) + "/s"
                }}</span
                ><span>{{
                  page === "monitor" ? "50%" : bytes(chartMax / 2) + "/s"
                }}</span
                ><span>0</span>
              </div>
              <div class="chart-canvas">
                <svg
                  viewBox="0 0 900 170"
                  preserveAspectRatio="none"
                  role="img"
                  :aria-label="
                    page === 'monitor'
                      ? '真实 CPU 历史趋势'
                      : '真实接口收发速率趋势'
                  "
                >
                  <defs>
                    <linearGradient id="fill" x1="0" y1="0" x2="0" y2="1">
                      <stop stop-color="#26a879" stop-opacity=".16" />
                      <stop offset="1" stop-color="#26a879" stop-opacity="0" />
                    </linearGradient>
                  </defs>
                  <path
                    d="M0 20H900 M0 85H900 M0 150H900"
                    stroke="#e7ece9"
                    stroke-dasharray="4 5"
                    fill="none"
                  />
                  <polygon
                    v-if="points.length > 1"
                    :points="`0,150 ${line(page === 'monitor' ? 'cpu' : 'rx')} 900,150`"
                    fill="url(#fill)"
                  />
                  <polyline
                    v-if="points.length > 1"
                    :points="line(page === 'monitor' ? 'cpu' : 'rx')"
                    fill="none"
                    stroke="#239b72"
                    stroke-width="2.5"
                    vector-effect="non-scaling-stroke"
                  />
                  <polyline
                    v-if="page !== 'monitor' && points.length > 1"
                    :points="line('tx')"
                    fill="none"
                    stroke="#7b9cc2"
                    stroke-width="2"
                    vector-effect="non-scaling-stroke"
                  />
                </svg>
                <div v-if="points.length < 2" class="chart-empty">
                  等待至少两个真实采样点…
                </div>
                <div class="x-labels">
                  <span>{{
                    points[0]
                      ? new Date(points[0].time).toLocaleTimeString("zh-CN", {
                          hour12: false,
                        })
                      : "—"
                  }}</span
                  ><span>{{
                    points.at(-1)
                      ? new Date(points.at(-1)!.time).toLocaleTimeString(
                          "zh-CN",
                          { hour12: false },
                        )
                      : "—"
                  }}</span>
                </div>
              </div>
            </div>
          </section>
          <section class="bottom-grid">
            <div class="panel interface-panel">
              <div class="panel-heading">
                <h3>
                  网络接口
                  <span class="number-badge">{{
                    sample?.interfaces.length || 0
                  }}</span>
                </h3>
                <span class="subtle">自动发现 · 角色配置见 WAN / LAN</span>
              </div>
              <div class="table-scroll">
                <table>
                  <thead>
                    <tr>
                      <th>接口 / 地址</th>
                      <th>链路状态</th>
                      <th>接收 / 发送</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="n in sample?.interfaces" :key="n.name">
                      <td>
                        <div class="interface-name">
                          <Network :size="17" /><b>{{ n.name }}</b>
                        </div>
                        <small
                          class="address"
                          v-for="a in n.addresses"
                          :key="a"
                          >{{ a }}</small
                        ><small v-if="!n.addresses.length">无地址</small>
                      </td>
                      <td>
                        <span :class="['status-pill', { up: n.state === 'up' }]"
                          ><i></i>{{ n.state.toUpperCase() }}</span
                        >
                      </td>
                      <td>
                        <span class="traffic-pair"
                          >↓ {{ bytes(n.rxRate) }}/s</span
                        ><small>↑ {{ bytes(n.txRate) }}/s</small>
                      </td>
                    </tr>
                    <tr v-if="!sample?.interfaces.length">
                      <td colspan="3" class="empty-row">
                        {{ sample ? "未发现非回环网络接口" : "等待接口信息" }}
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
            <div class="panel next-panel">
              <span class="section-kicker">基础功能已就绪</span>
              <div class="next-icon"><Activity :size="22" /></div>
              <h3>网络，自己掌控。</h3>
              <p>从状态观察到配置管理。<br />调整前先留一份配置备份。</p>
              <div class="implemented">
                <Check :size="15" /> 八项基础模块已完成
              </div>
              <button class="button secondary" @click="page = 'features'">
                查看功能范围 <ArrowRight :size="15" />
              </button>
            </div>
          </section>
          <div v-if="sample?.warnings.length" class="notice">
            {{ sample.warnings.join("；") }}
          </div>
        </template>
        <template v-else
          ><div class="roadmap-banner">
            <div>
              <span class="tiny-dot"></span>当前阶段 · 06
              <h2>从观察，走向管理。</h2>
              <p>八项基础模块已完成，配置支持备份、预览恢复与自动回滚。</p>
            </div>
            <span class="roadmap-count">08 <small>/ 08</small></span>
          </div>
          <div class="feature-grid">
            <button
              v-for="f in features"
              :key="f.id"
              :class="[
                'panel feature-card',
                { chosen: implemented.includes(f.id) },
              ]"
              @click="detail = f"
            >
              <div class="feature-top">
                <span class="feature-group">{{ f.group }}</span
                ><span
                  :class="[
                    'small-badge',
                    { green: implemented.includes(f.id) },
                  ]"
                  >{{ implemented.includes(f.id) ? "已实现" : "规划中" }}</span
                >
              </div>
              <h3>{{ f.name }}<ArrowUpRight :size="18" /></h3>
              <p>{{ f.desc }}</p>
              <div class="feature-bottom">
                <span>{{ f.level }}模块</span
                ><span>查看范围 <ArrowRight :size="14" /></span>
              </div>
            </button></div
        ></template>
        <footer>
          <span
            ><span class="tiny-dot"></span> LIGHTEN012-PILOT ·
            为自己的网络而构建</span
          ><span>Go + Vue · 网络服务 v0.6</span>
        </footer>
      </main>
    </div>
    <div
      v-if="detail"
      class="modal-backdrop"
      @click.self="detail = null"
      @keydown.esc="detail = null"
    >
      <section
        class="modal panel"
        role="dialog"
        aria-modal="true"
        :aria-label="detail.name"
      >
        <button
          class="modal-close icon-button"
          aria-label="关闭"
          @click="detail = null"
        >
          <X :size="20" /></button
        ><span class="section-kicker"
          >{{ detail.group }} ·
          {{ implemented.includes(detail.id) ? "已实现" : "功能规划" }}</span
        >
        <h2>{{ detail.name }}</h2>
        <p>{{ detail.desc }}</p>
        <div class="scope">
          <b>实现范围</b>
          <p>{{ detail.scope }}</p>
        </div>
        <p class="subtle">
          {{
            implemented.includes(detail.id)
              ? "功能已实现，可从工作空间导航进入。"
              : "当前版本尚未实现该模块，不会修改系统配置。"
          }}
        </p>
        <button
          class="button primary"
          @click="
            implemented.includes(detail.id)
              ? ((page = featurePage(detail.id)), (detail = null))
              : (detail = null)
          "
        >
          {{ implemented.includes(detail.id) ? "打开功能" : "了解了"
          }}<ArrowRight :size="16" />
        </button>
      </section>
    </div>
    <div v-if="toast" class="toast" role="status">{{ toast }}</div>
  </div>
</template>
