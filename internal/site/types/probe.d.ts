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
  vendor?: string;
  product?: string;
  /** Hypervisor when this machine is a guest: vmware, kvm, hyperv, … */
  virt?: string;
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
  netRxPps?: number;
  netTxPps?: number;
  /** counts since the previous sample */
  netErrs?: number;
  netDrops?: number;
  /** total/udp/other: socket counts by protocol (agent >= 1.15.0) */
  tcp?: { established: number; timeWait: number; listen: number; total?: number; udp?: number; other?: number; outSegs: number; retrans: number };
  retransPct?: number;
  load: [number, number, number];
  tempMax?: number;
  procs: number;
  latencyMs: number;
  disks?: DiskUsage[];
  temps?: TempSensor[];
  nics?: NicInfo[];
  /** per-NIC rates once the agent reports cumulative rx/tx */
  netNics?: { name: string; rxBps: number; txBps: number; rxBytes?: number; txBytes?: number }[];
  /** agents with the docker cap (>= 1.17.0) */
  docker?: DockerSample;
}

/** monitor.DockerSample */
interface DockerSample {
  engine?: string;
  version?: string;
  running: number;
  total: number;
  /** the engine did not answer this sample */
  error?: string;
  containers: ContainerSample[];
  images?: DockerImage[];
}

interface DockerImage {
  id: string;
  name: string;
  tags?: string[];
  size: number;
  created?: number;
  inUse?: number;
}

/** monitor.ContainerSample; rates are absent until two samples in a row. */
interface ContainerSample {
  id: string;
  name: string;
  image: string;
  state: string;
  status?: string;
  health?: "healthy" | "unhealthy" | "starting";
  project?: string;
  service?: string;
  compose?: string;
  /** unix seconds, container create time */
  created?: number;
  /** published ports, e.g. "127.0.0.1:5432→5432/tcp, 8080→80/tcp" */
  ports?: string;
  /** the registry has a newer image for this tag */
  update?: boolean;
  stats?: boolean;
  /** 100 = one core, like docker stats */
  cpuPct?: number;
  memUsed?: number;
  memLimit?: number;
  netRxBps?: number;
  netTxBps?: number;
  blkReadBps?: number;
  blkWriteBps?: number;
  pids?: number;
}

/** monitor.ContainerSeries from GET /monitor/systems/{id}/containers/series */
interface ContainerSeries {
  stepSec: number;
  t: number[];
  containers: { name: string; memLimit: number; cpu: (number | null)[]; mem: (number | null)[]; netRx: (number | null)[]; netTx: (number | null)[]; blkRead: (number | null)[]; blkWrite: (number | null)[]; pids: (number | null)[] }[];
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
  /** service | console | systemd | launchd | docker */
  runner?: string;
  /** device credential; absent when the hub runs with PROBE_HUB_AUTH=off */
  credential?: "active" | "pending" | "none";
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
  netRxMax: number[];
  netTxMax: number[];
  /** bytes moved within each bucket */
  netRxBytes: number[];
  netTxBytes: number[];
  /** the rest are null before agent 1.14.0 */
  netRxPps: (number | null)[];
  netTxPps: (number | null)[];
  /** counts within each bucket */
  netErrs: (number | null)[];
  netDrops: (number | null)[];
  tcpEstab: (number | null)[];
  tcpTimeWait: (number | null)[];
  tcpListen: (number | null)[];
  /** percent */
  retrans: (number | null)[];
  latency: (number | null)[];
  /** sums within each bucket; bytes on macOS */
  tcpOutSegs: (number | null)[];
  tcpRetransSegs: (number | null)[];
  /** socket counts by protocol (agent >= 1.15.0) */
  connTcp: (number | null)[];
  connUdp: (number | null)[];
  connOther: (number | null)[];
  load1: number[];
  load5: (number | null)[];
  load15: (number | null)[];
  temp: (number | null)[];
  procs: number[];
  /** mount -> used bytes */
  diskParts: Record<string, (number | null)[]>;
  /** per-NIC rates; missing until the agent reports counters */
  netNics?: Record<string, { rx: (number | null)[]; tx: (number | null)[]; rxBytes: (number | null)[]; txBytes: (number | null)[] }>;
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
