<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from "vue";
import {
  Shield,
  ArrowRight,
  Plus,
  Trash2,
  ArrowUp,
  ArrowDown,
} from "lucide-vue-next";
import { networkRequest } from "../network-api";
const props = defineProps<{ forwarding?: boolean }>();
interface Forward {
  enabled: boolean;
  protocol: string;
  externalPort: number;
  target: string;
  internalPort: number;
  source: string;
}
interface Rule {
  enabled: boolean;
  action: string;
  protocol: string;
  source: string;
  destination: string;
  port: string;
}
interface Config {
  enabled: boolean;
  nat: boolean;
  defaultAction: string;
  wan: string;
  lan: string;
  subnet: string;
  rules: Rule[];
  forwards: Forward[];
}
interface Pending {
  id: string;
  deadline: number;
  config: Config;
}
interface State {
  config: Config;
  pending: Pending | null;
  available: boolean;
  forwarding: boolean;
  ready: boolean;
  event: string;
  serverTime: number;
}
interface Plan {
  config: Config;
  revision: string;
  token: string;
  text: string;
}
const state = ref<State | null>(null),
  plan = ref<Plan | null>(null),
  busy = ref(false),
  error = ref(""),
  success = ref("");
const config = ref<Config>({
  enabled: false,
  nat: false,
  defaultAction: "ACCEPT",
  wan: "",
  lan: "",
  subnet: "",
  rules: [],
  forwards: [],
});
const baseline = ref(""),
  clock = ref(Date.now());
let offset = 0,
  loaded = false,
  fetching = false;
let poll: ReturnType<typeof setInterval>, tick: ReturnType<typeof setInterval>;
const dirty = computed(
  () =>
    baseline.value !== "" && JSON.stringify(config.value) !== baseline.value,
);
const remaining = computed(() =>
  Math.max(
    0,
    Math.ceil(
      ((state.value?.pending?.deadline || 0) - (clock.value + offset)) / 1000,
    ),
  ),
);
watch(
  config,
  () => {
    plan.value = null;
    success.value = "";
  },
  { deep: true },
);
function failed(e: unknown) {
  error.value = e instanceof Error ? e.message : "操作失败";
}
async function refresh() {
  if (busy.value || fetching) return;
  fetching = true;
  try {
    const s = await networkRequest<State>("network/firewall/state");
    offset = s.serverTime - Date.now();
    const changed = state.value?.pending?.id !== s.pending?.id;
    state.value = s;
    if (!loaded || changed) {
      config.value = structuredClone(s.config);
      baseline.value = JSON.stringify(s.config);
      loaded = true;
      plan.value = null;
    }
  } catch (e) {
    failed(e);
  } finally {
    fetching = false;
  }
}
async function preview() {
  busy.value = true;
  error.value = "";
  success.value = "";
  try {
    plan.value = await networkRequest<Plan>(
      "network/firewall/preview",
      config.value,
    );
  } catch (e) {
    failed(e);
  } finally {
    busy.value = false;
  }
}
async function apply() {
  if (!plan.value) return;
  busy.value = true;
  error.value = "";
  try {
    await networkRequest("network/firewall/apply", plan.value);
    loaded = false;
    plan.value = null;
  } catch (e) {
    failed(e);
  } finally {
    busy.value = false;
  }
  await refresh();
}
async function finish(action: "confirm" | "rollback") {
  if (!state.value?.pending) return;
  busy.value = true;
  error.value = "";
  let ok = false;
  try {
    await networkRequest("network/firewall/" + action, {
      id: state.value.pending.id,
    });
    loaded = false;
    ok = true;
  } catch (e) {
    failed(e);
  } finally {
    busy.value = false;
  }
  await refresh();
  await nextTick();
  if (ok)
    success.value =
      action === "confirm"
        ? "配置已确认，重启后会自动恢复。"
        : "已回滚到修改前的配置。";
}
function move(i: number, step: number) {
  const rules = config.value.rules;
  const other = rules[i + step];
  if (!other) return;
  [rules[i], rules[i + step]] = [other, rules[i]!];
}
function disable() {
  if (!config.value.enabled) config.value.nat = false;
}
onMounted(() => {
  refresh();
  poll = setInterval(refresh, 2000);
  tick = setInterval(() => (clock.value = Date.now()), 500);
});
onUnmounted(() => {
  clearInterval(poll);
  clearInterval(tick);
});
</script>

<template>
  <section class="firewall-page">
    <div v-if="error" class="notice error" role="alert">{{ error }}</div>
    <div v-if="success" class="notice" role="status">{{ success }}</div>
    <section v-if="state?.pending" class="panel confirmation">
      <span class="section-kicker">等待网络确认</span>
      <h2>{{ remaining }} 秒后自动回滚</h2>
      <p>
        规则已临时生效。{{
          props.forwarding
            ? "请从 WAN 侧测试映射端口"
            : "请用 LAN 客户端测试联网"
        }}，再确认保留；关闭页面也不会取消自动回滚。
      </p>
      <div class="actions">
        <button
          class="button primary"
          :disabled="busy || !remaining || !state.ready"
          @click="finish('confirm')"
        >
          网络正常，确认保留</button
        ><button
          class="button secondary"
          :disabled="busy"
          @click="finish('rollback')"
        >
          立即回滚
        </button>
      </div>
    </section>
    <section class="panel overview">
      <div>
        <span class="section-kicker">{{
          props.forwarding ? "PORT FORWARDING" : "FIREWALL & NAT"
        }}</span>
        <h2>
          {{
            props.forwarding
              ? "让内网服务，有一个入口。"
              : "让内网安全地走向外网。"
          }}
        </h2>
        <p>
          {{
            props.forwarding
              ? "WAN 端口 → LAN 设备 · TCP / UDP · 来源限制"
              : "IPv4 转发规则 · 按顺序匹配 · 出口地址转换"
          }}
        </p>
      </div>
      <span
        :class="['status-pill', { up: state?.config.enabled && state?.ready }]"
        >{{
          !state
            ? "正在读取"
            : !state.available
              ? "未安装 iptables"
              : state.config.enabled && state.ready
                ? "转发防火墙已启用"
                : "转发防火墙未启用"
        }}</span
      >
      <div v-if="!props.forwarding" class="path">
        <span
          ><b>LAN</b>{{ config.lan || "按 WAN/LAN 配置"
          }}<small>{{ config.subnet || "应用时自动绑定网段" }}</small></span
        ><ArrowRight :size="22" /><span
          ><Shield :size="24" /><b>转发规则</b></span
        ><ArrowRight :size="22" /><span
          ><b>WAN</b>{{ config.wan || "按 WAN/LAN 配置"
          }}<small>{{
            config.nat ? "NAT · MASQUERADE" : "未启用 NAT"
          }}</small></span
        >
      </div>
    </section>
    <fieldset :disabled="busy || !!state?.pending || !state" class="fields">
      <section class="panel card">
        <div class="heading">
          <div>
            <h3>转发与上网</h3>
            <p>启用后按规则转发流量；WAN 仅能通过已配置的端口映射连接 LAN。</p>
          </div>
          <label class="toggle"
            ><input
              type="checkbox"
              v-model="config.enabled"
              @change="disable"
            />启用转发防火墙</label
          >
        </div>
        <div class="options">
          <label class="toggle"
            ><input
              type="checkbox"
              v-model="config.nat"
              :disabled="!config.enabled"
            />启用出口 NAT</label
          >
          <p>将 LAN 的源地址转换为 WAN 地址；上游无需配置返回内网的路由。</p>
        </div>
        <label class="default-policy"
          >LAN → WAN 默认策略<select v-model="config.defaultAction">
            <option value="ACCEPT">放行未匹配流量</option>
            <option value="DROP">阻止未匹配流量</option>
          </select></label
        >
        <p class="hint">
          已建立连接及其返回流量优先放行。新增阻止规则请用新连接测试，已有连接不会立即中断。停用时恢复启用前的系统转发开关。
        </p>
      </section>
      <section v-if="!props.forwarding" class="panel card">
        <div class="heading">
          <div>
            <h3>
              LAN → WAN 规则
              <span class="number-badge">{{ config.rules.length }}</span>
            </h3>
            <p>从上到下匹配，首条命中即生效。地址留空表示任意地址。</p>
          </div>
          <button
            class="button secondary"
            :disabled="config.rules.length >= 128"
            @click="
              config.rules.push({
                enabled: true,
                action: 'DROP',
                protocol: 'tcp',
                source: '',
                destination: '',
                port: '',
              })
            "
          >
            <Plus :size="16" />添加规则
          </button>
        </div>
        <p v-if="!config.rules.length" class="empty">
          还没有自定义规则，LAN → WAN 将使用默认策略。
        </p>
        <div v-for="(rule, i) in config.rules" :key="i" class="rule">
          <div class="rule-title">
            <label class="toggle"
              ><input
                type="checkbox"
                v-model="rule.enabled"
                :aria-label="`启用规则 ${i + 1}`"
              />规则 {{ i + 1 }}</label
            >
            <div class="actions">
              <button
                class="icon-button"
                :disabled="i === 0"
                :aria-label="`上移规则 ${i + 1}`"
                @click="move(i, -1)"
              >
                <ArrowUp :size="16" /></button
              ><button
                class="icon-button"
                :disabled="i === config.rules.length - 1"
                :aria-label="`下移规则 ${i + 1}`"
                @click="move(i, 1)"
              >
                <ArrowDown :size="16" /></button
              ><button
                class="icon-button"
                :aria-label="`删除规则 ${i + 1}`"
                @click="config.rules.splice(i, 1)"
              >
                <Trash2 :size="16" />
              </button>
            </div>
          </div>
          <div class="rule-fields">
            <label
              >动作<select v-model="rule.action">
                <option value="DROP">阻止</option>
                <option value="ACCEPT">放行</option>
              </select></label
            ><label
              >协议<select v-model="rule.protocol" @change="rule.port = ''">
                <option value="all">全部</option>
                <option value="tcp">TCP</option>
                <option value="udp">UDP</option>
                <option value="icmp">ICMP</option>
              </select></label
            ><label
              >来源 IPv4 / 网段<input
                v-model="rule.source"
                placeholder="192.168.60.100" /></label
            ><label
              >目标 IPv4 / 网段<input
                v-model="rule.destination"
                placeholder="任意地址" /></label
            ><label
              >目标端口<input
                v-model="rule.port"
                :disabled="!['tcp', 'udp'].includes(rule.protocol)"
                placeholder="80 或 8000-8010"
            /></label>
          </div>
        </div>
      </section>
      <section v-if="props.forwarding" class="panel card">
        <div class="heading">
          <div>
            <h3>
              端口映射
              <span class="number-badge">{{ config.forwards.length }}</span>
            </h3>
            <p>
              访问 WAN 地址的外部端口，转发到指定 LAN 设备。每条映射独立启停。
            </p>
          </div>
          <button
            class="button secondary"
            :disabled="config.forwards.length >= 128"
            @click="
              config.forwards.push({
                enabled: true,
                protocol: 'tcp',
                externalPort: 18080,
                target: '',
                internalPort: 80,
                source: '',
              })
            "
          >
            <Plus :size="16" />添加映射
          </button>
        </div>
        <p v-if="!config.enabled" class="notice">
          转发防火墙未启用。映射可以保存，启用转发防火墙后才会生效。
        </p>
        <p v-if="!config.forwards.length" class="empty">
          还没有端口映射。例如 WAN:18080 → LAN 设备:80。
        </p>
        <div v-for="(f, i) in config.forwards" :key="i" class="rule">
          <div class="rule-title">
            <label class="toggle"
              ><input
                type="checkbox"
                v-model="f.enabled"
                :aria-label="`启用映射 ${i + 1}`"
              />映射 {{ i + 1 }}</label
            ><button
              class="icon-button"
              :aria-label="`删除映射 ${i + 1}`"
              @click="config.forwards.splice(i, 1)"
            >
              <Trash2 :size="16" />
            </button>
          </div>
          <div class="rule-fields mapping-fields">
            <label
              >协议<select
                v-model="f.protocol"
                :aria-label="`映射 ${i + 1} 协议`"
              >
                <option value="tcp">TCP</option>
                <option value="udp">UDP</option>
              </select></label
            >
            <label
              >外部端口<input
                type="number"
                min="1"
                max="65535"
                v-model.number="f.externalPort"
                :aria-label="`映射 ${i + 1} 外部端口`"
            /></label>
            <label
              >内网设备 IPv4<input
                v-model="f.target"
                placeholder="192.168.60.100"
                :aria-label="`映射 ${i + 1} 内网地址`"
            /></label>
            <label
              >内部端口<input
                type="number"
                min="1"
                max="65535"
                v-model.number="f.internalPort"
                :aria-label="`映射 ${i + 1} 内部端口`"
            /></label>
            <label
              >允许来源 IPv4 / 网段<input
                v-model="f.source"
                placeholder="留空允许所有 WAN 来源"
                :aria-label="`映射 ${i + 1} 来源`"
            /></label>
          </div>
        </div>
        <p class="hint">
          目标设备需使用 Pilot 作为返回网关。只接收从 WAN
          进入、发往路由器本机地址的请求；不支持内网回环访问。相同协议的启用映射不能占用相同外部端口，本机监听端口冲突会在预览时提示。
        </p>
      </section>
    </fieldset>
    <div class="actions footer-actions">
      <p>
        {{
          dirty ? "有未应用的修改，离开页面会丢失。" : "配置已与服务器同步。"
        }}
      </p>
      <button
        class="button primary"
        :disabled="busy || !state?.available || !!state?.pending"
        @click="preview"
      >
        校验并预览
      </button>
    </div>
    <section v-if="plan" class="panel card preview">
      <h3>即将应用的规则</h3>
      <p>
        {{ plan.config.enabled ? "启用 IPv4 转发防火墙" : "停用转发防火墙" }} ·
        NAT {{ plan.config.nat ? "开启" : "关闭" }} ·
        {{ plan.config.rules.filter((r) => r.enabled).length }} 条启用规则 ·
        {{ plan.config.forwards.filter((f) => f.enabled).length }} 条启用映射
      </p>
      <details>
        <summary>查看 iptables 规则</summary>
        <pre>{{ plan.text }}</pre>
      </details>
      <p>
        防火墙、NAT 与端口映射作为一份配置同时应用。应用后需在 90
        秒内确认，超时或服务重启会回滚。
      </p>
      <button class="button primary" :disabled="busy" @click="apply">
        {{ busy ? "正在应用…" : "应用并开始测试" }}
      </button>
    </section>
    <p class="hint">
      {{ state?.event }}。本版管理 IPv4 转发；本机 SSH、Web、DHCP/DNS
      访问不受转发过滤规则限制。IPv6 和本机入站防护尚未提供。系统 IPv4 转发：{{
        state?.forwarding ? "已开启" : "未开启"
      }}。
    </p>
  </section>
</template>

<style scoped>
.firewall-page {
  display: grid;
  gap: 20px;
}
.panel {
  padding: 26px;
}
.overview {
  display: flex;
  flex-wrap: wrap;
  gap: 24px;
  align-items: center;
  justify-content: space-between;
}
.overview h2 {
  font-size: 24px;
  margin: 12px 0;
}
.overview p,
.heading p,
.options p,
.hint,
.preview p,
.confirmation p,
.footer-actions p {
  font-size: 13px;
  color: #728277;
  line-height: 1.8;
}
.path {
  display: flex;
  width: 100%;
  background: #f3f8f5;
  border-radius: 14px;
  padding: 24px;
  justify-content: space-around;
  align-items: center;
  gap: 12px;
}
.path > span {
  display: grid;
  gap: 9px;
  text-align: center;
  justify-items: center;
  font-size: 13px;
}
.path small {
  color: #76857d;
}
.path > svg {
  color: #68a58a;
}
.fields {
  border: 0;
  margin: 0;
  padding: 0;
  min-width: 0;
  display: grid;
  gap: 20px;
}
.heading,
.rule-title,
.actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.heading h3,
.preview h3 {
  margin: 0;
  font-size: 17px;
}
.toggle {
  display: flex;
  gap: 9px;
  align-items: center;
  font-size: 13px;
}
.toggle input {
  accent-color: #27976e;
}
.options {
  padding: 20px 0;
  border-top: 1px solid #edf1ee;
  margin-top: 20px;
}
.options p {
  margin: 8px 0 0;
}
.default-policy {
  max-width: 310px;
}
.default-policy,
.rule-fields label {
  display: grid;
  gap: 9px;
  font-size: 12px;
  color: #68776e;
}
select,
input:not([type="checkbox"]) {
  min-width: 0;
  width: 100%;
  box-sizing: border-box;
  padding: 11px;
  border: 1px solid #dce5df;
  border-radius: 9px;
  background: white;
  font: inherit;
  color: #243f31;
}
.rule {
  border-top: 1px solid #edf1ee;
  padding-top: 18px;
  margin-top: 18px;
}
.rule-fields {
  display: grid;
  grid-template-columns: 90px 90px 1fr 1fr 1fr;
  gap: 12px;
  margin-top: 14px;
}
.rule-title .actions {
  gap: 2px;
}
.mapping-fields {
  grid-template-columns: 80px 100px minmax(120px, 1fr) 100px minmax(
      140px,
      1.3fr
    );
}
.empty {
  text-align: center;
  color: #859187;
  padding: 28px;
  font-size: 13px;
}
.confirmation {
  border-color: #d6b975;
  background: #fffdf5;
}
.confirmation h2 {
  font-size: 24px;
}
.confirmation .actions {
  justify-content: flex-start;
  flex-wrap: wrap;
}
.preview {
  border-color: #a5d4bf;
}
.preview summary {
  font-size: 13px;
  cursor: pointer;
  margin: 18px 0;
}
.preview pre {
  background: #f4f7f5;
  padding: 16px;
  max-height: 320px;
  overflow: auto;
  font-size: 12px;
}
.footer-actions {
  flex-wrap: wrap;
}
.firewall-page :disabled {
  opacity: 0.55;
}
@media (max-width: 1000px) {
  .rule-fields {
    grid-template-columns: 1fr 1fr;
  }
  .heading {
    flex-wrap: wrap;
  }
}
@media (max-width: 600px) {
  .panel {
    padding: 18px;
  }
  .overview h2 {
    font-size: 20px;
  }
  .path {
    padding: 15px;
    gap: 8px;
  }
  .path small {
    font-size: 10px;
  }
  .rule-fields {
    grid-template-columns: 1fr;
  }
  .footer-actions .button {
    width: 100%;
  }
}
</style>
