export interface Port {
  interface: string;
  mode: "dhcp" | "static";
  address: string;
  gateway: string;
}
export interface NetworkConfig {
  wan: Port;
  lan: Port;
}
export interface Device {
  name: string;
  addresses: string[];
  state: string;
  protected: boolean;
  current: Port | null;
  reason: string;
}
export interface NetworkState {
  inventory: {
    devices: Device[];
    routes: { dst: string; dev: string; gateway: string }[];
    revision: string;
    backend: string;
    blocked: string;
  };
  saved: NetworkConfig | null;
  draft: NetworkConfig | null;
  pending: {
    id: string;
    phase: string;
    deadline: number;
    config: NetworkConfig;
  } | null;
  event: string;
  serverTime: number;
}
export interface NetworkPlan {
  config: NetworkConfig;
  revision: string;
  token: string;
  changes: {
    interface: string;
    before: Port | null;
    after: Port;
    changed: boolean;
    file: string;
  }[];
  warnings: string[];
}
export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}
export async function networkRequest<T>(
  path: string,
  body?: unknown,
): Promise<T> {
  let r: Response;
  try {
    r = await fetch("/api/" + path, {
      method: body === undefined ? "GET" : "POST",
      headers:
        body === undefined
          ? {}
          : { "Content-Type": "application/json", "X-Pilot-Request": "1" },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(115000),
    });
  } catch {
    throw new APIError("无法连接管理服务。待确认的配置仍会由后台自动回滚。", 0);
  }
  const data = await r.json();
  if (!r.ok) throw new APIError(data.error || "请求未完成", r.status);
  return data;
}
