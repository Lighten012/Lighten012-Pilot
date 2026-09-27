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
  for (const name of ["network", "dns"]) {
    $(`${name}-page`).hidden = name !== page;
  }
  document.querySelectorAll("[data-page]").forEach((button) => {
    button.classList.toggle("active", button.dataset.page === page);
  });
  $("page-name").textContent = page === "network" ? "WAN / LAN" : "DNS 解析";
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

function renderDevices(devices) {
  const body = $("devices");
  body.replaceChildren();
  $("device-count").textContent = `${devices.length} 台设备`;
  if (!devices.length) {
    const row = node("tr", "", ""), cell = node("td", "empty-row", "暂未发现 LAN 设备，设备通信后可刷新查看。");
    cell.colSpan = 3;
    row.append(cell);
    body.append(row);
    return;
  }
  const labels = { REACHABLE: "活跃", STALE: "最近出现", DELAY: "正在确认", PROBE: "正在确认", PERMANENT: "固定", NOARP: "固定" };
  for (const device of devices) {
    const row = node("tr", "", "");
    for (const value of [device.ip, device.mac]) {
      const cell = node("td", "", "");
      cell.append(node("code", "", value));
      row.append(cell);
    }
    const stateCell = node("td", "", "");
    stateCell.append(node("span", `device-state${device.state === "REACHABLE" ? " active" : ""}`, labels[device.state] || device.state));
    row.append(stateCell);
    body.append(row);
  }
}

async function loadDevices() {
  const button = $("refresh-devices");
  button.disabled = true;
  message("devices-message", "");
  try {
    renderDevices(await request("/api/lan/devices"));
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
    for (const value of [record.name, record.type, record.value, record.mac || "所有设备", String(record.ttl)]) {
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
    for (const key of ["queries", "local", "forwarded"]) $(key).textContent = data.stats[key].toLocaleString();
    renderRecords();
  } catch (error) {
    message("message", error.message, true);
  }
}

$("add-form").onsubmit = (event) => {
  event.preventDefault();
  const name = $("name").value.trim().replace(/\.$/, "").toLowerCase();
  const type = $("type").value, value = $("value").value.trim(), ttl = Number($("ttl").value);
  const mac = $("mac").value.trim().toLowerCase().replace(/-/g, ":");
  if (records.some((record) => record.name === name && record.type === type && (record.mac || "") === mac)) {
    message("message", "同一域名、类型和设备只能添加一次", true);
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
    const config = await request("/api/config", mutation("PUT", { upstream: $("upstream").value.trim(), records }));
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
    for (const key of ["queries", "local", "forwarded"]) $(key).textContent = data.stats[key].toLocaleString();
  } catch { /* keep the last visible sample */ }
}, 10000);
