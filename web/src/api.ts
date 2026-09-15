export interface Health {
  status: string;
  version: string;
  mode: string;
  hostname: string;
  architecture: string;
  os: string;
}
export interface Sample {
  time: number;
  cpu: number;
  memoryTotal: number;
  memoryUsed: number;
  uptime: number;
  load: string;
  sampleSeconds: number;
  warnings: string[];
  interfaces: {
    name: string;
    mac: string;
    addresses: string[];
    state: string;
    rxBytes: number;
    txBytes: number;
    rxRate: number;
    txRate: number;
  }[];
  history: { time: number; cpu: number; rx: number; tx: number }[];
}
export async function getMonitor(): Promise<Sample> {
  const r = await request("/api/monitor");
  if (!r.ok) throw new Error("等待系统采样或服务恢复");
  return r.json();
}
export async function getHealth(): Promise<Health> {
  const r = await request("/api/health");
  if (!r.ok) throw new Error("服务暂时无法连接");
  return r.json();
}
async function request(path: string): Promise<Response> {
  try {
    return await fetch(path, { signal: AbortSignal.timeout(5000) });
  } catch {
    throw new Error("无法连接监控服务，请检查虚拟机与网络");
  }
}
