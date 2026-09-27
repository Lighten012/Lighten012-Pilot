const $ = (id) => document.getElementById(id);
const node = (tag, className, value) => {
  const result = document.createElement(tag);
  result.className = className;
  result.textContent = value;
  return result;
};

function message(id, value, error = false) {
  const target = $(id);
  target.textContent = value;
  target.classList.toggle("error", error);
}

async function request(path, options) {
  const response = await fetch(path, options);
  if (!response.ok) throw new Error((await response.text()).trim() || "请求失败");
  return response.json();
}

function mutation(method, body) {
  return {
    method,
    headers: { "Content-Type": "application/json", "X-Pilot-Request": "1" },
    body: JSON.stringify(body),
  };
}

function switchPage(page) {
  for (const name of ["network", "dns", "mihomo"]) {
    $(`${name}-page`).hidden = name !== page;
  }
  document.querySelectorAll("[data-page]").forEach((button) => {
    button.classList.toggle("active", button.dataset.page === page);
  });
  $("page-name").textContent = page === "network" ? "WAN / LAN" : page === "dns" ? "DNS 解析" : "Mihomo";
  if (page === "mihomo") { loadMihomo(); loadProxyWhitelist(); }
}

document.querySelectorAll("[data-page]").forEach((button) => {
  button.onclick = () => switchPage(button.dataset.page);
});

function renderInterfaces(interfaces) {
  const container = $("interfaces");
  container.replaceChildren();
  for (const item of interfaces) {
    const card = node("article", "interface-card", "");
    const top = node("div", "interface-top", "");
    top.append(node("strong", "", item.name));
    const status = item.up ? (item.running ? "运行中" : "已启用") : "未启用";
    top.append(node("span", `interface-status${item.up ? "" : " down"}`, status));
    card.append(top);
    card.append(node("p", "interface-meta", `${item.loopback ? "回环接口 · " : ""}MAC ${item.mac || "—"}`));
    const addresses = node("div", "interface-addresses", "");
    for (const address of item.addresses) addresses.append(node("code", "", address));
    if (!item.addresses.length) addresses.append(node("span", "interface-meta", "暂无地址"));
    card.append(addresses);
    container.append(card);
  }
  if (!interfaces.length) container.append(node("p", "interface-meta", "当前没有网卡"));
}

function renderNetwork(state) {
  renderInterfaces(state.interfaces);
  const selected = Boolean(state.roles);
  $("roles-form").hidden = selected;
  $("roles-current").hidden = !selected;
  if (!selected) {
    $("dns-test-hint").textContent = "请先选择 LAN 网卡";
    const available = state.interfaces.filter((item) => !item.loopback);
    for (const id of ["wan-select", "lan-select"]) {
      const select = $(id);
      select.replaceChildren(node("option", "", "选择网卡"));
      select.firstChild.value = "";
      for (const item of available) {
        const option = node("option", "", `${item.name} · ${item.addresses.find((address) => address.includes(".")) || "无 IPv4"}`);
        option.value = item.name;
        select.append(option);
      }
    }
    return;
  }
  const wan = state.interfaces.find((item) => item.name === state.roles.wan);
  $("wan-name").textContent = state.roles.wan;
  $("lan-name").textContent = state.roles.lan;
  $("wan-address").textContent = wan?.addresses.find((address) => address.includes(".")) || "暂无 IPv4";
  $("lan-address").textContent = state.lanAddress || "暂无 IPv4";
  $("dhcp-range").textContent = state.dhcpRange ? `DHCP 自动分配 ${state.dhcpRange}` : "DHCP 未启用";
  const [address, prefix] = (state.lanAddress || "").split("/");
  $("dns-test-hint").textContent = address ? `nslookup lighten012.home ${address}` : "LAN 暂无 IPv4 地址";
  $("lan-ip").value = address || "";
  $("lan-prefix").textContent = prefix ? `/${prefix}` : "";
  $("save-lan-ip").disabled = !address;
  loadDevices();
}

function renderDevices(devices, fullDevices = {}) {
  const body = $("devices");
  body.replaceChildren();
  $("device-count").textContent = `${devices.filter((device) => device.state === "ONLINE").length} 在线 / ${devices.length} 已知`;
  if (!devices.length) {
    const row = node("tr", "", ""), cell = node("td", "empty-row", "暂未发现 LAN 设备，设备通信后可刷新查看。");
    cell.colSpan = 4;
    row.append(cell);
    body.append(row);
    return;
  }
  const labels = { ONLINE: "在线", OFFLINE: "离线", MISMATCH: "IP 对应的 MAC 已变化", UNKNOWN: "待确认" };
  for (const device of devices) {
    const row = node("tr", "", "");
    for (const value of [device.ip, device.mac]) {
      const cell = node("td", "", "");
      cell.append(node("code", "", value));
      row.append(cell);
    }
    const stateCell = node("td", "", "");
    stateCell.append(node("span", `device-state${device.state === "ONLINE" ? " active" : device.state === "OFFLINE" ? " offline" : device.state === "MISMATCH" ? " mismatch" : ""}`, labels[device.state] || device.state));
    row.append(stateCell);
    const modeCell = node("td", "", "");
    const toggle = node("button", `button secondary${fullDevices[device.mac] ? " active" : ""}`, fullDevices[device.mac] ? "全部经 Mihomo" : "白名单分流");
    toggle.type = "button";
    toggle.setAttribute("aria-pressed", String(Boolean(fullDevices[device.mac])));
    toggle.onclick = async () => {
      toggle.disabled = true;
      try {
        await request(`/api/mihomo/devices/${encodeURIComponent(device.mac)}`, mutation("PUT", { full: !fullDevices[device.mac] }));
        await loadDevices();
      } catch (error) {
        message("devices-message", error.message, true);
        toggle.disabled = false;
      }
    };
    modeCell.append(toggle);
    row.append(modeCell);
    body.append(row);
  }
}

async function loadDevices() {
  const button = $("refresh-devices");
  button.disabled = true;
  message("devices-message", "");
  try {
    const [devices, fullDevices] = await Promise.all([request("/api/lan/devices"), request("/api/mihomo/devices")]);
    renderDevices(devices, fullDevices);
  } catch (error) {
    message("devices-message", error.message, true);
  } finally {
    button.disabled = false;
  }
}

$("refresh-devices").onclick = loadDevices;

async function loadNetwork() {
  const button = $("refresh-interfaces");
  button.disabled = true;
  message("interface-error", "");
  try {
    renderNetwork(await request("/api/network"));
  } catch (error) {
    message("interface-error", error.message, true);
  } finally {
    button.disabled = false;
  }
}

$("refresh-interfaces").onclick = loadNetwork;
$("save-roles").onclick = async () => {
  const wan = $("wan-select").value, lan = $("lan-select").value;
  if (!wan || !lan || wan === lan) {
    message("roles-message", "请选择两张不同的网卡", true);
    return;
  }
  const button = $("save-roles");
  button.disabled = true;
  try {
    renderNetwork(await request("/api/network/roles", mutation("PUT", { wan, lan })));
    message("roles-message", "");
    message("lan-message", "接口角色已保存");
  } catch (error) {
    message("roles-message", error.message, true);
  } finally {
    button.disabled = false;
  }
};
$("save-lan-ip").onclick = async () => {
  const address = $("lan-ip").value.trim();
  const button = $("save-lan-ip");
  button.disabled = true;
  try {
    renderNetwork(await request("/api/network/lan-ip", mutation("PUT", { address })));
    message("lan-message", "LAN 地址已保存并应用");
  } catch (error) {
    message("lan-message", error.message, true);
  } finally {
    button.disabled = false;
  }
};

let records = [];
function renderRecords() {
  const body = $("records");
  body.replaceChildren();
  $("count").textContent = `${records.length} 条记录`;
  if (!records.length) {
    const row = node("tr", "", ""), cell = node("td", "empty-row", "还没有记录，先添加一条吧。");
    cell.colSpan = 6;
    row.append(cell);
    body.append(row);
    return;
  }
  records.forEach((record, index) => {
    const row = node("tr", "", "");
    for (const value of [record.name, record.type, record.mac ? "跟随 DHCP" : record.value, record.mac || "—", String(record.ttl)]) {
      const cell = node("td", "", "");
      cell.append(node("code", "", value));
      row.append(cell);
    }
    const cell = node("td", "", ""), button = node("button", "delete-button", "删除");
    button.type = "button";
    button.onclick = () => { records.splice(index, 1); renderRecords(); message("message", "有未保存的修改"); };
    cell.append(button);
    row.append(cell);
    body.append(row);
  });
}

async function loadDNS() {
  try {
    const data = await request("/api/state");
    records = data.config.records || [];
    $("upstream").value = data.config.upstream;
    $("backup-upstream").value = data.config.backupUpstream || "";
    for (const key of ["queries", "local", "forwarded", "mihomo"]) $(key).textContent = data.stats[key].toLocaleString();
    renderRecords();
  } catch (error) {
    message("message", error.message, true);
  }
}

function updateTargetKind() {
  const device = $("target-kind").value === "device";
  $("value-field").hidden = device;
  $("mac-field").hidden = !device;
  $("value").required = !device;
  $("mac").required = device;
  $("type").value = device ? "A" : $("type").value;
  $("type").disabled = device;
}
$("target-kind").onchange = updateTargetKind;
updateTargetKind();

$("add-form").onsubmit = (event) => {
  event.preventDefault();
  const name = $("name").value.trim().replace(/\.$/, "").toLowerCase();
  const device = $("target-kind").value === "device";
  const type = $("type").value, value = device ? "" : $("value").value.trim(), ttl = Number($("ttl").value);
  const mac = device ? $("mac").value.trim().toLowerCase().replace(/-/g, ":") : "";
  if (records.some((record) => record.name === name && record.type === type)) {
    message("message", "同一域名和类型只能添加一条记录；请先删除旧记录", true);
    return;
  }
  records.push({ name, type, value, ttl, mac });
  renderRecords();
  $("name").value = "";
  $("value").value = "";
  $("mac").value = "";
  message("message", "有未保存的修改");
};
$("save").onclick = async () => {
  const button = $("save");
  button.disabled = true;
  try {
    const upstream = $("upstream").value.trim(), backupUpstream = $("backup-upstream").value.trim();
    if (!upstream && !backupUpstream) throw new Error("至少填写一个上游 DNS");
    const config = await request("/api/config", mutation("PUT", { upstream, backupUpstream, records }));
    records = config.records;
    renderRecords();
    message("message", "已保存，DNS 记录立即生效");
  } catch (error) {
    message("message", error.message, true);
  } finally {
    button.disabled = false;
  }
};

loadNetwork();
loadDNS();
setInterval(async () => {
  try {
    const data = await request("/api/state");
    for (const key of ["queries", "local", "forwarded", "mihomo"]) $(key).textContent = data.stats[key].toLocaleString();
  } catch { /* keep the last visible sample */ }
}, 10000);

function formatBytes(value) {
  const amount = Number(value) || 0;
  if (amount < 1024) return `${amount} B`;
  if (amount < 1048576) return `${(amount / 1024).toFixed(1)} KB`;
  if (amount < 1073741824) return `${(amount / 1048576).toFixed(1)} MB`;
  return `${(amount / 1073741824).toFixed(1)} GB`;
}

function renderMihomo(data) {
  $("mihomo-badge").textContent = data.running ? "运行中" : "未运行";
  $("mihomo-summary").textContent = data.running
    ? `${data.version || "Mihomo"} · ${data.mode || "规则模式"} · 控制接口仅在本机开放`
    : (data.error || "Mihomo 服务未启动");
  $("mihomo-up").textContent = data.running ? `${formatBytes(data.up)}/s` : "—";
  $("mihomo-down").textContent = data.running ? `${formatBytes(data.down)}/s` : "—";
  $("mihomo-connections").textContent = data.running ? String(data.connections) : "—";
  $("mihomo-reload").disabled = !data.running;
  $("mihomo-import").disabled = !data.running;
  $("mihomo-update").disabled = !data.running || !data.subscriptionHost;
  $("mihomo-subscription-host").textContent = data.subscriptionHost ? `已保存订阅：${data.subscriptionHost}` : "尚未保存订阅";
  const groupSelect = $("proxy-group"), previousGroup = groupSelect.value;
  groupSelect.replaceChildren();
  for (const group of (data.groups || []).filter((item) => item.name !== "GLOBAL")) {
    const option = node("option", "", group.name);
    option.value = group.name;
    groupSelect.append(option);
  }
  groupSelect.value = previousGroup || proxyWhitelist.group;
  if (!groupSelect.value && groupSelect.options.length) groupSelect.selectedIndex = 0;
  const groups = $("mihomo-groups");
  groups.replaceChildren();
  for (const group of data.groups || []) {
    const card = node("div", "mihomo-group", "");
    const info = node("div", "", "");
    info.append(node("strong", "", group.name), node("small", "", `当前：${group.now || "未选择"}`));
    const actions = node("div", "mihomo-group-actions", "");
    const select = node("select", "", "");
    select.setAttribute("aria-label", `${group.name} 选择节点`);
    for (const name of group.options || []) {
      const option = node("option", "", name);
      option.value = name;
      select.append(option);
    }
    select.value = group.now;
    select.disabled = group.type !== "Selector";
    select.onchange = async () => {
      select.disabled = true;
      try {
        await request("/api/mihomo/group", mutation("PUT", { group: group.name, name: select.value }));
        message("mihomo-message", `${group.name} 已切换到 ${select.value}`);
        await loadMihomo();
      } catch (error) {
        message("mihomo-message", error.message, true);
        select.value = group.now;
      } finally { select.disabled = false; }
    };
    actions.append(select);
    if (group.now && !["DIRECT", "REJECT"].includes(group.now)) {
      const delayButton = node("button", "button secondary", "测速");
      delayButton.type = "button";
      delayButton.onclick = async () => {
        delayButton.disabled = true;
        try {
          const result = await request("/api/mihomo/delay", mutation("POST", { name: group.now }));
          message("mihomo-message", `${group.now} 延迟 ${result.delay} ms`);
        } catch (error) { message("mihomo-message", error.message, true); }
        finally { delayButton.disabled = false; }
      };
      actions.append(delayButton);
    }
    card.append(info, actions);
    groups.append(card);
  }
  if (!groups.children.length) groups.append(node("p", "subtle", "暂无可手动切换的代理组。添加订阅后会显示在这里。"));
  const rows = $("mihomo-recent");
  rows.replaceChildren();
  for (const item of data.recent || []) {
    const row = node("tr", "", "");
    for (const value of [item.host || item.destination || "—", item.network || "—", item.rule || "—", `${formatBytes(item.upload)} ↑ · ${formatBytes(item.download)} ↓`]) {
      row.append(node("td", "", value));
    }
    rows.append(row);
  }
  if (!rows.children.length) {
    const row = node("tr", "", ""), cell = node("td", "empty-row", "暂无 Mihomo 活动连接");
    cell.colSpan = 4;
    row.append(cell);
    rows.append(row);
  }
}

let proxyWhitelist = { domains: [], group: "" };
function renderProxyWhitelist() {
  const list = $("proxy-domains");
  list.replaceChildren();
  for (const domain of proxyWhitelist.domains) {
    const row = node("div", "proxy-domain", "");
    const label = node("code", "", domain);
    const remove = node("button", "delete-button", "移除");
    remove.type = "button";
    remove.onclick = () => {
      proxyWhitelist.domains = proxyWhitelist.domains.filter((item) => item !== domain);
      renderProxyWhitelist();
    };
    row.append(label, remove);
    list.append(row);
  }
  if (!proxyWhitelist.domains.length) list.append(node("p", "subtle", "白名单为空：所有 LAN 设备继续由 Pilot 直连。"));
}
async function loadProxyWhitelist() {
  try {
    proxyWhitelist = await request("/api/mihomo/whitelist");
    renderProxyWhitelist();
    if (proxyWhitelist.group) $("proxy-group").value = proxyWhitelist.group;
  } catch (error) { message("proxy-message", error.message, true); }
}
$("proxy-add").onclick = () => {
  const raw = $("proxy-domain").value.trim().toLowerCase().replace(/\.$/, "");
  if (!/^[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(raw) || raw.includes("..")) {
    message("proxy-message", "请只填写域名，例如 google.com", true);
    return;
  }
  if (!proxyWhitelist.domains.includes(raw)) proxyWhitelist.domains.push(raw);
  $("proxy-domain").value = "";
  message("proxy-message", "点击保存白名单后生效");
  renderProxyWhitelist();
};
$("proxy-domain").onkeydown = (event) => { if (event.key === "Enter") $("proxy-add").click(); };
$("proxy-save").onclick = async () => {
  const button = $("proxy-save");
  button.disabled = true;
  try {
    proxyWhitelist = await request("/api/mihomo/whitelist", mutation("PUT", { domains: proxyWhitelist.domains, group: $("proxy-group").value }));
    renderProxyWhitelist();
    message("proxy-message", "白名单已保存并应用");
    await loadMihomo();
  } catch (error) { message("proxy-message", error.message, true); }
  finally { button.disabled = false; }
};

async function loadMihomo() {
  try { renderMihomo(await request("/api/mihomo")); }
  catch (error) { message("mihomo-message", error.message, true); }
}

$("mihomo-refresh").onclick = loadMihomo;
$("mihomo-reload").onclick = async () => {
  const button = $("mihomo-reload");
  button.disabled = true;
  try {
    await request("/api/mihomo/reload", mutation("POST", {}));
    message("mihomo-message", "配置已重载");
    await loadMihomo();
  } catch (error) { message("mihomo-message", error.message, true); }
  finally { button.disabled = false; }
};
$("mihomo-import").onclick = async () => {
  const button = $("mihomo-import");
  button.disabled = true;
  try {
    await request("/api/mihomo/subscription", mutation("POST", { url: $("mihomo-subscription").value.trim() }));
    message("mihomo-config-message", "订阅已保存并应用");
    $("mihomo-subscription").value = "";
    await loadMihomo();
  } catch (error) { message("mihomo-config-message", error.message, true); }
  finally { button.disabled = false; }
};
$("mihomo-update").onclick = async () => {
  const button = $("mihomo-update");
  button.disabled = true;
  try {
    await request("/api/mihomo/subscription/refresh", mutation("POST", {}));
    message("mihomo-config-message", "订阅已更新并应用");
    await loadMihomo();
  } catch (error) { message("mihomo-config-message", error.message, true); }
  finally { button.disabled = false; }
};
setInterval(() => {
  if (!$("mihomo-page").hidden) loadMihomo();
}, 8000);
