<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { Download, Upload, Archive, RefreshCw } from "lucide-vue-next";
import { networkRequest } from "../network-api";
interface Bundle {
  format: string;
  version: number;
  createdAt: number;
  network: unknown;
  services: unknown;
  firewall: unknown;
}
interface Plan {
  backup: Bundle;
  token: string;
  changes: string[];
}
interface State {
  pending: { id: string; phase: string; deadline: number } | null;
  event: string;
  serverTime: number;
}
const state = ref<State | null>(null),
  file = ref<Bundle | null>(null),
  name = ref(""),
  plan = ref<Plan | null>(null),
  error = ref(""),
  busy = ref(false),
  notice = ref("");
const remaining = computed(() =>
  Math.max(
    0,
    Math.ceil(
      ((state.value?.pending?.deadline || 0) - (state.value?.serverTime || 0)) /
        1000,
    ),
  ),
);
const phases: Record<string, string> = {
  applying: "正在恢复配置",
  awaiting_confirmation: "等待确认",
  rolling_back: "正在还原原配置",
  rollback_failed: "还原未完成，将自动重试",
};
let timer: ReturnType<typeof setInterval>,
  polling = false,
  closed = false;
async function refresh() {
  if (polling) return;
  polling = true;
  try {
    const s = await networkRequest<State>("backup/state");
    if (!closed) state.value = s;
  } catch (e) {
    if (!closed) error.value = e instanceof Error ? e.message : "读取状态失败";
  } finally {
    polling = false;
  }
}
async function run(fn: () => Promise<void>) {
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await fn();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "操作失败";
  } finally {
    busy.value = false;
    await refresh();
  }
}
async function download() {
  await run(async () => {
    const data = await networkRequest<Bundle>("backup/export");
    const url = URL.createObjectURL(
      new Blob([JSON.stringify(data, null, 2) + "\n"], {
        type: "application/json",
      }),
    );
    const a = document.createElement("a");
    a.href = url;
    a.download =
      "pilot-backup-" + new Date().toISOString().replaceAll(":", "-") + ".json";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    notice.value = "已生成当前已确认配置的备份。";
  });
}
async function select(event: Event) {
  plan.value = null;
  file.value = null;
  name.value = "";
  error.value = "";
  const f = (event.target as HTMLInputElement).files?.[0];
  if (!f) return;
  try {
    if (f.size > 262144) throw new Error("备份文件不能超过 256 KB");
    file.value = JSON.parse(await f.text());
    name.value = f.name;
  } catch (e) {
    error.value = e instanceof Error ? e.message : "无法读取 JSON 备份";
  }
}
async function preview() {
  await run(async () => {
    plan.value = null;
    plan.value = await networkRequest<Plan>("backup/preview", file.value);
  });
}
async function apply() {
  if (!plan.value) return;
  await run(async () => {
    await networkRequest("backup/apply", {
      backup: plan.value!.backup,
      token: plan.value!.token,
    });
    plan.value = null;
    notice.value = "恢复已开始，请等待并检查连接。";
  });
}
async function finish(action: string) {
  if (!state.value?.pending) return;
  const id = state.value.pending.id;
  await run(async () => {
    await networkRequest("backup/" + action, { id });
    notice.value =
      action === "confirm" ? "恢复已确认。" : "已开始还原导入前的配置。";
  });
}
onMounted(() => {
  refresh();
  timer = setInterval(refresh, 2000);
});
onUnmounted(() => {
  closed = true;
  clearInterval(timer);
});
</script>
<template>
  <section class="backup-page">
    <div v-if="error" class="notice error" role="alert">{{ error }}</div>
    <div v-if="notice" class="notice" role="status">{{ notice }}</div>
    <section class="panel intro">
      <div>
        <span class="section-kicker">CONFIGURATION BACKUP</span>
        <h2>把好用的配置，留一份。</h2>
        <p>保存已确认设置，需要时一起恢复。</p>
      </div>
      <Archive :size="34" />
    </section>
    <section v-if="state?.pending" class="panel transaction">
      <h3>{{ phases[state.pending.phase] || state.pending.phase }}</h3>
      <p>{{ state.event }}</p>
      <template v-if="state.pending.phase === 'awaiting_confirmation'"
        ><strong class="countdown">{{ remaining }} 秒</strong>
        <p>请检查管理页面、LAN 联网与域名解析。超时未确认会还原导入前配置。</p>
        <div class="actions">
          <button
            class="button primary"
            :disabled="busy || remaining === 0"
            @click="finish('confirm')"
          >
            网络正常，确认保留</button
          ><button
            class="button secondary"
            :disabled="busy"
            @click="finish('rollback')"
          >
            还原导入前配置
          </button>
        </div></template
      >
      <button
        v-else-if="state.pending.phase === 'rollback_failed'"
        class="button secondary"
        :disabled="busy"
        @click="finish('rollback')"
      >
        立即重试还原
      </button>
    </section>
    <div class="columns">
      <section class="panel">
        <span class="step">01 / 保存</span>
        <h3>下载当前配置</h3>
        <p>
          包含 WAN/LAN、DHCP 地址池、DNS 记录与上游、防火墙、NAT 和端口映射。
        </p>
        <p class="hint">
          不包含未应用草稿、日志、租约、系统文件或程序。备份为可读 JSON 文件。
        </p>
        <button
          class="button primary"
          :disabled="busy || !state || !!state.pending"
          @click="download"
        >
          <Download :size="16" />下载配置备份
        </button>
      </section>
      <section class="panel">
        <span class="step">02 / 恢复</span>
        <h3>选择之前的备份</h3>
        <p>选择 Pilot 导出的文件，先检查配置差异，再开始恢复。</p>
        <label class="file-picker"
          ><Upload :size="20" /><span>{{ name || "选择 JSON 备份文件" }}</span
          ><input
            aria-label="选择备份文件"
            type="file"
            accept=".json,application/json"
            :disabled="busy || !state || !!state.pending"
            @change="select" /></label
        ><button
          class="button secondary"
          :disabled="busy || !file || !state || !!state.pending"
          @click="preview"
        >
          校验并预览恢复
        </button>
      </section>
    </div>
    <section v-if="plan" class="panel">
      <h3>恢复预览</h3>
      <p>
        备份时间：{{
          plan.backup.createdAt
            ? new Date(plan.backup.createdAt).toLocaleString()
            : "未记录"
        }}
        · 格式版本 {{ plan.backup.version }}
      </p>
      <ul v-if="plan.changes.length">
        <li v-for="change in plan.changes" :key="change">
          将替换：{{ change }}
        </li>
      </ul>
      <p v-else>备份与当前配置一致，无需恢复。</p>
      <details>
        <summary>查看将恢复的完整配置</summary>
        <pre>{{ JSON.stringify(plan.backup, null, 2) }}</pre>
      </details>
      <p class="hint">
        恢复可能短暂中断 DHCP/DNS 和转发。成功后有 90
        秒确认时间，失败、超时或服务重启会尝试还原。
      </p>
      <button
        class="button primary"
        :disabled="busy || !plan.changes.length || !!state?.pending"
        @click="apply"
      >
        开始恢复并测试
      </button>
    </section>
    <section class="panel notes">
      <div class="status-line">
        <h3>恢复状态</h3>
        <button class="icon-button" aria-label="刷新恢复状态" @click="refresh">
          <RefreshCw :size="16" />
        </button>
      </div>
      <p>{{ state?.event || "尚无恢复操作" }}</p>
      <p class="hint">
        恢复到当前设备的原 WAN/LAN
        接口；不支持自动迁移接口角色，受保护的管理接口继续受到保护。请先确认已有网络或防火墙变更，再导出或恢复。
      </p>
    </section>
  </section>
</template>
<style scoped>
.backup-page {
  display: grid;
  gap: 20px;
}
.panel {
  padding: 24px;
  min-width: 0;
}
.intro,
.status-line {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 18px;
}
.intro svg {
  color: #329976;
}
.intro h2 {
  font-size: 24px;
  margin: 12px 0;
}
.columns {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
}
h3 {
  font-size: 17px;
  margin: 12px 0;
}
p,
li {
  font-size: 13px;
  line-height: 1.9;
  color: #60786b;
}
.hint {
  font-size: 12px;
  color: #859288;
}
.step {
  font-size: 11px;
  letter-spacing: 1px;
  color: #2f9770;
}
.file-picker {
  position: relative;
  display: flex;
  gap: 12px;
  align-items: center;
  min-height: 66px;
  padding: 14px;
  border: 1px dashed #9dbbad;
  border-radius: 10px;
  margin: 16px 0;
  color: #397657;
  overflow-wrap: anywhere;
}
.file-picker input {
  position: absolute;
  inset: 0;
  opacity: 0;
  cursor: pointer;
  width: 100%;
}
.file-picker:focus-within {
  outline: 2px solid #27976e;
}
.transaction {
  border: 1px solid #72b997;
  background: #f4fbf6;
}
.countdown {
  font-size: 30px;
  color: #25885d;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}
details {
  margin: 18px 0;
  font-size: 13px;
}
summary {
  cursor: pointer;
  color: #388367;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  max-height: 360px;
  overflow: auto;
  padding: 16px;
  background: #f5f8f6;
  font-size: 12px;
  line-height: 1.7;
}
.button {
  margin-top: 8px;
}
:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
@media (max-width: 800px) {
  .columns {
    grid-template-columns: 1fr;
  }
}
@media (max-width: 600px) {
  .panel {
    padding: 18px;
  }
  .intro h2 {
    font-size: 20px;
  }
}
</style>
