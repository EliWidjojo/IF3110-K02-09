// Klien API backend Go: semua request same-origin dengan cookie session HttpOnly.
// Request yang mengubah state selalu mengambil token CSRF baru yang terikat ke session.

export class ApiError extends Error {
  status: number;
  code?: string;
  constructor(message: string, status: number, code?: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

type Options = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
};

export const UNAUTHORIZED_EVENT = "atlas:unauthorized";

async function send<T>(path: string, options: Options = {}): Promise<T> {
  const method = options.method ?? "GET";
  const headers: Record<string, string> = { ...options.headers };
  if (method !== "GET") headers["X-CSRF-Token"] = await csrfToken(options.signal);
  let body: string | undefined;
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(options.body);
  }
  const response = await fetch(path, { method, headers, body, signal: options.signal, credentials: "same-origin", cache: "no-store" });
  let payload: any = {};
  if (response.status !== 204) {
    try {
      payload = await response.json();
    } catch {
      throw new ApiError("Respons server tidak dapat dibaca.", response.status);
    }
  }
  if (!response.ok) {
    const error = payload?.error;
    const message = typeof error === "string" ? error : error?.message ?? `Permintaan ditolak (${response.status}).`;
    if (response.status === 401 && !path.startsWith("/api/auth/")) window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    throw new ApiError(message, response.status, typeof error === "object" ? error?.code : undefined);
  }
  return (payload && "data" in payload ? payload.data : payload) as T;
}

async function csrfToken(signal?: AbortSignal): Promise<string> {
  const result = await send<{ csrf_token: string }>("/api/auth/csrf", { signal });
  if (!result.csrf_token) throw new ApiError("Token CSRF tidak tersedia.", 500);
  return result.csrf_token;
}

const enc = encodeURIComponent;
const page = (limit: number, offset: number) => `limit=${limit}&offset=${offset}`;

// ---------- Tipe data ----------

export type AccessLevel = "View Only" | "View + Edit";
export type Driver = "postgres" | "mariadb" | "mysql";

export type Me = {
  id: number;
  username: string;
  is_active: boolean;
  is_platform_admin: boolean;
  leader_groups: { id: number; name: string }[];
};

export type AccessibleDatabase = { database_id: number; name: string; driver: Driver; access_level: AccessLevel };
export type TypedValue = { type: string; encoding?: string; value: string };
export type CellValue = null | string | number | boolean | TypedValue | unknown;

export type TableMetadata = {
  schema: string;
  name: string;
  type: string;
  columns: { name: string; ordinal_position: number; data_type: string; nullable: boolean; default: string | null; identity: string; generated: string; generation_expression: string | null }[];
  constraints: { name: string; type: string; definition: string; columns: string[]; reference: { schema: string; table: string; columns: string[] } | null; validated: boolean }[];
  indexes: { name: string; definition: string; method: string; unique: boolean; primary: boolean; valid: boolean; ready: boolean; predicate: string | null }[];
};

export type TableData = {
  columns: { name: string; data_type: string; value_type: string }[];
  rows: CellValue[][];
  pagination: { limit: number; offset: number; returned_rows: number; has_more: boolean };
  order_by: string[];
  stable_order: boolean;
};

export type Attempt = {
  id: number;
  version: number;
  executed_by: number;
  executed_at: string;
  finished_at: string | null;
  status: string;
  query_sql: string;
  sql_state: string;
  error_code: string;
  // Diisi backend rollback bila tersedia: operasi dan attempt yang dibatalkan.
  operation?: "apply" | "rollback" | "forward";
  reverts_attempt_id?: number | null;
};

export type JobStatus =
  | "QUEUED" | "RUNNING" | "PARTIAL" | "READY" | "RECOVERING" | "BLOCKED"
  | "SUCCESS" | "FAILED" | "NEEDS_REVIEW" | "PARTIALLY_REVERTED" | "REVERTED";

export type Submission = {
  version: number;
  query_sql: string;
  down_sql?: string;
  batch_size?: number;
  operation?: "apply" | "rollback" | "forward";
  source_job?: number;
  non_atomic_ddl: boolean;
};

export type Job = {
  id: number;
  database_id: number;
  submitted_by: number;
  submission: Submission;
  driver: Driver;
  status: JobStatus;
  worker_id: string | null;
  attempt_id: number | null;
  committed_batches: number;
  committed_rows: number;
  reverted_batches: number;
  reverted_rows: number;
  retry_count: number;
  recovery_count: number;
  cancel_origin?: string;
  cancel_waiting?: boolean;
  next_attempt_at?: string;
  created_at: string;
  updated_at: string;
  started_at: string | null;
  finished_at: string | null;
  error_code: string;
  status_url: string;
};

export type Approval = { group_id: number; group_name: string; leader_id: number | null; leader_username: string | null; approved: boolean; approved_at: string | null };

export type PlanNode = {
  node_type: string;
  relation?: string;
  index?: string;
  join?: string;
  startup_cost: string | null;
  total_cost: string | null;
  estimated_rows: string | null;
  actual_rows: string | null;
  actual_time_ms: string | null;
  children: PlanNode[] | null;
};
export type Analysis = { analyze: boolean; statement_type: string; plan: PlanNode | null; planning_time_ms: string | null; execution_time_ms: string | null; raw_json: string };
export type DiffResult = { differences: { object: string; kind: string; message: string; requires_decision: boolean }[]; sql: string; limitations: string };

export type User = { id: number; username: string; is_active: boolean; is_platform_admin: boolean };
export type Group = { id: number; name: string };
export type Grant = { group_id: number; database_id: number; access_level: AccessLevel };
export type Connection = {
  id: number;
  name: string;
  driver: Driver;
  host: string;
  port: number;
  database_name: string;
  username: string;
  audit_username: string;
  ssl_mode: string;
  ssl_root_cert: string;
  password_configured: boolean;
};

// ---------- Endpoint ----------

export const api = {
  login: (username: string, password: string) => send<{ csrf_token: string }>("/api/auth/login", { method: "POST", body: { username, password } }),
  logout: () => send<void>("/api/auth/logout", { method: "POST" }),
  me: (signal?: AbortSignal) => send<Me>("/api/platform/me", { signal }),

  databases: () => send<AccessibleDatabase[]>("/api/viewer/databases"),
  schemas: (db: number) => send<{ name: string }[]>(`/api/viewer/databases/${db}/schemas`),
  tables: (db: number, schema: string) => send<{ schema: string; name: string; type: string }[]>(`/api/viewer/databases/${db}/schemas/${enc(schema)}/tables`),
  table: (db: number, schema: string, table: string) => send<TableMetadata>(`/api/viewer/databases/${db}/schemas/${enc(schema)}/tables/${enc(table)}`),
  tableData: (db: number, schema: string, table: string, limit: number, offset: number) =>
    send<TableData>(`/api/viewer/databases/${db}/schemas/${enc(schema)}/tables/${enc(table)}/data?${page(limit, offset)}`),
  explain: (db: number, query: string, analyze: boolean) => send<Analysis>(`/api/viewer/databases/${db}/explain`, { method: "POST", body: { query, analyze } }),

  history: (db: number, limit: number, offset: number) => send<{ attempts: Attempt[]; latest_version: number }>(`/api/databases/${db}/migrations/history?${page(limit, offset)}`),
  diff: (db: number, desired: unknown) => send<DiffResult>(`/api/databases/${db}/migrations/diff`, { method: "POST", body: desired }),
  jobs: (db: number, limit: number, offset: number) => send<Job[]>(`/api/databases/${db}/migrations/jobs?${page(limit, offset)}`),
  job: (db: number, id: number) => send<Job>(`/api/databases/${db}/migrations/jobs/${id}`),
  approvals: (db: number, id: number) => send<Approval[]>(`/api/databases/${db}/migrations/jobs/${id}/approvals`),
  submitJob: (db: number, body: Record<string, unknown>, key: string) =>
    send<Job>(`/api/databases/${db}/migrations/jobs`, { method: "POST", body, headers: { "Idempotency-Key": key } }),
  approve: (db: number, id: number, group: number, round: number) => send<void>(`/api/databases/${db}/migrations/jobs/${id}/approvals/${group}?retry_count=${round}`, { method: "PUT" }),
  retryBatch: (db: number, id: number, n: number) => send<Job>(`/api/databases/${db}/migrations/jobs/${id}/retries/${n}`, { method: "PUT" }),
  cancelBatch: (db: number, id: number, n: number) => send<Job>(`/api/databases/${db}/migrations/jobs/${id}/cancellations/${n}`, { method: "PUT" }),
  recover: (db: number, id: number) => send<Job>(`/api/databases/${db}/migrations/jobs/${id}/recovery`, { method: "POST" }),

  users: (limit: number, offset: number) => send<User[]>(`/api/admin/users?${page(limit, offset)}`),
  createUser: (body: { username: string; password: string; is_platform_admin: boolean }) => send<User>("/api/admin/users", { method: "POST", body }),
  updateUser: (id: number, body: Partial<Pick<User, "username" | "is_active" | "is_platform_admin">>) => send<User>(`/api/admin/users/${id}`, { method: "PATCH", body }),
  deactivateUser: (id: number) => send<User>(`/api/admin/users/${id}/deactivate`, { method: "POST" }),

  groups: (limit: number, offset: number) => send<Group[]>(`/api/admin/groups?${page(limit, offset)}`),
  group: (id: number) => send<Group>(`/api/admin/groups/${id}`),
  createGroup: (name: string) => send<Group>("/api/admin/groups", { method: "POST", body: { name } }),
  renameGroup: (id: number, name: string) => send<Group>(`/api/admin/groups/${id}`, { method: "PATCH", body: { name } }),
  deleteGroup: (id: number) => send<void>(`/api/admin/groups/${id}`, { method: "DELETE" }),
  members: (id: number, limit: number, offset: number) => send<User[]>(`/api/admin/groups/${id}/members?${page(limit, offset)}`),
  addMember: (id: number, userId: number) => send<unknown>(`/api/admin/groups/${id}/members`, { method: "POST", body: { user_id: userId } }),
  removeMember: (id: number, userId: number) => send<void>(`/api/admin/groups/${id}/members/${userId}`, { method: "DELETE" }),
  leader: (id: number) => send<{ user_id: number | null }>(`/api/admin/groups/${id}/leader`),
  setLeader: (id: number, userId: number) => send<void>(`/api/admin/groups/${id}/leader`, { method: "PUT", body: { user_id: userId } }),
  grants: (id: number, limit: number, offset: number) => send<Grant[]>(`/api/admin/groups/${id}/database-access?${page(limit, offset)}`),
  setGrant: (id: number, db: number, level: AccessLevel) => send<Grant>(`/api/admin/groups/${id}/database-access/${db}`, { method: "PUT", body: { access_level: level } }),
  revokeGrant: (id: number, db: number) => send<void>(`/api/admin/groups/${id}/database-access/${db}`, { method: "DELETE" }),

  connections: (limit: number, offset: number) => send<Connection[]>(`/api/admin/databases?${page(limit, offset)}`),
  createConnection: (body: Record<string, unknown>) => send<Connection>("/api/admin/databases", { method: "POST", body }),
  updateConnection: (id: number, body: Record<string, unknown>) => send<Connection>(`/api/admin/databases/${id}`, { method: "PATCH", body }),
  deleteConnection: (id: number) => send<void>(`/api/admin/databases/${id}`, { method: "DELETE" }),
  checkConnection: (id: number) => send<{ database_id: number; connected: boolean }>(`/api/admin/databases/${id}/check-connection`, { method: "POST" }),
};

// Status yang masih bergerak sehingga detail job perlu dipantau berkala.
export const ACTIVE_JOB_STATUSES: JobStatus[] = ["QUEUED", "RUNNING", "READY", "RECOVERING", "BLOCKED"];
