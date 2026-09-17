<script setup lang="ts">
import { ref, computed, nextTick, onMounted, onUnmounted, watch } from "vue";
import { Globe, Network, Plus, Trash2, RefreshCw } from "lucide-vue-next";
import { networkRequest, type Port } from "../network-api";
interface RecordEntry {
  name: string;
  type: string;
  value: string;
  ttl: number;
}
interface Config {
  interface: string;
  address: string;
  dns: { enabled: boolean; upstream: string; records: RecordEntry[] };
  dhcp: { enabled: boolean; start: string; end: string; leaseMinutes: number };
}
interface State {
  config: Config;
  revision: string;
  lan: Port | null;
  dnsRunning: boolean;
  dhcpRunning: boolean;
  stats: { queries: number; local: number; forwarded: number; failed: number };
  leases: { address: string; mac: string; hostname: string; expires: number }[];
  event: string;
}
interface Plan {
  config: Config;
  revision: string;
  token: string;
  dhcpText: string;
}
const busy = ref(false),
  error = ref(""),
  success = ref(""),
  state = ref<State | null>(null),
  plan = ref<Plan | null>(null);
const config = ref<Config>({
  interface: "",
  address: "",
  dns: { enabled: false, upstream: "119.29.29.29", records: [] },
  dhcp: { enabled: false, start: "", end: "", leaseMinutes: 720 },
});
const savedConfig = ref("");
const dirty = computed(
  () =>
    savedConfig.value !== "" &&
    JSON.stringify(config.value) !== savedConfig.value,
);
let timer: ReturnType<typeof setInterval> | undefined,
  fetching = false,
  initialized = false;
watch(
  config,
  () => {
    plan.value = null;
    success.value = "";
  },
  { deep: true },
);
function failure(e: unknown) {
  error.value = e instanceof Error ? e.message : "操作失败";
}
async function refresh() {
  if (fetching || busy.value) return;
  fetching = true;
  try {
    const s = await networkRequest<State>("network/services/state");
    if (state.value?.revision !== s.revision) plan.value = null;
    state.value = s;
    if (!initialized) {
      config.value = structuredClone(s.config);
      savedConfig.value = JSON.stringify(s.config);
      initialized = true;
      syncLAN();
    }
  } catch (e) {
    failure(e);
  } finally {
    fetching = false;
  }
}
function syncLAN() {
  const lan = state.value?.lan;
  if (lan && !config.value.dns.enabled && !config.value.dhcp.enabled) {
    config.value.interface = lan.interface;
    config.value.address = lan.address;
    if (!config.value.dhcp.start && lan.address.endsWith("/24")) {
      const subnet = lan.address.split(".").slice(0, 3).join(".");
      config.value.dhcp.start = subnet + ".100";
      config.value.dhcp.end = subnet + ".200";
    }
  }
}
async function preview() {
  busy.value = true;
  error.value = "";
  success.value = "";
  try {
    syncLAN();
    plan.value = await networkRequest<Plan>(
      "network/services/preview",
      config.value,
    );
  } catch (e) {
    failure(e);
  } finally {
    busy.value = false;
  }
}
async function apply(usePreview = false) {
  if (busy.value || !state.value) return;
  busy.value = true;
  error.value = "";
  success.value = "";
  try {
    syncLAN();
    const selected =
      usePreview && plan.value
        ? plan.value
        : await networkRequest<Plan>("network/services/preview", config.value);
    await networkRequest("network/services/apply", {
      config: selected.config,
      revision: selected.revision,
      token: selected.token,
    });
    plan.value = null;
    config.value = JSON.parse(JSON.stringify(selected.config)) as Config;
    savedConfig.value = JSON.stringify(selected.config);
    await nextTick();
    success.value = "配置已保存并应用";
  } catch (e) {
    failure(e);
  } finally {
    busy.value = false;
  }
  await refresh();
}
onMounted(() => {
  refresh();
  timer = setInterval(refresh, 5000);
});
onUnmounted(() => clearInterval(timer));
</script>

<template>
  <section class="services-page">
    <div v-if="error" class="notice error" role="alert">{{ error }}</div>
    <div class="services-intro">
      <div>
        <span class="section-kicker">ADDRESS & RESOLUTION</span>
        <h2>让设备找到地址，也找到彼此。</h2>
        <p>自定义解析优先，未命中的域名交给上游 DNS。</p>
      </div>
    </div>
    <div class="service-summary panel">
      <span
        ><Network :size="18" /> LAN ·
        <b>{{ state?.lan?.interface || "未配置" }}</b>
        {{ state?.lan?.address || "请先确认 WAN/LAN 配置" }}</span
      >
      <div>
        <span :class="['status-pill', { up: state?.dnsRunning }]"
          >DNS {{ state?.dnsRunning ? "运行中" : "已停止" }}</span
        ><span :class="['status-pill', { up: state?.dhcpRunning }]"
          >DHCP {{ state?.dhcpRunning ? "运行中" : "已停止" }}</span
        >
      </div>
    </div>
    <fieldset :disabled="busy" class="service-fields">
      <section class="panel service-card">
        <div class="panel-heading">
          <div class="service-heading">
            <Globe :size="22" />
            <div>
              <h3>DNS 解析</h3>
              <p>由 Pilot Go 服务提供 · UDP / TCP 53</p>
            </div>
          </div>
          <label class="toggle-label"
            ><input type="checkbox" v-model="config.dns.enabled" />启用
            DNS</label
          >
        </div>
        <div class="dns-route">
          <span>设备查询</span><b>→</b><span>本地记录</span><b>→</b
          ><span>未命中转发上游</span>
        </div>
        <div class="service-form-grid">
          <label
            >上游 DNS<input
              v-model="config.dns.upstream"
              placeholder="119.29.29.29"
          /></label>
          <div class="field-note">
            上游使用标准 53 端口。超时会返回解析失败；本版不缓存上游答案。
          </div>
        </div>
        <div class="records-heading">
          <div>
            <h4>
              自定义解析记录
              <span class="number-badge">{{ config.dns.records.length }}</span>
            </h4>
            <p>精确域名匹配，忽略大小写；支持同名多条地址。</p>
          </div>
          <button
            type="button"
            class="button secondary"
            :disabled="config.dns.records.length >= 128"
            @click="
              config.dns.records.push({
                name: '',
                type: 'A',
                value: '',
                ttl: 60,
              })
            "
          >
            <Plus :size="15" />添加记录
          </button>
        </div>
        <div v-if="!config.dns.records.length" class="service-empty">
          还没有本地记录。添加后，例如 lighten012.home →
          192.168.1.1，将直接返回你指定的地址。
        </div>
        <div
          v-for="(record, i) in config.dns.records"
          :key="i"
          class="record-row"
        >
          <label
            >域名<input
              v-model="record.name"
              :aria-label="`记录 ${i + 1} 域名`"
              placeholder="lighten012.home"
          /></label>
          <label
            >类型<select
              v-model="record.type"
              :aria-label="`记录 ${i + 1} 类型`"
            >
              <option>A</option>
              <option>AAAA</option>
            </select></label
          >
          <label
            >解析地址<input
              v-model="record.value"
              :aria-label="`记录 ${i + 1} 地址`"
              :placeholder="record.type === 'A' ? '192.168.1.1' : 'fd00::1'"
          /></label>
          <label
            >TTL · 秒<input
              type="number"
              v-model.number="record.ttl"
              min="1"
              max="86400"
              :aria-label="`记录 ${i + 1} TTL`"
          /></label>
          <button
            type="button"
            class="icon-button delete-record"
            :aria-label="`删除记录 ${i + 1}`"
            @click="config.dns.records.splice(i, 1)"
          >
            <Trash2 :size="17" />
          </button>
        </div>
        <p class="service-hint">
          同一域名未配置的类型返回空答案，不再转发上游。自定义记录不会自动覆盖子域名。
        </p>
        <div class="service-action save-action">
          <div>
            <p role="status">
              {{
                dirty
                  ? "有未保存的修改，离开页面会丢失。"
                  : "配置已与服务器同步。"
              }}
            </p>
            <p>保存会同时应用本页的 DNS 与 DHCP 配置。</p>
          </div>
          <button
            type="button"
            class="button primary"
            :disabled="busy || !state"
            @click="apply()"
          >
            {{ busy ? "保存中…" : "保存并应用" }}
          </button>
        </div>
        <div v-if="success" class="notice" role="status">{{ success }}</div>
      </section>
      <section class="panel service-card">
        <div class="panel-heading">
          <div class="service-heading">
            <Network :size="22" />
            <div>
              <h3>DHCP 地址分配</h3>
              <p>仅向 LAN 接口的设备分配 IPv4 地址</p>
            </div>
          </div>
          <label class="toggle-label"
            ><input type="checkbox" v-model="config.dhcp.enabled" />启用
            DHCP</label
          >
        </div>
        <div class="service-form-grid three">
          <label
            >起始地址<input
              v-model="config.dhcp.start"
              placeholder="192.168.60.100" /></label
          ><label
            >结束地址<input
              v-model="config.dhcp.end"
              placeholder="192.168.60.200" /></label
          ><label
            >租期 · 分钟<input
              type="number"
              v-model.number="config.dhcp.leaseMinutes"
              min="2"
              max="10080"
          /></label>
        </div>
        <div class="dhcp-options">
          <span
            >网关 <b>{{ state?.lan?.address.split("/")[0] || "—" }}</b></span
          ><span
            >DNS <b>{{ state?.lan?.address.split("/")[0] || "—" }}</b></span
          >
        </div>
        <p class="service-hint">
          DHCP 需要同时启用 DNS。客户端获得 Pilot 作为网关与
          DNS；共享外网上网请在“防火墙与 NAT”中启用转发与出口 NAT。
          地址池内请勿手动配置其他设备的固定地址。
        </p>
      </section>
    </fieldset>
    <div class="service-action">
      <p>启用服务后，修改 WAN/LAN 前需先停用 DHCP 与 DNS。</p>
      <button
        class="button primary"
        :disabled="busy || !state"
        @click="preview"
      >
        校验并预览
      </button>
    </div>
    <section v-if="plan" class="panel service-preview">
      <h3>应用预览</h3>
      <p>接口 {{ plan.config.interface }} · {{ plan.config.address }}</p>
      <ul>
        <li>
          DNS：{{ plan.config.dns.enabled ? "启用" : "停用" }} · 上游
          {{ plan.config.dns.upstream }} ·
          {{ plan.config.dns.records.length }} 条本地记录
        </li>
        <li>
          DHCP：{{
            plan.config.dhcp.enabled
              ? `${plan.config.dhcp.start} – ${plan.config.dhcp.end}，${plan.config.dhcp.leaseMinutes} 分钟`
              : "停用"
          }}
        </li>
      </ul>
      <p>
        应用会短暂重启 DNS / DHCP 服务；启动失败将尝试恢复旧配置。已有客户端的
        DNS 缓存会按 TTL 到期。
      </p>
      <button class="button primary" :disabled="busy" @click="apply(true)">
        {{ busy ? "保存中…" : "确认保存并应用" }}
      </button>
    </section>
    <div class="service-counters">
      <div class="panel">
        <small>DNS 查询</small><b>{{ state?.stats.queries || 0 }}</b>
      </div>
      <div class="panel">
        <small>本地命中</small><b>{{ state?.stats.local || 0 }}</b>
      </div>
      <div class="panel">
        <small>转发上游</small><b>{{ state?.stats.forwarded || 0 }}</b>
      </div>
      <div class="panel">
        <small>转发失败 / 过载</small><b>{{ state?.stats.failed || 0 }}</b>
      </div>
    </div>
    <section class="panel service-card">
      <div class="panel-heading">
        <div>
          <h3>DHCP 租约</h3>
          <p>保留未过期租约 · 每 5 秒刷新</p>
        </div>
        <button
          class="icon-button"
          aria-label="刷新租约"
          :disabled="busy"
          @click="refresh"
        >
          <RefreshCw :size="17" />
        </button>
      </div>
      <div class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>设备</th>
              <th>IPv4 地址</th>
              <th>MAC 地址</th>
              <th>到期时间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="lease in state?.leases" :key="lease.mac + lease.address">
              <td>
                {{ lease.hostname === "*" ? "未提供主机名" : lease.hostname }}
              </td>
              <td>{{ lease.address }}</td>
              <td>{{ lease.mac }}</td>
              <td>
                {{
                  lease.expires
                    ? new Date(lease.expires).toLocaleString()
                    : "无限期"
                }}
              </td>
            </tr>
            <tr v-if="!state?.leases.length">
              <td colspan="4" class="service-empty">
                暂无有效租约，等待 LAN 设备请求地址。
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
    <p class="service-hint">
      {{ state?.event }} · DNS 统计在服务重新应用或重启后重新计数。
    </p>
  </section>
</template>

<style scoped>
.services-page {
  display: grid;
  gap: 20px;
}
.services-intro,
.service-summary,
.service-summary > div,
.service-heading,
.records-heading,
.service-action,
.dhcp-options {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}
.services-intro h2 {
  font-size: 25px;
  margin: 10px 0;
}
.services-intro p {
  color: #76857d;
  font-size: 13px;
  line-height: 1.8;
}
.service-summary {
  padding: 20px 24px;
  flex-wrap: wrap;
  font-size: 13px;
}
.service-summary > span {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.service-fields {
  border: 0;
  padding: 0;
  margin: 0;
  min-width: 0;
  display: grid;
  gap: 20px;
}
.service-card {
  padding: 26px;
  min-width: 0;
}
.service-heading {
  justify-content: flex-start;
}
.service-heading > svg {
  color: #299974;
}
.service-card h3 {
  margin: 0;
  font-size: 17px;
}
.service-card h4 {
  margin: 0;
  font-size: 14px;
}
.toggle-label {
  display: flex;
  align-items: center;
  gap: 9px;
  font-size: 13px;
  white-space: nowrap;
}
.toggle-label input {
  accent-color: #218a65;
  width: 17px;
  height: 17px;
}
.service-form-grid {
  display: grid;
  grid-template-columns: minmax(180px, 1fr) 2fr;
  gap: 20px;
  align-items: center;
  margin: 24px 0;
}
.service-form-grid.three {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.services-page label:not(.toggle-label) {
  display: grid;
  gap: 9px;
  font-size: 12px;
  color: #68776e;
}
.services-page input:not([type="checkbox"]),
.services-page select {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  border: 1px solid #dce5df;
  border-radius: 9px;
  background: #fff;
  padding: 11px 12px;
  font: inherit;
  font-size: 13px;
  color: #243f31;
  outline: none;
}
.services-page input:focus,
.services-page select:focus {
  border-color: #27976e;
  box-shadow: 0 0 0 3px #27976e15;
}
.dns-route {
  display: flex;
  align-items: center;
  gap: 15px;
  background: #f3f8f5;
  border-radius: 10px;
  padding: 16px;
  font-size: 12px;
  margin-top: 22px;
  flex-wrap: wrap;
}
.dns-route b {
  color: #56a38b;
}
.field-note,
.service-hint,
.service-preview p,
.service-action p {
  font-size: 12px;
  color: #7b8880;
  line-height: 1.8;
}
.records-heading {
  border-top: 1px solid #edf1ee;
  padding-top: 22px;
}
.records-heading p {
  font-size: 12px;
  color: #869189;
  margin-bottom: 0;
}
.record-row {
  display: grid;
  grid-template-columns: 2fr 90px 1.7fr 100px 30px;
  gap: 12px;
  align-items: end;
  margin-top: 18px;
}
.delete-record {
  margin-bottom: 5px;
  color: #b16663;
}
.service-empty {
  padding: 28px 12px;
  color: #859187;
  font-size: 12px;
  text-align: center;
  line-height: 1.9;
}
.dhcp-options {
  justify-content: flex-start;
  gap: 28px;
  padding: 16px;
  background: #f6f8f6;
  border-radius: 9px;
  font-size: 12px;
  color: #738176;
  flex-wrap: wrap;
}
.dhcp-options b {
  color: #3d5143;
  margin-left: 12px;
}
.service-counters {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 15px;
}
.service-counters > div {
  padding: 20px 24px;
  display: grid;
  gap: 10px;
}
.service-counters small {
  color: #7a897e;
  font-size: 12px;
}
.service-counters b {
  font-size: 26px;
  font-weight: 550;
}
.service-preview {
  padding: 24px;
  border-color: #a5d4bf;
}
.service-preview h3 {
  margin-top: 0;
}
.service-preview ul {
  font-size: 13px;
  line-height: 2;
  color: #456451;
  padding-left: 20px;
}
.service-card table {
  width: 100%;
  white-space: nowrap;
}
.service-card td,
.service-card th {
  padding: 14px 10px;
  font-size: 12px;
}
.service-action {
  flex-wrap: wrap;
}
.save-action {
  margin-top: 20px;
  border-top: 1px solid #edf1ee;
  padding-top: 16px;
}
.save-action p {
  margin: 4px 0;
}
.services-page :disabled {
  opacity: 0.6;
}
@media (max-width: 1000px) {
  .record-row {
    grid-template-columns: 2fr 85px 2fr;
  }
  .record-row label:nth-child(4) {
    grid-column: 1/2;
  }
  .service-summary > div {
    flex-wrap: wrap;
  }
  .service-form-grid.three {
    grid-template-columns: 1fr 1fr;
  }
  .service-counters {
    grid-template-columns: repeat(2, 1fr);
  }
}
@media (max-width: 600px) {
  .service-card,
  .service-summary {
    padding: 18px;
  }
  .services-intro {
    align-items: flex-start;
  }
  .services-intro h2 {
    font-size: 20px;
  }
  .services-intro > .button {
    padding: 9px;
    white-space: nowrap;
  }
  .service-card .panel-heading,
  .records-heading {
    flex-wrap: wrap;
  }
  .record-row {
    grid-template-columns: minmax(0, 1fr) 85px;
  }
  .record-row label:nth-child(3) {
    grid-column: 1/-1;
  }
  .record-row label:nth-child(4) {
    grid-column: 1/2;
  }
  .service-form-grid,
  .service-form-grid.three {
    grid-template-columns: 1fr;
    gap: 15px;
  }
  .service-summary > span {
    font-size: 12px;
  }
  .service-counters > div {
    padding: 16px;
  }
  .dns-route {
    gap: 8px;
  }
  .service-action .button {
    width: 100%;
  }
}
</style>
