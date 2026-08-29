export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

export type JsonRequestOptions = {
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  token?: string;
  credentials?: RequestCredentials;
  cache?: RequestCache;
  csrf?: {
    cookieName: string;
    headerName: string;
  };
};

export async function requestJson<T>(url: string, options: JsonRequestOptions = {}): Promise<T> {
  const method = options.method ?? "GET";
  const headers: Record<string, string> = {
    ...options.headers,
    "Content-Type": "application/json",
  };

  if (options.token) {
    headers.Authorization = `Bearer ${options.token}`;
  } else if (options.csrf && method !== "GET" && method !== "HEAD") {
    const csrf = readCookie(options.csrf.cookieName);
    if (csrf) {
      headers[options.csrf.headerName] = csrf;
    }
  }

  const response = await fetch(url, {
    method,
    headers,
    credentials: options.credentials,
    cache: options.cache,
    signal: options.signal,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });

  const text = await response.text();
  let payload: unknown = null;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      if (response.ok) {
        throw new ApiError(response.status, "Invalid JSON response");
      }
      payload = null;
    }
  }

  if (!response.ok) {
    throw new ApiError(response.status, errorMessage(payload, response.statusText));
  }

  return payload as T;
}

export async function requestBytes(url: string, options: JsonRequestOptions = {}): Promise<Uint8Array> {
  const headers: Record<string, string> = { ...options.headers };
  if (options.token) {
    headers.Authorization = `Bearer ${options.token}`;
  }
  const response = await fetch(url, {
    method: options.method ?? "GET",
    headers,
    credentials: options.credentials,
    cache: options.cache,
    signal: options.signal,
  });
  const body = new Uint8Array(await response.arrayBuffer());
  if (!response.ok) {
    let payload: unknown = null;
    const text = new TextDecoder().decode(body);
    if (text) {
      try {
        payload = JSON.parse(text);
      } catch {
        payload = null;
      }
    }
    throw new ApiError(response.status, errorMessage(payload, response.statusText));
  }
  return body;
}

function readCookie(name: string): string {
  const prefix = `${name}=`;
  return document.cookie
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith(prefix))
    ?.slice(prefix.length) ?? "";
}

function errorMessage(payload: unknown, fallback: string): string {
  if (payload && typeof payload === "object") {
    const record = payload as Record<string, unknown>;
    if (typeof record.message === "string") {
      return record.message;
    }
    if (typeof record.error === "string") {
      return record.error;
    }
  }
  return fallback || "Request failed";
}
