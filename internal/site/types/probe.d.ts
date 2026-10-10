// JSON shapes served under /v1/probe, mirroring internal/monitor and
// internal/guest/proto.go. Type-check only: nothing here is shipped.

/** GET /monitor/config */
interface HubConfig {
  intervalSec: number;
  retentionSec: number;
  history: boolean;
  authRequired: boolean;
}

/** guest.SysInfo */
interface HostInfo {
  hostname?: string;
  os?: string;
  platform?: string;
  platformVersion?: string;
  kernel?: string;
  arch?: string;
  cpuModel?: string;
  cores?: number;
  threads?: number;
  bootTime?: number;
}

/** guest.DiskUsage */
interface DiskUsage {
  mount: string;
  fs?: string;
  total: number;
  used: number;
}

/** guest.TempSensor */
interface TempSensor {
  name: string;
  c: number;
}

/** guest.NICInfo */
interface NicInfo {
  name: string;
  mac?: string;
  ipv4?: string;
  linkMbps?: number;
  up: boolean;
}

/** monitor.Sample */
interface Sample {
  ts: string;
  cpuPct: number;
  memUsed: number;
  memTotal: number;
  memPct: number;
  swapUsed: number;
  swapTotal: number;
  diskUsed: number;
  diskTotal: number;
  diskPct: number;
  rootMount?: string;
  diskReadBps: number;
  diskWriteBps: number;
  netRxBps: number;
  netTxBps: number;
  load: [number, number, number];
  tempMax?: number;
  procs: number;
  latencyMs: number;
  disks?: DiskUsage[];
  temps?: TempSensor[];
  nics?: NicInfo[];
}

/** monitor.Event */
interface SystemEvent {
  ts: string;
  kind: string;
  category: "lifecycle" | "change" | "audit";
  level: "info" | "warn";
  /** agent source IP at the time */
  ip?: string;
  /** agent version at the time */
  version?: string;
  /** operator IP (audit events) */
  actor?: string;
  detail?: string;
}

/** guest.DeviceFacts: short digests of the fingerprint inputs. */
interface DeviceFacts {
  mid?: string;
  hw?: string;
  mac?: string;
}

/** monitor.SystemView; events only on GET /monitor/systems/{id}. */
interface SystemView {
  /** device fingerprint; BIOS UUID for agents before 1.8 */
  id: string;
  biosUuid: string;
  device?: DeviceFacts;
  name: string;
  hostname: string;
  version: string;
  remoteAddr: string;
  tags: string[] | null;
  online: boolean;
  firstSeen: string;
  lastSeen: string;
  connectedAt: string;
  info: HostInfo;
  latest?: Sample;
  events?: SystemEvent[];
  /** live session only */
  caps?: string[];
  /** service | console | systemd | launchd */
  runner?: string;
}

/** GET /releases */
interface Releases {
  latest?: string;
  versions: { version: string; created: string; platforms: string[] }[];
}

interface UpgradeResult {
  ok: boolean;
  from: string;
  to: string;
  message?: string;
}

interface BatchResult {
  id: string;
  ok: boolean;
  from?: string;
  to?: string;
  message?: string;
}

/** guest.UpgradeProgress from GET /upgrade. */
interface UpgradeProgress {
  id: string;
  from?: string;
  to?: string;
  stage: "queued" | "download" | "verify" | "selftest" | "restart" | "done" | "failed" | "rolled_back";
  done?: number;
  total?: number;
  message?: string;
  started: string;
  updated: string;
}

/** monitor.Series; every array is aligned to t (unix ms). */
interface Series {
  stepSec: number;
  t: number[];
  cpu: number[];
  cpuMax: number[];
  mem: number[];
  memTotal: number[];
  swap: number[];
  disk: number[];
  diskTotal: number[];
  diskRead: number[];
  diskWrite: number[];
  netRx: number[];
  netTx: number[];
  load1: number[];
  load5: (number | null)[];
  load15: (number | null)[];
  temp: (number | null)[];
  procs: number[];
  /** mount -> used bytes */
  diskParts: Record<string, (number | null)[]>;
}

/** guest.ProcInfo */
interface ProcInfo {
  pid: number;
  name: string;
  cpuPct?: number;
  memMB?: number;
  status?: string;
  cmdline?: string;
}

/** POST /sessions/{bios}/term and /display */
interface WsTicket {
  ws: string;
}

/** guest.FileEntry */
interface FileEntry {
  name: string;
  path: string;
  isDir: boolean;
  size?: number;
  mtimeUtc?: string;
  hidden?: boolean;
  link?: boolean;
}

/** guest.FilesListResult */
interface FilesListResult {
  path: string;
  parent?: string;
  staging: string;
  home?: string;
  sep?: string;
  roots?: string[];
  /** agent supports op "trash" */
  trash?: boolean;
  entries: FileEntry[];
}

/** guest.FilesOpRequest */
interface FilesOpRequest {
  op: "mkdir" | "rename" | "delete" | "trash" | "zip";
  path: string;
  to?: string;
  confirm?: boolean;
}

/** guest.FilesOpResult */
interface FilesOpResult {
  ok: boolean;
  path?: string;
  size?: number;
  count?: number;
}

/** guest.TrashRequest */
interface TrashRequest {
  op: "list" | "restore" | "purge" | "empty";
  ids?: string[];
  confirm?: boolean;
}

/** guest.TrashItem */
interface TrashItem {
  id: string;
  name: string;
  /** original full path; empty if unknown */
  orig?: string;
  isDir?: boolean;
  size?: number;
  deletedUtc?: string;
}

/** guest.TrashResult */
interface TrashResult {
  location?: string;
  items: TrashItem[];
  done?: number;
  paths?: string[];
  failed?: { id: string; message: string }[];
}
