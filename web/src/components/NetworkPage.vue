<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import {
  ArrowRight,
  Check,
  CheckCircle2,
  Clock3,
  Globe,
  LockKeyhole,
  Network,
  RefreshCw,
  Save,
  ShieldCheck,
  Undo2,
} from "lucide-vue-next";
import {
  networkRequest,
  type NetworkConfig,
  type NetworkState,
  type NetworkPlan,
  type Port,
} from "../network-api";
const state = ref<NetworkState | null>(null),
  error = ref(""),
  message = ref(""),
  busy = ref(false),
  polling = ref(false),
  preview = ref<NetworkPlan | null>(null),
  ack = ref(false),
  now = ref(Date.now()),
  offset = ref(0);
const form = ref<NetworkConfig>({
  wan: { interface: "", mode: "dhcp", address: "", gateway: "" },
  lan: {
    interface: "",
    mode: "static",
    address: "192.168.60.1/24",
    gateway: "",
  },
});
const wanDevice = computed(() =>
  state.value?.inventory.devices.find(
    (d) => d.name === form.value.wan.interface,
  ),
);
const lanDevice = computed(() =>
  state.value?.inventory.devices.find(
    (d) => d.name === form.value.lan.interface,
  ),
);
const canEdit = computed(() => !busy.value && !state.value?.pending);
const missing = computed(
  () => !state.value || state.value.inventory.devices.length < 2,
);
const seconds = computed(() =>
  Math.max(
    0,
    Math.ceil(
      ((state.value?.pending?.deadline || 0) - now.value - offset.value) / 1000,
    ),
  ),
);
const phases: Record<string, string> = {
  applying: "正在应用",
  awaiting_confirmation: "等待确认",
  rolling_back: "正在恢复",
  rollback_failed: "恢复未完成，后台正在重试",
};
let initialized = false,
  timer: ReturnType<typeof setInterval> | undefined,
  clock: ReturnType<typeof setInterval> | undefined;
watch(
  form,
  () => {
    preview.value = null;
    ack.value = false;
  },
  { deep: true },
);
function fail(e: unknown) {
  error.value = e instanceof Error ? e.message : "操作失败";
}
async function sync() {
  if (polling.value) return;
  polling.value = true;
  try {
    const s = await networkRequest<NetworkState>("network/state");
    state.value = s;
    if (preview.value && preview.value.revision !== s.inventory.revision) {
      preview.value = null;
      ack.value = false;
    }
    offset.value = s.serverTime - Date.now();
    if (!initialized) {
      const suggested = s.draft || s.saved;
      if (suggested) form.value = structuredClone(suggested);
      else {
        const w =
          s.inventory.devices.find((d) => d.protected) ||
          s.inventory.devices.find((d) =>
            s.inventory.routes.some(
              (r) => r.dst === "default" && r.dev === d.name,
            ),
          ) ||
          s.inventory.devices[0];
        if (w)
          form.value.wan = w.current
            ? structuredClone(w.current)
            : { interface: w.name, mode: "dhcp", address: "", gateway: "" };
        const l = s.inventory.devices.find(
          (d) => d.name !== w?.name && !d.reason && !d.protected,
        );
        if (l)
          form.value.lan = {
            interface: l.name,
            mode: "static",
            address: "192.168.60.1/24",
            gateway: "",
          };
      }
      initialized = true;
    }
  } catch (e) {
    fail(e);
  } finally {
    polling.value = false;
  }
}
function changePort(role: "wan" | "lan") {
  const d = state.value?.inventory.devices.find(
    (d) => d.name === form.value[role].interface,
  );
  if (role === "wan" && d?.current) form.value.wan = structuredClone(d.current);
  if (role === "lan" && d?.protected && d.current)
    form.value.lan = structuredClone(d.current);
}
function modeChanged() {
  if (form.value.wan.mode === "dhcp") {
    form.value.wan.address = "";
    form.value.wan.gateway = "";
  }
}
async function act(
  action: "preview" | "draft" | "apply" | "confirm" | "rollback",
) {
  busy.value = true;
  error.value = "";
  message.value = "";
  try {
    if (action === "preview") {
      preview.value = await networkRequest<NetworkPlan>(
        "network/preview",
        form.value,
      );
      ack.value = false;
    }
    if (action === "draft") {
      await networkRequest("network/draft", form.value);
      message.value = "草稿已保存，系统网络尚未改变。";
    }
    if (action === "apply" && preview.value) {
      await networkRequest("network/apply", {
        config: preview.value.config,
        revision: preview.value.revision,
        token: preview.value.token,
      });
      preview.value = null;
      message.value = "已提交配置，请等待应用结果。";
    }
    if (
      (action === "confirm" || action === "rollback") &&
      state.value?.pending
    ) {
      await networkRequest("network/" + action, { id: state.value.pending.id });
      message.value =
        action === "confirm" ? "配置已确认保存。" : "已恢复应用前的网络配置。";
    }
    await sync();
  } catch (e) {
    fail(e);
    if (action === "apply") preview.value = null;
  } finally {
    busy.value = false;
  }
}
function describe(p: Port | null) {
  if (!p) return "未配置 IPv4";
  return p.mode === "dhcp"
    ? "DHCP 自动获取"
    : p.address + (p.gateway ? " · 网关 " + p.gateway : "");
}
onMounted(() => {
  sync();
  timer = setInterval(() => {
    if (!busy.value) sync();
  }, 3000);
  clock = setInterval(() => (now.value = Date.now()), 500);
});
onUnmounted(() => {
  clearInterval(timer);
  clearInterval(clock);
});
</script>

<template>
  <section class="network-page">
    <div v-if="error" class="notice error" role="alert">
      {{ error }}<button @click="error = ''">关闭</button>
    </div>
    <div v-if="message" class="network-success" role="status">
      <CheckCircle2 :size="17" />{{ message }}
    </div>
    <div class="network-intro">
      <div>
        <span class="section-kicker">NETWORK INTERFACES</span>
        <h2>连接上游，定义内网。</h2>
        <p>为每张网卡分配角色，从这里开始建立你的网络。</p>
      </div>
    </div>
    <div v-if="state?.inventory.blocked" class="notice" role="alert">
      {{ state.inventory.blocked }}
    </div>
    <div v-if="missing" class="notice">
      需要至少两张网卡才能配置 WAN / LAN。请在 VirtualBox
      中添加第二张接入内部网络的网卡，然后刷新。
    </div>
    <div v-if="state?.pending" class="pending-banner" role="status">
      <div class="pending-clock">
        <Clock3 :size="24" /><strong>{{ seconds }}<small>秒</small></strong>
      </div>
      <div class="pending-copy">
        <h3>{{ phases[state.pending.phase] || state.pending.phase }}</h3>
        <p>确认 LAN 地址与连接正常后保留配置。关闭页面不会取消后台回滚。</p>
        <small v-if="seconds === 0">后台正在完成当前操作或恢复，请稍候。</small>
      </div>
      <div class="pending-actions">
        <button
          class="button primary"
          :disabled="
            busy ||
            state.pending.phase !== 'awaiting_confirmation' ||
            seconds === 0
          "
          @click="act('confirm')"
        >
          <Check :size="16" />确认保留配置</button
        ><button
          class="button secondary"
          :disabled="
            busy ||
            state.pending.phase === 'applying' ||
            state.pending.phase === 'rolling_back'
          "
          @click="act('rollback')"
        >
          <Undo2 :size="15" />立即恢复
        </button>
      </div>
    </div>
    <div class="network-devices">
      <div
        v-for="d in state?.inventory.devices"
        :key="d.name"
        class="panel discovered-device"
      >
        <div>
          <Network :size="18" /><strong>{{ d.name }}</strong
          ><span :class="['status-pill', { up: d.state === 'up' }]">{{
            d.state.toUpperCase() || "UNKNOWN"
          }}</span>
        </div>
        <p>
          {{
            d.addresses.filter((a) => !a.includes(":")).join(" · ") ||
            "尚无 IPv4 地址"
          }}
        </p>
        <small v-if="d.protected"
          ><LockKeyhole :size="12" />当前管理接口 · 保留配置</small
        ><small v-else-if="d.reason" class="device-warning">{{
          d.reason
        }}</small
        ><small v-else>可分配为 WAN 或 LAN</small>
      </div>
    </div>
    <form @submit.prevent="act('preview')">
      <fieldset :disabled="!canEdit" class="network-fields">
        <section class="panel port-card">
          <div class="port-heading">
            <span class="port-icon"><Globe :size="23" /></span>
            <div>
              <h3>WAN <span>上游网络</span></h3>
              <p>连接现有路由器或互联网入口</p>
            </div>
            <span class="port-label">UPLINK</span>
          </div>
          <label for="wan-interface">网络接口</label
          ><select
            id="wan-interface"
            v-model="form.wan.interface"
            @change="changePort('wan')"
            required
          >
            <option disabled value="">选择 WAN 网卡</option>
            <option
              v-for="d in state?.inventory.devices"
              :key="d.name"
              :value="d.name"
              :disabled="!!d.reason || d.name === form.lan.interface"
            >
              {{ d.name }}{{ d.protected ? " · 管理接口" : "" }}
            </option></select
          ><label for="wan-mode">IPv4 地址方式</label
          ><select
            id="wan-mode"
            v-model="form.wan.mode"
            @change="modeChanged"
            :disabled="wanDevice?.protected"
          >
            <option value="dhcp">DHCP · 自动获取</option>
            <option value="static">静态 IPv4 · 手动指定</option></select
          ><template v-if="form.wan.mode === 'static'"
            ><label for="wan-address">IPv4 地址 / 前缀</label
            ><input
              id="wan-address"
              v-model="form.wan.address"
              placeholder="例如 192.168.50.10/24"
              required
              :disabled="wanDevice?.protected" /><label for="wan-gateway"
              >默认网关</label
            ><input
              id="wan-gateway"
              v-model="form.wan.gateway"
              placeholder="例如 192.168.50.1"
              required
              :disabled="wanDevice?.protected"
          /></template>
          <div v-else class="mode-explainer">
            <RefreshCw :size="18" />
            <p>
              由上游 DHCP 服务分配地址和默认网关。<br />当前地址：{{
                wanDevice?.addresses
                  .filter((a) => !a.includes(":"))
                  .join(" · ") || "尚未获取"
              }}
            </p>
          </div>
          <p class="field-note" v-if="wanDevice?.protected">
            <LockKeyhole
              :size="14"
            />该接口承载当前管理连接，本次保留其现有配置。
          </p>
          <p class="field-note" v-else>
            本版本仅配置地址与网关，DNS 在后续模块中管理。
          </p>
        </section>
        <section class="panel port-card">
          <div class="port-heading">
            <span class="port-icon lan"><Network :size="23" /></span>
            <div>
              <h3>LAN <span>内部网络</span></h3>
              <p>为下游设备提供一个稳定的网关地址</p>
            </div>
            <span class="port-label">LOCAL</span>
          </div>
          <label for="lan-interface">网络接口</label
          ><select
            id="lan-interface"
            v-model="form.lan.interface"
            @change="changePort('lan')"
            required
          >
            <option disabled value="">选择 LAN 网卡</option>
            <option
              v-for="d in state?.inventory.devices"
              :key="d.name"
              :value="d.name"
              :disabled="
                !!d.reason || d.protected || d.name === form.wan.interface
              "
            >
              {{ d.name }}
            </option></select
          ><label for="lan-address">路由器 LAN 地址 / 前缀</label
          ><input
            id="lan-address"
            v-model="form.lan.address"
            placeholder="例如 192.168.60.1/24"
            required
            :disabled="lanDevice?.protected"
          />
          <div class="lan-example">
            <div>
              <span class="example-node router">Pilot</span
              ><span class="example-line"></span
              ><span class="example-node">内网设备</span>
            </div>
            <p>
              {{ form.lan.address || "设置 LAN 地址" }}<span>同一网段</span>
            </p>
          </div>
          <p class="field-note">
            LAN 使用静态 IPv4，不设置默认网关。DHCP 地址分配、转发与 NAT
            将在后续模块实现。
          </p>
        </section>
      </fieldset>
      <div class="network-toolbar">
        <div>
          <ShieldCheck :size="18" /><span
            >先预览，再应用。未确认的变更会自动恢复。</span
          >
        </div>
        <div>
          <button
            type="button"
            class="button secondary"
            :disabled="!canEdit || missing || !!state?.inventory.blocked"
            @click="act('draft')"
          >
            <Save :size="15" />保存草稿</button
          ><button
            class="button primary"
            type="submit"
            :disabled="!canEdit || missing || !!state?.inventory.blocked"
          >
            校验并预览<ArrowRight :size="15" />
          </button>
        </div>
      </div>
    </form>
    <section v-if="preview" class="panel network-preview">
      <div class="panel-heading">
        <div>
          <span class="section-kicker">REVIEW CHANGES</span>
          <h3>确认这次变更</h3>
        </div>
        <span class="small-badge">尚未应用</span>
      </div>
      <div
        v-for="c in preview.changes"
        :key="c.interface"
        class="preview-change"
      >
        <strong
          >{{ c.interface
          }}<span>{{ c.changed ? "将更新" : "保持不变" }}</span></strong
        >
        <div>
          <p><small>当前</small>{{ describe(c.before) }}</p>
          <ArrowRight :size="17" />
          <p><small>应用后</small>{{ describe(c.after) }}</p>
        </div>
      </div>
      <ul>
        <li v-for="w in preview.warnings" :key="w">{{ w }}</li>
      </ul>
      <label class="ack-label"
        ><input
          type="checkbox"
          v-model="ack"
        />我已检查变更，并将在应用后确认连接；超时将恢复旧配置。</label
      ><button
        class="button primary"
        :disabled="!ack || busy || !!state?.pending"
        @click="act('apply')"
      >
        临时应用 · 90 秒确认<ArrowRight :size="15" />
      </button>
    </section>
    <div v-if="state?.event" class="network-event">
      <Clock3 :size="14" />{{ state.event }}
    </div>
  </section>
</template>

<style scoped>
.network-page {
  padding-bottom: 8px;
}
.network-success {
  display: flex;
  align-items: center;
  gap: 9px;
  background: #eaf5ee;
  color: #337351;
  border: 1px solid #d5e9da;
  border-radius: 8px;
  padding: 14px 17px;
  margin-bottom: 20px;
  font-size: 13px;
}
.network-intro {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 25px;
  gap: 20px;
}
.network-intro h2 {
  font-size: 23px;
  font-weight: 500;
  margin: 10px 0;
}
.network-intro p {
  font-size: 12px;
  color: #83957f;
}
.network-devices {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
  gap: 15px;
  margin-bottom: 22px;
}
.discovered-device {
  padding: 17px 20px;
}
.discovered-device > div {
  display: flex;
  align-items: center;
  gap: 10px;
}
.discovered-device strong {
  font-size: 14px;
}
.discovered-device .status-pill {
  margin-left: auto;
}
.discovered-device p {
  margin: 13px 0 9px;
  font-size: 12px;
  color: #66815c;
}
.discovered-device small {
  font-size: 10px;
  color: #99a48b;
  display: flex;
  align-items: center;
  gap: 5px;
}
.discovered-device small.device-warning {
  color: #b18047;
}
.network-fields {
  border: 0;
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 22px;
  min-width: 0;
}
.port-card {
  padding: 25px 28px;
  min-width: 0;
}
.port-heading {
  display: flex;
  gap: 13px;
  align-items: center;
  margin-bottom: 27px;
}
.port-icon {
  display: flex;
  padding: 13px;
  border-radius: 12px;
  background: #eaf2ed;
  color: #477a58;
}
.port-icon.lan {
  background: #eef1f6;
  color: #738faa;
}
.port-heading h3 {
  font-size: 19px;
  font-weight: 600;
}
.port-heading h3 span {
  font-size: 12px;
  font-weight: 400;
  color: #758b70;
  margin-left: 8px;
}
.port-heading p {
  font-size: 10px;
  color: #98a78d;
  margin-top: 7px;
}
.port-label {
  margin-left: auto;
  align-self: flex-start;
  font-size: 8px;
  letter-spacing: 1px;
  color: #a3ad98;
}
.network-page label:not(.ack-label) {
  display: block;
  font-size: 12px;
  color: #647b5b;
  margin: 19px 0 9px;
}
.network-page input:not([type="checkbox"]),
.network-page select {
  width: 100%;
  border: 1px solid #dfe7db;
  background: #fcfdfb;
  border-radius: 7px;
  padding: 12px 13px;
  font-size: 13px;
  color: #365335;
  font-family: inherit;
  min-width: 0;
}
.network-page input:disabled,
.network-page select:disabled {
  color: #8b9c83;
  background: #f3f6ef;
  cursor: not-allowed;
}
.network-page input:focus,
.network-page select:focus {
  outline: 2px solid #9cc6a2;
  outline-offset: 1px;
}
.mode-explainer {
  display: flex;
  gap: 12px;
  background: #f4f7f0;
  border-radius: 8px;
  padding: 19px 16px;
  margin-top: 23px;
  color: #87a179;
  align-items: flex-start;
}
.mode-explainer p {
  font-size: 11px;
  line-height: 1.9;
  color: #7d9470;
}
.field-note {
  display: flex;
  gap: 6px;
  align-items: flex-start;
  color: #99a58e;
  font-size: 11px;
  line-height: 1.8;
  margin-top: 20px;
}
.field-note svg {
  flex-shrink: 0;
  margin-top: 3px;
}
.lan-example {
  background: #f6f8f2;
  padding: 19px;
  margin-top: 23px;
  border-radius: 8px;
}
.lan-example > div {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.example-node {
  font-size: 11px;
  border: 1px solid #dce6d5;
  border-radius: 6px;
  padding: 8px 12px;
  background: white;
  color: #8b9e7a;
}
.example-node.router {
  background: #e6efde;
  color: #587f43;
  border-color: #d4e5c7;
}
.example-line {
  flex: 1;
  border-top: 1px dashed #bbceb0;
  margin: 0 15px;
}
.lan-example p {
  display: flex;
  justify-content: space-between;
  margin-top: 12px;
  font-size: 10px;
  color: #9aab8b;
}
.network-toolbar {
  display: flex;
  justify-content: space-between;
  gap: 20px;
  align-items: center;
  padding: 24px 0;
}
.network-toolbar > div {
  display: flex;
  gap: 11px;
  align-items: center;
}
.network-toolbar > div:first-child {
  font-size: 11px;
  color: #90a281;
}
.network-preview {
  padding: 28px;
  margin-top: 10px;
}
.network-preview h3 {
  font-size: 20px;
  margin-top: 10px;
  font-weight: 500;
}
.preview-change {
  border-bottom: 1px solid #edf1e8;
  padding: 22px 0;
}
.preview-change > strong {
  display: flex;
  gap: 10px;
  align-items: center;
  font-size: 13px;
}
.preview-change > strong span {
  font-size: 10px;
  font-weight: 400;
  color: #8da47d;
}
.preview-change > div {
  display: flex;
  align-items: center;
  gap: 24px;
  margin-top: 15px;
  color: #859b77;
}
.preview-change p {
  flex: 1;
  font-size: 12px;
  color: #5a7450;
  line-height: 1.8;
  overflow-wrap: anywhere;
}
.preview-change p small {
  display: block;
  font-size: 10px;
  color: #9ba98e;
}
.network-preview ul {
  font-size: 11px;
  color: #93a181;
  line-height: 2;
  padding-left: 17px;
  margin: 20px 0;
}
.ack-label {
  font-size: 12px;
  color: #718764;
  display: flex;
  gap: 9px;
  align-items: flex-start;
  margin: 22px 0;
}
.ack-label input {
  accent-color: #4a8653;
  margin-top: 2px;
}
.pending-banner {
  display: flex;
  gap: 20px;
  align-items: center;
  padding: 24px;
  border: 1px solid #d9e3b1;
  background: #f6f8e9;
  border-radius: 10px;
  margin: 20px 0;
}
.pending-clock {
  display: flex;
  gap: 12px;
  align-items: center;
  color: #8b9b4f;
}
.pending-clock strong {
  font-size: 30px;
  font-variant-numeric: tabular-nums;
}
.pending-clock small {
  font-size: 12px;
  margin-left: 5px;
}
.pending-copy {
  flex: 1;
}
.pending-copy h3 {
  font-size: 16px;
  color: #697c38;
}
.pending-copy p,
.pending-copy small {
  font-size: 11px;
  color: #919e6f;
  line-height: 1.9;
  margin-top: 7px;
}
.pending-actions {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.network-event {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  color: #8b9b7e;
  margin-top: 18px;
  line-height: 1.8;
}
@media (max-width: 1100px) {
  .network-fields {
    gap: 15px;
  }
  .port-card {
    padding: 23px 20px;
  }
  .port-label {
    display: none;
  }
  .network-toolbar {
    flex-wrap: wrap;
  }
  .network-toolbar > div:last-child {
    margin-left: auto;
  }
  .pending-banner {
    flex-wrap: wrap;
  }
  .pending-actions {
    flex-direction: row;
    width: 100%;
    justify-content: flex-end;
  }
}
@media (max-width: 700px) {
  .network-fields {
    grid-template-columns: 1fr;
  }
  .network-intro {
    align-items: flex-start;
    flex-direction: column;
  }
  .network-intro h2 {
    font-size: 21px;
  }
  .network-preview {
    padding: 20px;
  }
  .preview-change > div {
    gap: 10px;
    flex-wrap: wrap;
  }
  .preview-change p {
    min-width: 100px;
  }
  .network-toolbar > div:first-child {
    line-height: 1.8;
  }
  .pending-banner {
    padding: 19px;
  }
  .pending-actions {
    flex-direction: column;
  }
  .network-toolbar .button {
    padding: 10px;
    font-size: 10px;
  }
  .network-devices {
    grid-template-columns: 1fr;
  }
  .network-intro p {
    line-height: 1.8;
  }
}
</style>
