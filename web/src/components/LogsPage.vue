<script setup lang="ts">
import { ref, onMounted, onUnmounted } from "vue";
import { RefreshCw, Download, Search, ScrollText } from "lucide-vue-next";
import { networkRequest } from "../network-api";
interface Entry {
  cursor: string;
  time: number;
  priority: number;
  unit: string;
  message: string;
}
interface Result {
  entries: Entry[];
  nextCursor: string;
  hasMore: boolean;
  until: number;
  scanned: number;
}
const source = ref("pilot"),
  level = ref("all"),
  windowRange = ref("24h"),
  search = ref("");
const entries = ref<Entry[]>([]),
  error = ref(""),
  busy = ref(false),
  result = ref<Result | null>(null),
  auto = ref(false),
  updated = ref(0);
const active = ref({
  source: "pilot",
  level: "all",
  window: "24h",
  search: "",
});
let timer: ReturnType<typeof setInterval>,
  generation = 0,
  closed = false;
const names = ["紧急", "警报", "严重", "错误", "警告", "通知", "信息", "调试"];
async function load(older = false) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  const id = ++generation;
  if (!older)
    active.value = {
      source: source.value,
      level: level.value,
      window: windowRange.value,
      search: search.value,
    };
  const params = new URLSearchParams({ ...active.value, limit: "100" });
  if (older && result.value) {
    params.set("cursor", result.value.nextCursor);
    params.set("until", String(result.value.until));
    auto.value = false;
  }
  try {
    const data = await networkRequest<Result>("logs?" + params);
    if (closed || id !== generation) return;
    const seen = new Set(older ? entries.value.map((e) => e.cursor) : []);
    const extra = data.entries.filter((e) => !seen.has(e.cursor));
    entries.value = older ? [...entries.value, ...extra] : extra;
    result.value = data;
    updated.value = Date.now();
  } catch (e) {
    if (!closed) error.value = e instanceof Error ? e.message : "读取日志失败";
  } finally {
    busy.value = false;
  }
}
function exportLogs() {
  const data = entries.value
    .map(
      (e) =>
        `${new Date(e.time).toISOString()} [${names[e.priority]}] ${e.unit}: ${e.message}`,
    )
    .join("\n");
  const url = URL.createObjectURL(
    new Blob([data], { type: "text/plain;charset=utf-8" }),
  );
  const a = document.createElement("a");
  a.href = url;
  a.download =
    "pilot-logs-" + new Date().toISOString().replaceAll(":", "-") + ".txt";
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
onMounted(() => {
  load();
  timer = setInterval(() => {
    if (auto.value && !busy.value) load();
  }, 10000);
});
onUnmounted(() => {
  closed = true;
  generation++;
  clearInterval(timer);
});
</script>
<template>
  <section class="logs-page">
    <div v-if="error" class="notice error" role="alert">
      {{ error }}。下方保留上次结果。
    </div>
    <section class="panel intro">
      <div>
        <span class="section-kicker">SYSTEM JOURNAL</span>
        <h2>每一次变化，都有迹可循。</h2>
        <p>读取系统真实日志，按时间倒序显示。</p>
      </div>
      <ScrollText :size="34" />
    </section>
    <form class="panel filters" @submit.prevent="load()">
      <label
        >日志来源<select v-model="source" :disabled="busy">
          <option value="pilot">Pilot 全部服务</option>
          <option value="web">Web 服务</option>
          <option value="network">网络助手</option>
          <option value="system">全部系统日志</option>
          <option value="kernel">内核</option>
        </select></label
      >
      <label
        >级别<select v-model="level" :disabled="busy">
          <option value="all">全部级别</option>
          <option value="warning">警告及以上</option>
          <option value="error">错误及以上</option>
        </select></label
      >
      <label
        >时间范围<select v-model="windowRange" :disabled="busy">
          <option value="1h">最近 1 小时</option>
          <option value="24h">最近 24 小时</option>
          <option value="7d">最近 7 天</option>
        </select></label
      >
      <label class="keyword"
        >关键词<input
          v-model="search"
          maxlength="256"
          placeholder="搜索消息或服务名"
          :disabled="busy"
      /></label>
      <button class="button primary" :disabled="busy">
        <Search :size="15" />{{ busy ? "查询中…" : "查询日志" }}
      </button>
    </form>
    <section class="panel log-panel">
      <div class="toolbar">
        <div>
          <h3>
            日志记录 <span class="number-badge">{{ entries.length }}</span>
          </h3>
          <p>
            {{
              updated
                ? "更新于 " + new Date(updated).toLocaleTimeString()
                : "尚未加载"
            }}
            · 每次最多 100 条
          </p>
        </div>
        <div class="tools">
          <label class="auto"
            ><input type="checkbox" v-model="auto" />每 10 秒刷新</label
          ><button class="button secondary" :disabled="busy" @click="load()">
            <RefreshCw :size="15" />刷新</button
          ><button
            class="button secondary"
            :disabled="!entries.length"
            @click="exportLogs"
          >
            <Download :size="15" />导出已加载日志
          </button>
        </div>
      </div>
      <p class="hint">
        {{
          result?.hasMore && !entries.length
            ? "本批扫描未命中关键词，可以继续加载更早日志。"
            : "关键词按普通文本匹配；开启自动刷新会返回最新一页。"
        }}
        日志保留时间由系统 journal 决定。
      </p>
      <div
        class="log-list"
        role="log"
        aria-label="系统日志记录"
        aria-live="off"
      >
        <article v-for="entry in entries" :key="entry.cursor" class="log-entry">
          <div class="meta">
            <time>{{ new Date(entry.time).toLocaleString() }}</time
            ><span
              :class="[
                'severity',
                { danger: entry.priority <= 3, warning: entry.priority === 4 },
              ]"
              >{{ names[entry.priority] }}</span
            ><span class="unit">{{ entry.unit || "system" }}</span>
          </div>
          <pre>{{ entry.message }}</pre>
        </article>
        <div v-if="!entries.length" class="empty">
          {{ busy ? "正在读取日志…" : "当前范围暂无匹配日志。" }}
        </div>
      </div>
      <div class="more">
        <button
          v-if="result?.hasMore && entries.length < 2000"
          class="button secondary"
          :disabled="busy"
          @click="load(true)"
        >
          {{ busy ? "加载中…" : "加载更早日志" }}</button
        ><span v-else>{{
          entries.length >= 2000
            ? "已加载 2000 条，请缩小筛选范围或导出。"
            : "已到本次查询范围末尾"
        }}</span>
      </div>
    </section>
  </section>
</template>
<style scoped>
.logs-page {
  display: grid;
  gap: 20px;
}
.panel {
  padding: 24px;
  min-width: 0;
}
.intro,
.toolbar,
.tools {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}
.intro svg {
  color: #379c77;
}
.intro h2 {
  font-size: 24px;
  margin: 12px 0;
}
.intro p,
.toolbar p,
.hint {
  font-size: 12px;
  color: #76857d;
  line-height: 1.8;
}
.filters {
  display: grid;
  grid-template-columns: 1.2fr 1fr 1fr 1.5fr auto;
  gap: 14px;
  align-items: end;
}
label:not(.auto) {
  display: grid;
  gap: 9px;
  font-size: 12px;
  color: #68776e;
}
input:not([type="checkbox"]),
select {
  width: 100%;
  box-sizing: border-box;
  min-width: 0;
  border: 1px solid #dce5df;
  border-radius: 9px;
  padding: 11px;
  background: white;
  color: #243f31;
  font: inherit;
}
.toolbar h3 {
  margin: 0;
  font-size: 17px;
}
.toolbar p {
  margin: 8px 0 0;
}
.auto {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: #68776e;
}
.auto input {
  accent-color: #27976e;
}
.log-list {
  border-top: 1px solid #edf1ee;
  margin-top: 18px;
}
.log-entry {
  padding: 17px 0;
  border-bottom: 1px solid #edf1ee;
}
.meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  font-size: 11px;
  color: #869189;
}
.unit {
  color: #527462;
  overflow-wrap: anywhere;
}
.severity {
  border-radius: 5px;
  padding: 3px 7px;
  background: #edf6f0;
  color: #418264;
}
.severity.danger {
  background: #ffeded;
  color: #b45050;
}
.severity.warning {
  background: #fff5dc;
  color: #a57926;
}
pre {
  font:
    12px/1.8 ui-monospace,
    SFMono-Regular,
    Consolas,
    monospace;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  color: #344d3e;
  margin: 10px 0 0;
  unicode-bidi: plaintext;
}
.empty {
  padding: 40px;
  text-align: center;
  font-size: 13px;
  color: #859187;
}
.more {
  text-align: center;
  padding-top: 20px;
  font-size: 12px;
  color: #869189;
}
.tools .button {
  font-size: 12px;
}
.logs-page :disabled {
  opacity: 0.55;
}
@media (max-width: 1100px) {
  .filters {
    grid-template-columns: 1fr 1fr 1fr;
  }
  .keyword {
    grid-column: 1/3;
  }
}
@media (max-width: 600px) {
  .panel {
    padding: 18px;
  }
  .intro h2 {
    font-size: 20px;
  }
  .filters {
    grid-template-columns: 1fr 1fr;
  }
  .keyword {
    grid-column: auto;
  }
  .filters .button {
    grid-column: 1/-1;
  }
  .tools {
    gap: 10px;
  }
  .meta time {
    width: 100%;
  }
}
</style>
