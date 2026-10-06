<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ArrowDownToLine, ArrowLeft, ArrowRight, Bell, Check, ChevronDown, ChevronRight, Cloud, Database, File, FileText, Folder, Grid2X2, HardDrive, LayoutGrid, ListFilter, LoaderCircle, Moon, MoreHorizontal, Play, Plus, RefreshCw, Search, Settings, SlidersHorizontal, Sun, Upload, WandSparkles, Zap } from 'lucide-vue-next'

type PrototypeSection = 'dashboard' | 'transfers' | 'records' | 'organize' | 'uploads' | 'policies' | 'cache' | 'files' | 'logs' | 'settings'
type Job = { id: string; title?: string; source?: string; status: string; created_at?: string; share_url?: string; error?: string; tmdb_kind?: string; stage_cid?: string; [key: string]: unknown }
type LocalJob = { id: string; title: string; source: string; status: string; createdAt: string; size: string; progress: number; quality: string; path: string; shareUrl: string; shareCode?: string; duplicate?: boolean; submissionMessage?: string; shareUpdate?: boolean; error?: string; kind: string; tmdbId: number; stageCID: string; remoteIDs: string[]; cloudSourcePath: string; cloudTargetPath: string; cloudLoaded: boolean; cloudLoading: boolean }
type FileItem = { name: string; type: 'folder' | 'file'; size: string; updated: string; path?: string }
type Account = { used: number; total: number; username: string; vip: string; expire: string }
type LogItem = { time: string; level: string; category: string; message: string; tone: string }
type OrganizationRecord = { id: string; task_id: string; file_id?: string; remote_id: string; inbox_id?: string; name: string; source_path?: string; output_path?: string; cloud_source_path?: string; cloud_target_path?: string; cloud_operation?: string; kind?: string; tmdb_id?: number; season?: number; episode?: number; episode_end?: number; quality?: { resolution?: number; hdr?: string; codec?: string; audio?: string }; status?: string; error?: string; updated_at?: string }
type TMDBEpisodePreview = { episode_number: number; name?: string; overview?: string; air_date?: string; still_path?: string }
type TMDBSeasonPreview = { season_number?: number; name?: string; episode_count?: number; episodes?: TMDBEpisodePreview[] }
type TMDBPreview = { id: number; kind: 'movie' | 'tv'; title: string; original_title?: string; year?: number; release_date?: string; first_air_date?: string; overview?: string; poster_path?: string; seasons?: TMDBSeasonPreview[]; season?: number; episode?: number; episodeTitle?: string; episodeOverview?: string; episodeAirDate?: string }
type MatchTarget = { source: 'transfer' | 'organization' | 'organization-batch'; job: LocalJob; record?: OrganizationRecord }
type DeletePreviewItem = { remoteID: string; title: string; output?: string; source?: string; cloud?: string; retained?: string[]; assets?: string[]; digest: string }
type OrganizationRow = { key: string; job: LocalJob; record?: OrganizationRecord }
type ClassificationRule = { name: string; enabled: boolean; kind: 'movie' | 'tv'; countries: string[]; languages: string[]; genres: number[]; keyword: string; target: string }
type ClassificationConfig = { movie_root: string; tv_root: string; rules: ClassificationRule[] }
type FileOperation = 'mkdir' | 'rename' | 'move' | 'copy' | 'delete'
type SettingsTab = 'general' | 'directories' | 'uploads' | 'pan' | 'tmdb' | 'jellyfin' | 'wecom' | 'cms' | 'advanced' | 'users'
type ManagedUser = { username: string; role: 'admin' | 'operator' | 'viewer'; enabled: boolean; created_at?: string; updated_at?: string }
type CMSStatus = { enabled?: boolean; resolver_ready?: boolean; legacy_enabled?: boolean; legacy_listen?: string; read_only_active?: boolean; restart_required?: boolean; imported?: number; mode?: string; cookie_configured?: boolean }
type UploadConfig = { enabled: boolean; local_dir: string; target_cid: string; channel: 'auto' | 'open_oauth' | 'cookie'; scan_interval_seconds: number; stable_seconds: number }
type UploadSession = { id: string; owner_username?: string; filename: string; safe_filename?: string; total_size: number; received_size: number; sha256?: string; target_cid?: string; channel?: string; final_path?: string; status: string; error?: string; upload_task_id?: string; created_at?: string; updated_at?: string; expires_at?: string }

const sections: Array<{ id: PrototypeSection; label: string; group: string; icon: string }> = [
  { id: 'dashboard', label: '运行概览', group: '工作台', icon: 'grid' },
  { id: 'transfers', label: '转存与整理', group: '工作台', icon: 'arrow' },
  { id: 'records', label: '记录中心', group: '工作台', icon: 'records' },
  { id: 'organize', label: '媒体整理', group: '工作台', icon: 'wand' },
  { id: 'uploads', label: '本地上传', group: '工作台', icon: 'upload' },
  { id: 'files', label: '目录管理', group: '资源', icon: 'folder' },
  { id: 'logs', label: '运行日志', group: '资源', icon: 'logs' },
  { id: 'policies', label: '整理策略', group: '系统', icon: 'sliders' },
  { id: 'cache', label: '识别缓存', group: '系统', icon: 'database' },
  { id: 'settings', label: '系统设置', group: '系统', icon: 'settings' },
]

const authenticated = ref(false)
const csrf = ref('')
const currentRole = ref<'admin' | 'operator' | 'viewer'>('admin')
const login = reactive({ username: 'admin', password: '' })
const busy = ref(false)
const error = ref('')
const section = ref<PrototypeSection>('dashboard')
const notice = ref('')
const mobileNav = ref(false)
const search = ref('')
const expandedJob = ref<string | null>(null)
const selectedFile = ref<string | null>(null)
const directorySource = ref<'strm' | 'pan'>('strm')
const directoryPath = ref('')
const panCID = ref('0')
const panCrumbs = ref<Array<{ id: string; name: string }>>([{ id: '0', name: '115 根目录' }])
const settingsTab = ref<SettingsTab>('general')
const policyTab = ref<'version' | 'classification'>('version')
const organizationRecords = ref<OrganizationRecord[]>([])
const classification = reactive<ClassificationConfig>({ movie_root: '电影', tv_root: '电视剧', rules: [] })
const classificationPreviewID = ref('')
const classificationPreview = ref<{ root?: string; subcategory?: string; rule?: string; title?: string } | null>(null)
const classificationBusy = ref(false)
const policyForm = reactive({ mode: 'copy', tvVersionPolicy: 'keep', movieVersionPolicy: 'quality', deletePolicy: 'output', retirePolicy: 'output', receiveCleanupMode: 'disabled', receiveCleanupDays: 7, movieTemplate: '', tvTemplate: '', episodeRegex: '' })
const cleanupPreset = ref<'disabled' | '3' | '7' | '10' | 'custom'>('disabled')
const policyRows = ref<Array<{ name: string; field: keyof typeof policyForm; enabled: boolean }>>([
  { name: '电影版本策略', field: 'movieVersionPolicy', enabled: true },
  { name: '电视剧版本策略', field: 'tvVersionPolicy', enabled: true },
  { name: '接收目录清理', field: 'receiveCleanupMode', enabled: false },
])
const settingsForm = reactive({
  panCookie: '', tmdbToken: '',
  jellyfin: { url: '', api_key: '', protocol: 'http', host: '', port: 8096, base_path: '' },
  wecom: { enabled: false, callback_base_url: '', callback_access_token: '', corp_id: '', agent_id: '', secret: '', token: '', encoding_aes_key: '', allow_users: '' },
  cms: { enabled: false, origins: '', strm_roots: '', cookie: '', legacy_enabled: false, migration_read_only: false, interval_ms: 1000 },
  playback: { public_url: '', check_link: true },
  proxy: { enabled: false, url: '', username: '', password: '', tmdb: true, images: true, pan: false, wecom: false, jellyfin: false, bypass_private: true, bypass: '' },
  logging: { days: 30, max_entries: 50000, level: 'info', max_bytes: 52428800 },
  recognition: { enabled: true, ttl_days: 30, max_entries: 5000, max_bytes: 67108864 },
})
const cmsStatus = reactive<CMSStatus>({})
const managedUsers = ref<ManagedUser[]>([])
const newUser = reactive({ username: '', password: '', role: 'operator' as ManagedUser['role'] })
const uploadConfig = reactive<UploadConfig>({ enabled: false, local_dir: '/media/115-upload', target_cid: '', channel: 'auto', scan_interval_seconds: 30, stable_seconds: 30 })
const uploadSessions = ref<UploadSession[]>([])
const uploadDragging = ref(false)
const uploadInput = ref<HTMLInputElement | null>(null)
const uploadRunning = new Map<string, Promise<void>>()
const uploadResumeKey = 'prototype-b-upload-sessions'
const qr = reactive({ id: '', image: '', state: '', busy: false })
let qrTimer = 0
const operation = reactive({ open: false, action: 'mkdir' as FileOperation, target: '', scope: 'output', paths: [] as string[], label: '', digest: '', changes: [] as Array<{ source?: string; target?: string; name?: string }>, busy: false, previewed: false })
const directoryFilter = ref('')
const selectedFileItem = ref<FileItem | null>(null)
const directoryPicker = reactive({
  open: false,
  field: 'pending' as 'pending' | 'strm' | 'inbox' | 'library',
  source: 'local' as 'local' | 'pan',
  path: '',
  cid: '0',
  crumbs: [{ id: '0', name: '115 根目录' }] as Array<{ id: string; name: string }>,
  files: [] as FileItem[],
  loading: false,
})
const newURL = ref('')
const colorMode = ref<'dark' | 'light'>((window.localStorage.getItem('prototype-b-color-mode') as 'dark' | 'light') || 'dark')
const organizeRun = ref<'' | 'transfer-incremental' | 'transfer-full' | 'local-incremental' | 'local-full'>('')
const queuePulse = ref(0)
const organizePulse = ref(0)
const uploadPulse = ref(0)
const directoryPulse = ref(0)
const settingsPulse = ref(0)
const state = reactive({
  jobs: [] as LocalJob[],
  stats: { media: 0, jobs: 0, pending: 0, failed: 0 },
  account: { used: 0, total: 1, username: '', vip: '', expire: '' } as Account,
  logs: [] as LogItem[],
  files: [] as FileItem[],
  uploadTasks: [] as Array<{ name: string; status: string; progress: number; size: string; channel: string }>,
  cacheHits: 0,
  cacheTotal: 0,
  cacheItems: [] as Array<{ label: string; kind: string; hits: number }>,
  uploadChannel: 'auto',
  settings: { gateway: '', interval: 5, syncEnabled: true, pendingPath: '/media/115-pending', strmPath: '/media/115-strm', inboxCID: '', libraryCID: '' },
  directorySettings: {} as Record<string, unknown>,
})
let noticeTimer = 0
let refreshTimer = 0

const title = computed(() => sections.find(item => item.id === section.value)?.label || '运行概览')
const transferJobs = computed(() => state.jobs.filter(job => !['local', 'scrape'].includes(job.source)))
const filteredJobs = computed(() => transferJobs.value.filter(job => !search.value || `${job.title} ${job.id} ${job.source}`.toLowerCase().includes(search.value.toLowerCase())))
const transferDuplicateGroups = computed(() => {
  const groups = new Map<string, LocalJob[]>()
  for (const job of transferJobs.value) {
    const key = (job.shareUrl || job.shareCode || '').trim().toLowerCase()
    if (!key) continue
    groups.set(key, [...(groups.get(key) || []), job])
  }
  for (const [key, jobs] of groups) groups.set(key, jobs.sort((a, b) => new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime()))
  return groups
})
const activeJobs = computed(() => state.jobs.filter(job => !['completed', 'cleaned'].includes(job.status)))
const waitingJobs = computed(() => state.jobs.filter(job => ['waiting_match', 'needs_confirmation', 'unrecognized'].includes(job.status)))
const completedJobs = computed(() => state.jobs.filter(job => ['completed', 'cleaned', 'success'].includes(job.status)))
// The media organization view contains all organization-capable jobs. Scrape
// is a library scan summary and has no per-file organization chain, so it is
// intentionally kept out of this table.
const organizeJobs = computed(() => state.jobs.filter(job => job.source !== 'scrape'))
const organizationFilter = ref<'all' | 'success' | 'skipped' | 'failed' | 'duplicate'>('all')
const organizationPage = ref(1)
const organizationPageSize = ref(25)
const organizationFilterOptions: Array<{ key: typeof organizationFilter.value; label: string }> = [
  { key: 'all', label: '全部' },
  { key: 'success', label: '成功' },
  { key: 'skipped', label: '已跳过' },
  { key: 'failed', label: '失败' },
  { key: 'duplicate', label: '重复' },
]
const organizationRows = computed<OrganizationRow[]>(() => {
  const rows: OrganizationRow[] = []
  const rowKeys = new Set<string>()
  const covered = new Set<string>()
  for (const job of organizeJobs.value) {
    const records = recordsFor(job)
    if (!records.length) {
      if (['completed', 'cleaned', 'success'].includes(job.status)) continue
      const key = `job:${job.id}`
      if (!rowKeys.has(key)) rows.push({ key, job })
      rowKeys.add(key)
      continue
    }
    for (const record of records) {
      covered.add(record.id)
      const key = `record:${record.id}`
      if (!rowKeys.has(key)) rows.push({ key, job, record })
      rowKeys.add(key)
    }
  }
  for (const record of organizationRecords.value) {
    if (covered.has(record.id)) continue
    const job = jobForOrganizationRecord(record)
    const key = `record:${record.id}`
    if (!rowKeys.has(key)) rows.push({ key, job, record })
    rowKeys.add(key)
  }
  return rows.sort((a, b) => {
    const at = a.record?.updated_at || a.job.createdAt
    const bt = b.record?.updated_at || b.job.createdAt
    return new Date(bt).getTime() - new Date(at).getTime()
  })
})
const filteredOrganizationRows = computed(() => organizationRows.value.filter(row => {
  if (organizationFilter.value === 'duplicate') return isOrganizationDuplicate(row.record)
  if (organizationFilter.value === 'success') return ['success', 'completed', 'cleaned'].includes(rowStatus(row))
  if (organizationFilter.value === 'skipped') return rowStatus(row) === 'skipped'
  if (organizationFilter.value === 'failed') return rowStatus(row) === 'failed'
  return true
}))
const organizationPageCount = computed(() => Math.max(1, Math.ceil(filteredOrganizationRows.value.length / organizationPageSize.value)))
const pagedOrganizationRows = computed(() => {
  const start = (organizationPage.value - 1) * organizationPageSize.value
  return filteredOrganizationRows.value.slice(start, start + organizationPageSize.value)
})
const files = computed(() => state.files)
const filteredFiles = computed(() => state.files.filter(item => !directoryFilter.value || item.name.toLowerCase().includes(directoryFilter.value.toLowerCase())))
const statusLabel = (status: string) => ({ completed: '已完成', cleaned: '已完成', success: '成功', transferring: '转存中', queued: '排队中', received: '已接收', matching: '匹配中', organizing: '整理中', running: '整理中', waiting_match: '待确认', needs_confirmation: '待确认', unrecognized: '待识别', skipped: '已跳过', failed: '失败', retry: '等待重试' } as Record<string, string>)[status] || status
const statusTone = (status: string) => ['completed', 'cleaned', 'success'].includes(status) ? 'success' : status === 'failed' ? 'danger' : ['waiting_match', 'needs_confirmation', 'unrecognized'].includes(status) ? 'warn' : 'progress'
function transferDuplicateReason(job: LocalJob) {
  const key = (job.shareUrl || job.shareCode || '').trim().toLowerCase()
  const group = key ? transferDuplicateGroups.value.get(key) : undefined
  if (group && group.length > 1 && group.findIndex(item => item.id === job.id) === 0) return ''
  if (job.submissionMessage) return job.submissionMessage
  if (job.duplicate) return '服务端判定为重复分享，已复用原任务'
  if (group && group.length > 1) return `同一分享重复提交（共 ${group.length} 条记录）`
  if (job.shareUpdate) return '同一分享的版本更新记录'
  return ''
}
function isTransferDuplicate(job: LocalJob) { return Boolean(transferDuplicateReason(job)) }

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body) headers.set('Content-Type', 'application/json')
  if (csrf.value && options.method && options.method !== 'GET') headers.set('X-CSRF-Token', csrf.value)
  const response = await fetch(path, { ...options, headers, credentials: 'same-origin' })
  const body = await response.json().catch(() => ({})) as T & { error?: { message?: string } }
  if (response.status === 401) { authenticated.value = false; throw new Error('登录已失效') }
  if (!response.ok) throw new Error(body.error?.message || `HTTP ${response.status}`)
  return body
}
function flash(message: string) { notice.value = message; window.clearTimeout(noticeTimer); noticeTimer = window.setTimeout(() => notice.value = '', 2600) }
function fail(value: unknown) { error.value = value instanceof Error ? value.message : String(value); window.setTimeout(() => error.value = '', 5000) }
function navigate(next: PrototypeSection) { section.value = next; mobileNav.value = false; window.scrollTo({ top: 0, behavior: 'smooth' }); void refreshSection(next) }
function toggleColorMode() { colorMode.value = colorMode.value === 'dark' ? 'light' : 'dark'; window.localStorage.setItem('prototype-b-color-mode', colorMode.value); flash(colorMode.value === 'dark' ? '已切换到深色主题' : '已切换到浅色主题') }
function formatRelative(value?: string) { if (!value) return '-'; const minutes = Math.max(1, Math.round((Date.now() - new Date(value).getTime()) / 60000)); return minutes < 60 ? `${minutes} 分钟前` : minutes < 1440 ? `${Math.round(minutes / 60)} 小时前` : `${Math.round(minutes / 1440)} 天前` }
function formatDate(value?: string) { return value ? new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(value)) : '-' }
function formatBytes(value: number) { if (!Number.isFinite(value) || value <= 0) return '0 B'; const units = ['B', 'KB', 'MB', 'GB', 'TB']; const index = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024))); return `${(value / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}` }
function uploadStatusLabel(status: string) { return ({ created: '等待上传', receiving: '接收中', verifying: '校验中', ready: '等待入队', queued: '等待传到 115', remote_uploading: '上传到 115', completed: '已完成', paused: '已暂停', cancelled: '已取消', failed: '失败', expired: '已过期' } as Record<string, string>)[status] || status }
function uploadStatusTone(status: string) { return status === 'completed' ? 'success' : ['failed', 'cancelled', 'expired'].includes(status) ? 'danger' : ['verifying', 'ready'].includes(status) ? 'warn' : 'progress' }
function uploadProgress(session: UploadSession) { if (['queued', 'remote_uploading', 'completed'].includes(session.status)) return 100; if (!session.total_size) return session.status === 'receiving' ? 0 : 100; return Math.min(100, Math.round((session.received_size / session.total_size) * 100)) }
function canCancelUpload(session: UploadSession) { return ['created', 'receiving', 'verifying', 'ready'].includes(session.status) || (session.status === 'failed' && !session.upload_task_id) }
function uploadResumeEntries(): Array<{ id: string; name: string; size: number; lastModified: number }> { try { const value = JSON.parse(window.localStorage.getItem(uploadResumeKey) || '[]'); return Array.isArray(value) ? value : [] } catch { return [] } }
function rememberUploadSession(file: File, id: string) { const entries = uploadResumeEntries().filter(item => item.id !== id); entries.unshift({ id, name: file.name, size: file.size, lastModified: file.lastModified }); window.localStorage.setItem(uploadResumeKey, JSON.stringify(entries.slice(0, 50))) }
function forgetUploadSession(id: string) { const entries = uploadResumeEntries().filter(item => item.id !== id); window.localStorage.setItem(uploadResumeKey, JSON.stringify(entries)) }
function remoteIDsFromJob(job: Job) { if (!Array.isArray(job.expected)) return []; return job.expected.flatMap((item: unknown) => { if (typeof item === 'string' && ['local', 'manual'].includes(job.source || '')) return [item]; if (item && typeof item === 'object' && 'id' in item) return [String(item.id)]; return [] }) }
function localJob(job: Job): LocalJob { const status = job.status || 'queued'; const progress = ['completed', 'cleaned', 'success', 'waiting_match', 'needs_confirmation'].includes(status) ? 100 : status === 'failed' ? 0 : status === 'transferring' ? 50 : 10; return { id: job.id, title: job.title || '等待识别', source: job.source || '网页提交', status, createdAt: job.created_at || new Date().toISOString(), size: '-', progress, quality: job.tmdb_kind === 'tv' ? '电视剧' : job.tmdb_kind === 'movie' ? '电影' : '待识别', path: '', shareUrl: job.share_url || '', shareCode: String(job.share_code || ''), duplicate: job.duplicate === true, submissionMessage: String(job.submission_message || ''), shareUpdate: job.share_update === true, error: job.error, kind: job.tmdb_kind || '', tmdbId: Number(job.tmdb_id || 0), stageCID: String(job.stage_cid || ''), remoteIDs: remoteIDsFromJob(job), cloudSourcePath: '', cloudTargetPath: '', cloudLoaded: false, cloudLoading: false } }
function recordsFor(job: LocalJob) { const records = organizationRecords.value.filter(item => item.task_id === job.id); if (records.length) return records; return organizationRecords.value.filter(item => !item.task_id && (job.remoteIDs.includes(item.remote_id) || (!!job.stageCID && item.inbox_id === job.stageCID))) }
function recordFor(job: LocalJob) { return recordsFor(job)[0] }
function jobForOrganizationRecord(record: OrganizationRecord): LocalJob {
  const linked = state.jobs.find(job => job.id === record.task_id) || state.jobs.find(job => job.remoteIDs.includes(record.remote_id) || (!!job.stageCID && job.stageCID === record.inbox_id))
  if (linked) return linked
  const title = (record.name || '未命名媒体').replace(/\.[^.]+$/, '')
  return { id: record.task_id || `record-${record.id}`, title, source: 'organization', status: record.status === 'failed' ? 'failed' : record.status === 'skipped' ? 'waiting_match' : 'success', createdAt: record.updated_at || new Date().toISOString(), size: '-', progress: 100, quality: record.kind === 'tv' ? '电视剧' : '电影', path: '', shareUrl: '', kind: record.kind || '', tmdbId: Number(record.tmdb_id || 0), stageCID: String(record.inbox_id || ''), remoteIDs: record.remote_id ? [record.remote_id] : [], cloudSourcePath: record.cloud_source_path || '', cloudTargetPath: record.cloud_target_path || '', cloudLoaded: true, cloudLoading: false }
}
function organizationKey(record: OrganizationRecord) {
  const kind = record.kind || ''
  if (record.remote_id) return `${kind}:${record.remote_id}:${record.season || 0}:${record.episode || 0}:${record.episode_end || 0}`
  const path = record.output_path || record.source_path || record.name || record.id
  return `${kind}:${record.tmdb_id || 0}:${path}`
}
const organizationDuplicateIndex = computed(() => {
  const groups = new Map<string, OrganizationRecord[]>()
  for (const record of organizationRecords.value) {
    const key = organizationKey(record)
    groups.set(key, [...(groups.get(key) || []), record])
  }
  for (const [key, records] of groups) {
    groups.set(key, records.sort((a, b) => new Date(a.updated_at || '').getTime() - new Date(b.updated_at || '').getTime()))
  }
  return groups
})
function isOrganizationRecordDuplicate(record?: OrganizationRecord) {
  if (!record) return false
  const group = organizationDuplicateIndex.value.get(organizationKey(record)) || []
  return group.length > 1 && group.findIndex(item => item.id === record.id) > 0
}
function organizationDuplicateReason(record?: OrganizationRecord) {
  if (!isOrganizationRecordDuplicate(record)) return ''
  const explicit = record?.error
  if (explicit) return explicit
  if (record?.kind === 'tv') return '同一剧集已存在更早整理记录，当前记录属于第 2 次及之后的整理'
  return '同一媒体已存在整理记录，当前记录与其他任务重复'
}
function isOrganizationDuplicate(record?: OrganizationRecord) { return isOrganizationRecordDuplicate(record) }
function rowTitle(row: OrganizationRow) {
  if (!row.record) return mediaItemLabel(row.job)
  const kind = row.record.kind || row.job.kind || ''
  if (kind !== 'tv') return row.job.title || row.record.name
  if ((row.record.season || 0) >= 0 && (row.record.episode || 0) > 0) {
    const title = row.job.title || row.record.name
    return `${title}-S${String(row.record.season || 0).padStart(2, '0')}-E${String(row.record.episode).padStart(2, '0')}`
  }
  const values = [row.record.output_path, row.record.source_path, row.record.name, row.job.title]
  for (const value of values) {
    const match = value?.match(/(?:S|Season[ ._-]*)(\d{1,2})[\s._-]*(?:E|Episode[ ._-]*)(\d{1,3})/i)
    if (match) return `${row.job.title || row.record.name}-S${match[1].padStart(2, '0')}-E${match[2].padStart(2, '0')}`
  }
  return row.job.title || row.record.name
}
function rowEpisodeLabel(row: OrganizationRow) {
  const kind = row.record?.kind || row.job.kind || ''
  if (kind !== 'tv') return ''
  if ((row.record?.episode || 0) > 0) return ` · S${String(row.record?.season || 0).padStart(2, '0')}-E${String(row.record?.episode).padStart(2, '0')}`
  const value = [row.record?.output_path, row.record?.source_path, row.record?.name, row.job.title].find(Boolean) || ''
  const match = value.match(/(?:S|Season[ ._-]*)(\d{1,2})[\s._-]*(?:E|Episode[ ._-]*)(\d{1,3})/i)
  return match ? ` · S${match[1].padStart(2, '0')}-E${match[2].padStart(2, '0')}` : ''
}
function organizationPathFallback(row: OrganizationRow, side: 'source' | 'target') {
  const status = rowStatus(row)
  if (['waiting_match', 'needs_confirmation', 'unrecognized'].includes(status)) return '等待 TMDB 匹配后生成本地路径'
  if (['queued', 'matching', 'received', 'organizing', 'transferring', 'retry', 'running'].includes(status)) return '任务处理中，路径尚未生成'
  if (status === 'failed') return `任务失败，未生成${side === 'source' ? '源文件' : '目标文件'}路径`
  return side === 'source' ? '尚未生成源文件路径' : '尚未生成目标文件路径'
}
function rowSourcePath(row: OrganizationRow) { return row.record?.source_path || (row.record ? sourcePath(row.job) : organizationPathFallback(row, 'source')) }
function rowTargetPath(row: OrganizationRow) { return row.record?.output_path || (row.record ? targetPath(row.job) : organizationPathFallback(row, 'target')) }
function rowQualityPath(row: OrganizationRow) { return row.record?.quality ? [row.record.quality.resolution ? `${row.record.quality.resolution}P` : '', row.record.quality.hdr || '', row.record.quality.codec || '', row.record.quality.audio || ''].filter(Boolean).join(' ') || row.job.quality : qualityPath(row.job) }
function rowStatus(row: OrganizationRow) { return row.record?.status || row.job.status }
function organizationFilterCount(filter: typeof organizationFilter.value) {
  return organizationRows.value.filter(row => {
    if (filter === 'duplicate') return isOrganizationDuplicate(row.record)
    if (filter === 'success') return ['success', 'completed', 'cleaned'].includes(rowStatus(row))
    if (filter === 'skipped') return rowStatus(row) === 'skipped'
    if (filter === 'failed') return rowStatus(row) === 'failed'
    return true
  }).length
}
function setOrganizationFilter(filter: typeof organizationFilter.value) {
  organizationFilter.value = filter
  organizationPage.value = 1
}
function setOrganizationPageSize(value: number) {
  organizationPageSize.value = value
  organizationPage.value = 1
}
function changeOrganizationPageSize(event: Event) {
  const target = event.target as HTMLSelectElement | null
  setOrganizationPageSize(Number(target?.value || 25))
}
function moveOrganizationPage(delta: number) {
  organizationPage.value = Math.min(organizationPageCount.value, Math.max(1, organizationPage.value + delta))
}
watch([filteredOrganizationRows, organizationPageSize], () => {
  if (organizationPage.value > organizationPageCount.value) organizationPage.value = organizationPageCount.value
})
function sourcePath(job: LocalJob) { return recordFor(job)?.source_path || '尚未生成源文件路径' }
function targetPath(job: LocalJob) { return recordFor(job)?.output_path || '尚未生成目标文件路径' }
function mediaItemLabel(job: LocalJob) {
  const record = recordFor(job)
  const kind = job.kind || record?.kind || ''
  if (kind !== 'tv') return job.title
  const paths = [targetPath(job), sourcePath(job), record?.name || '', job.title]
  for (const value of paths) {
    if (!value || value.startsWith('尚未生成')) continue
    const seasonEpisode = value.match(/(?:S|Season[ ._-]*)(\d{1,2})[\s._-]*(?:E|Episode[ ._-]*)(\d{1,3})/i)
    if (seasonEpisode) return `${job.title}-S${seasonEpisode[1].padStart(2, '0')}-E${seasonEpisode[2].padStart(2, '0')}`
    const episode = value.match(/(?:第|Ep(?:isode)?[ ._-]*)\s*(\d{1,3})\s*(?:集)?/i)
    if (episode) return `${job.title}-E${episode[1].padStart(2, '0')}`
  }
  return job.title
}
function transferEpisodeSummary(job: LocalJob) {
  const episodes = recordsFor(job).filter(record => (record.kind || job.kind) === 'tv' && (record.episode || 0) > 0).map(record => `S${String(record.season || 0).padStart(2, '0')}-E${String(record.episode).padStart(2, '0')}`)
  return [...new Set(episodes)].join('、')
}
function qualityPath(job: LocalJob) { const record = recordFor(job); if (!record?.quality) return job.quality; const q = record.quality; return [q.resolution ? `${q.resolution}P` : '', q.hdr || '', q.codec || '', q.audio || ''].filter(Boolean).join(' ') || job.quality }
function transferPathFallback(job: LocalJob, side: 'source' | 'target') {
  if (['waiting_match', 'needs_confirmation', 'unrecognized'].includes(job.status)) return '等待 TMDB 匹配后生成 115 路径'
  if (['queued', 'matching', 'received', 'organizing', 'transferring', 'retry'].includes(job.status)) return '任务处理中，路径尚未投影'
  if (job.status === 'failed') return '任务失败，未生成 115 路径'
  return `已完成，但后端未返回 115 ${side === 'source' ? '源文件' : '目标'}路径`
}
function transferSourcePath(job: LocalJob) { return job.cloudLoading ? '正在读取 115 源文件路径…' : job.cloudSourcePath || transferPathFallback(job, 'source') }
function transferTargetPath(job: LocalJob) { return job.cloudLoading ? '正在读取 115 目标路径…' : job.cloudTargetPath || transferPathFallback(job, 'target') }
function transferPathReason(job: LocalJob) {
  if (['waiting_match', 'needs_confirmation', 'unrecognized'].includes(job.status)) return '尚未生成：任务还没有确认 TMDB 匹配，后端不会生成 115 源文件和目标路径。'
  if (['queued', 'matching', 'received', 'organizing', 'transferring', 'retry'].includes(job.status)) return '尚未生成：任务仍在接收、识别或整理中，完成后会生成路径投影。'
  if (job.status === 'failed') return `尚未生成：任务处理失败${job.error ? `（${job.error}）` : ''}。`
  if (!state.settings.inboxCID || !state.settings.libraryCID) return '路径未返回：115 接收目录或整理目录尚未配置，无法拼出完整路径。'
  return '路径未返回：任务已完成，但后端没有生成可投影的 115 组织记录，请检查该任务的组织记录和网盘目录。'
}
function isCloudTransferJob(job: LocalJob) { return !['local', 'scrape'].includes(job.source) }
async function loadCloudPaths(job: LocalJob) {
  if (!isCloudTransferJob(job) || job.cloudLoaded || job.cloudLoading) return
  job.cloudLoading = true
  try {
    const value = await request<{ items?: OrganizationRecord[] }>(`/api/v1/organize/jobs/${encodeURIComponent(job.id)}/preview`)
    const item = (value.items || []).find(entry => entry.cloud_source_path || entry.cloud_target_path)
    if (item) {
      job.cloudSourcePath = item.cloud_source_path || ''
      job.cloudTargetPath = item.cloud_target_path || ''
    }
  } catch { /* a queued job may not have a projection yet */ } finally {
    job.cloudLoading = false
    job.cloudLoaded = true
  }
}
async function toggleExpandedJob(job: LocalJob) {
  if (expandedJob.value === job.id) {
    expandedJob.value = null
    return
  }
  expandedJob.value = job.id
  await loadCloudPaths(job)
}
async function toggleExpandedOrganizationRow(row: OrganizationRow) {
  if (expandedJob.value === row.key) {
    expandedJob.value = null
    return
  }
  expandedJob.value = row.key
  await loadCloudPaths(row.job)
}
let jobsRequest: Promise<void> | null = null
async function loadJobs() {
  if (jobsRequest) return jobsRequest
  jobsRequest = (async () => {
    const value = await request<{ items: Job[] }>('/api/v1/transfers')
    state.jobs = (value.items || []).map(localJob)
    state.stats.jobs = state.jobs.length
    state.stats.pending = state.jobs.filter(job => !['completed', 'cleaned', 'success'].includes(job.status)).length
    state.stats.failed = state.jobs.filter(job => job.status === 'failed').length
  })()
  try { await jobsRequest } finally { jobsRequest = null }
}
let organizationRecordsRequest: Promise<void> | null = null
async function loadOrganizationRecords() {
  if (organizationRecordsRequest) return organizationRecordsRequest
  organizationRecordsRequest = (async () => {
    try {
      const records: OrganizationRecord[] = []
      let offset = 0
      while (true) {
        const value = await request<{ items: OrganizationRecord[]; total: number }>(`/api/v1/records/organization?limit=100&offset=${offset}`)
        const items = value.items || []
        records.push(...items)
        offset += items.length
        if (!items.length || offset >= value.total || items.length < 100) break
      }
      organizationRecords.value = [...new Map(records.map(record => [record.id, record])).values()]
    } catch (value) { fail(value) }
  })()
  try { await organizationRecordsRequest } finally { organizationRecordsRequest = null }
}
const mutationPendingStatuses = new Set(['queued', 'retry', 'transferring', 'received', 'matching', 'organizing', 'verifying', 'running'])
function waitFor(milliseconds: number) { return new Promise<void>(resolve => window.setTimeout(resolve, milliseconds)) }
async function waitForMutation(options: { jobIDs?: string[]; recordIDs?: string[] } = {}) {
  const jobIDs = new Set(options.jobIDs || [])
  const recordIDs = new Set(options.recordIDs || [])
  const hasTargets = jobIDs.size > 0 || recordIDs.size > 0
  for (let attempt = 0; attempt < 40; attempt++) {
    await Promise.all([loadJobs(), loadOrganizationRecords()])
    if (!hasTargets) return
    const jobsSettled = [...jobIDs].every(id => {
      const job = state.jobs.find(item => item.id === id)
      return !job || !mutationPendingStatuses.has(job.status)
    })
    const recordsSettled = [...recordIDs].every(id => {
      const record = organizationRecords.value.find(item => item.id === id)
      return !record || !mutationPendingStatuses.has(record.status || '')
    })
    if (jobsSettled && recordsSettled) return
    await waitFor(500)
  }
}
async function loadDashboard() { const value = await request<{ stats: { media?: number; jobs?: number; pending?: number; failed?: number } }>('/api/v1/dashboard'); Object.assign(state.stats, value.stats || {}); if (!state.stats.jobs) state.stats.jobs = state.jobs.length }
async function loadAccount() { try { const value = await request<{ username?: string; vip?: boolean; vip_level?: number; vip_expire?: number; space_total?: number; space_used?: number }>('/api/v1/115/account'); state.account = { used: (value.space_used || 0) / 1e12, total: Math.max(0.001, (value.space_total || 1e12) / 1e12), username: value.username || '', vip: value.vip ? `VIP ${value.vip_level || ''}` : '', expire: value.vip_expire ? formatDate(new Date(value.vip_expire * 1000).toISOString()) : '' } } catch { state.account = { used: 0, total: 1, username: '', vip: '', expire: '' } } }
async function loadLogs() { try { const value = await request<{ items: Array<{ created_at: string; level?: string; category: string; message: string }> }>('/api/v1/logs?limit=100&offset=0'); state.logs = (value.items || []).map(item => ({ time: item.created_at, level: item.level || 'info', category: item.category || '系统', message: item.message, tone: item.level === 'error' || item.level === 'warning' ? 'warning' : item.level === 'info' ? 'info' : 'success' })) } catch { state.logs = [] } }
async function loadFiles() { try { if (directorySource.value === 'pan') { const value = await request<{ entries: Array<{ id: string; name: string; directory: boolean; size?: number; updated_at?: string }> }>(`/api/v1/files/115?cid=${encodeURIComponent(panCID.value)}`); state.files = (value.entries || []).map(item => ({ name: item.name, type: item.directory ? 'folder' : 'file', size: item.directory ? '-' : `${item.size || 0} B`, updated: item.updated_at ? formatDate(item.updated_at) : '-', path: item.id })) } else { const value = await request<{ path?: string; entries: Array<{ name: string; directory: boolean; size?: number; modified?: string; path?: string }> }>(`/api/v1/files/strm?path=${encodeURIComponent(directoryPath.value)}`); directoryPath.value = value.path || directoryPath.value; state.files = (value.entries || []).map(item => ({ name: item.name, type: item.directory ? 'folder' : 'file', size: item.directory ? '-' : `${item.size || 0} B`, updated: item.modified ? formatDate(item.modified) : '-', path: item.path })) } } catch { state.files = [] } }
function pickerLabel(field: typeof directoryPicker.field) { return field === 'pending' ? '本地待整理目录' : field === 'strm' ? '本地 STRM 媒体库' : field === 'inbox' ? '115 接收目录' : '115 整理目录' }
function pickerRoot(field: typeof directoryPicker.field) { return field === 'pending' ? state.settings.pendingPath : state.settings.strmPath }
function pickerPathDisplay() { return directoryPicker.source === 'pan' ? (directoryPicker.crumbs.map(item => item.name).join(' / ')) : (directoryPicker.path || '本地根目录') }
async function openDirectoryPicker(field: typeof directoryPicker.field) {
  directoryPicker.field = field
  directoryPicker.source = field === 'pending' || field === 'strm' ? 'local' : 'pan'
  directoryPicker.path = ''
  directoryPicker.cid = '0'
  directoryPicker.crumbs = [{ id: '0', name: '115 根目录' }]
  directoryPicker.files = []
  directoryPicker.open = true
  await loadDirectoryPicker()
}
async function loadDirectoryPicker() {
  directoryPicker.loading = true
  try {
    if (directoryPicker.source === 'pan') {
      const value = await request<{ entries: Array<{ id: string; name: string; directory: boolean; size?: number; updated_at?: string }> }>(`/api/v1/files/115?cid=${encodeURIComponent(directoryPicker.cid)}`)
      directoryPicker.files = (value.entries || []).map(item => ({ name: item.name, type: item.directory ? 'folder' : 'file', size: item.directory ? '-' : `${item.size || 0} B`, updated: item.updated_at ? formatDate(item.updated_at) : '-', path: item.id }))
    } else {
      const scope = directoryPicker.field === 'pending' ? 'pending' : ''
      const value = await request<{ path?: string; entries: Array<{ name: string; directory: boolean; size?: number; modified?: string; path?: string }> }>(`/api/v1/files/strm?scope=${encodeURIComponent(scope)}&path=${encodeURIComponent(directoryPicker.path)}`)
      directoryPicker.path = value.path || directoryPicker.path
      directoryPicker.files = (value.entries || []).map(item => ({ name: item.name, type: item.directory ? 'folder' : 'file', size: item.directory ? '-' : `${item.size || 0} B`, updated: item.modified ? formatDate(item.modified) : '-', path: item.path }))
    }
  } catch (value) { directoryPicker.files = []; fail(value) } finally { directoryPicker.loading = false }
}
async function enterPickerDirectory(file: FileItem) {
  if (file.type !== 'folder' || !file.path) return
  if (directoryPicker.source === 'pan') {
    directoryPicker.cid = file.path
    directoryPicker.crumbs.push({ id: file.path, name: file.name })
  } else directoryPicker.path = file.path
  await loadDirectoryPicker()
}
async function pickerGoUp() {
  if (directoryPicker.source === 'pan') {
    if (directoryPicker.crumbs.length <= 1) return
    directoryPicker.crumbs.pop()
    directoryPicker.cid = directoryPicker.crumbs[directoryPicker.crumbs.length - 1]?.id || '0'
  } else {
    const parts = directoryPicker.path.split('/').filter(Boolean)
    parts.pop()
    directoryPicker.path = parts.join('/')
  }
  await loadDirectoryPicker()
}
function selectPickerDirectory() {
  if (directoryPicker.source === 'pan') {
    state.settings[directoryPicker.field === 'inbox' ? 'inboxCID' : 'libraryCID'] = directoryPicker.cid
  } else {
    const root = pickerRoot(directoryPicker.field)
    const relative = directoryPicker.path.replace(/^\/+/, '')
    state.settings[directoryPicker.field === 'pending' ? 'pendingPath' : 'strmPath'] = relative ? `${root.replace(/\/+$/, '')}/${relative}` : root
  }
  directoryPicker.open = false
  flash(`${pickerLabel(directoryPicker.field)}已选择`)
}
function closeDirectoryPicker() { if (!directoryPicker.loading) directoryPicker.open = false }
async function selectDirectorySource(value: 'strm' | 'pan') { directorySource.value = value; selectedFile.value = null; selectedFileItem.value = null; directoryFilter.value = ''; if (value === 'pan') { panCID.value = '0'; panCrumbs.value = [{ id: '0', name: '115 根目录' }] } else directoryPath.value = ''; await loadFiles() }
async function enterDirectory(file: FileItem) { if (file.type !== 'folder' || !file.path) return; selectedFile.value = file.name; selectedFileItem.value = null; if (directorySource.value === 'pan') { panCID.value = file.path; panCrumbs.value.push({ id: file.path, name: file.name }) } else directoryPath.value = file.path; await loadFiles() }
async function resetDirectory() { selectedFile.value = null; selectedFileItem.value = null; directoryFilter.value = ''; if (directorySource.value === 'pan') { panCID.value = '0'; panCrumbs.value = [{ id: '0', name: '115 根目录' }] } else directoryPath.value = ''; await loadFiles() }
const canGoUpDirectory = computed(() => directorySource.value === 'pan' ? panCrumbs.value.length > 1 : Boolean(directoryPath.value))
async function goUpDirectory() {
  selectedFile.value = null
  selectedFileItem.value = null
  directoryFilter.value = ''
  if (directorySource.value === 'pan') {
    if (panCrumbs.value.length <= 1) return
    panCrumbs.value.pop()
    panCID.value = panCrumbs.value[panCrumbs.value.length - 1]?.id || '0'
  } else {
    const parts = directoryPath.value.split('/').filter(Boolean)
    parts.pop()
    directoryPath.value = parts.join('/')
  }
  await loadFiles()
}
async function loadUploads() {
  try {
    const [tasks, sessions] = await Promise.all([
      request<{ items: Array<{ filename?: string; name?: string; size?: number; status: string; channel?: string }> }>('/api/v1/uploads'),
      request<{ items: UploadSession[] }>('/api/v1/upload/sessions'),
    ])
    state.uploadTasks = (tasks.items || []).map(item => ({ name: item.filename || item.name || '未命名文件', status: item.status, progress: ['completed', 'success'].includes(item.status) ? 100 : 0, size: formatBytes(item.size || 0), channel: item.channel || 'auto' }))
    uploadSessions.value = sessions.items || []
  } catch {
    state.uploadTasks = []
    uploadSessions.value = []
  }
}
async function loadUploadSettings() {
  try {
    const value = await request<UploadConfig>('/api/v1/upload/settings')
    Object.assign(uploadConfig, value)
    state.uploadChannel = value.channel || 'auto'
  } catch {
    state.uploadChannel = 'auto'
  }
}
async function loadCache() { try { const value = await request<{ items?: Array<{ label?: string; kind?: string; hits?: number }> ; total?: number }>('/api/v1/recognition-cache'); state.cacheTotal = value.total || value.items?.length || 0; state.cacheHits = (value.items || []).reduce((sum, item) => sum + (item.hits || 0), 0); state.cacheItems = (value.items || []).map(item => ({ label: item.label || '未命名条目', kind: item.kind || '识别', hits: item.hits || 0 })) } catch { state.cacheTotal = 0; state.cacheHits = 0; state.cacheItems = [] } }
async function loadSettings() {
  try {
    const [directories, syncValue, organization, jellyfin, wecom, tmdb, classificationValue, cms, cmsStatusValue, playback, proxy, logging, recognition] = await Promise.all([
      request<Record<string, unknown>>('/api/v1/settings/directories'),
      request<{ interval_minutes?: number; enabled?: boolean }>('/api/v1/settings/sync'),
      request<Record<string, unknown>>('/api/v1/settings/organization'),
      request<Record<string, unknown>>('/api/v1/settings/jellyfin'),
      request<Record<string, unknown>>('/api/v1/settings/wecom'),
      request<Record<string, unknown>>('/api/v1/settings/tmdb'),
      request<ClassificationConfig>('/api/v1/settings/classification'),
      request<Record<string, unknown>>('/api/v1/settings/cms'),
      request<CMSStatus>('/api/v1/cms/status'),
      request<Record<string, unknown>>('/api/v1/settings/playback'),
      request<Record<string, unknown>>('/api/v1/settings/proxy'),
      request<Record<string, unknown>>('/api/v1/settings/logging'),
      request<Record<string, unknown>>('/api/v1/settings/recognition'),
    ])
    state.directorySettings = directories || {}
    state.settings.gateway = String(directories.gateway_url || '')
    state.settings.pendingPath = String(directories.pending_path || state.settings.pendingPath)
    state.settings.strmPath = String(directories.strm_path || state.settings.strmPath)
    state.settings.inboxCID = String(directories.inbox_cid || '')
    state.settings.libraryCID = String(directories.library_cid || '')
    state.settings.interval = syncValue.interval_minutes || 5
    state.settings.syncEnabled = syncValue.enabled !== false
    Object.assign(policyForm, { mode: organization.mode || 'copy', tvVersionPolicy: organization.tv_version_policy || 'keep', movieVersionPolicy: organization.movie_version_policy || 'quality', deletePolicy: organization.delete_policy || 'output', retirePolicy: organization.retire_policy || 'output', receiveCleanupMode: organization.receive_cleanup_mode || 'disabled', receiveCleanupDays: organization.receive_cleanup_days || 7, movieTemplate: organization.movie_template || '', tvTemplate: organization.tv_template || '', episodeRegex: organization.episode_regex || '' })
    cleanupPreset.value = policyForm.receiveCleanupMode === 'disabled' ? 'disabled' : [3, 7, 10].includes(policyForm.receiveCleanupDays) ? String(policyForm.receiveCleanupDays) as '3' | '7' | '10' : 'custom'
    Object.assign(classification, classificationValue || {})
    classification.rules = Array.isArray(classification.rules) ? classification.rules : []
    Object.assign(settingsForm.jellyfin, jellyfin || {})
    Object.assign(settingsForm.wecom, wecom || {})
    const users = (wecom as { allow_users?: unknown })?.allow_users
    settingsForm.wecom.allow_users = Array.isArray(users) ? users.join('\n') : String(users || '')
    settingsForm.tmdbToken = String((tmdb || {}).token || '')
    settingsForm.cms.enabled = cms.enabled === true
    settingsForm.cms.origins = Array.isArray(cms.origins) ? cms.origins.join('\\n') : String(cms.origins || '')
    settingsForm.cms.strm_roots = Array.isArray(cms.strm_roots) ? cms.strm_roots.join('\\n') : String(cms.strm_roots || '')
    settingsForm.cms.cookie = cms.cookie === '********' ? '' : String(cms.cookie || '')
    settingsForm.cms.legacy_enabled = cms.legacy_enabled === true
    settingsForm.cms.migration_read_only = cms.migration_read_only === true
    settingsForm.cms.interval_ms = Number(cms.interval_ms || 1000)
    Object.assign(cmsStatus, cmsStatusValue || {})
    const playbackValue = playback || {}
    const proxyValue = proxy || {}
    settingsForm.playback.public_url = String(playbackValue.public_url || '')
    settingsForm.playback.check_link = playbackValue.check_link !== false
    settingsForm.proxy.enabled = proxyValue.enabled === true
    settingsForm.proxy.url = String(proxyValue.url || '')
    settingsForm.proxy.username = String(proxyValue.username || '')
    settingsForm.proxy.password = proxyValue.password === '********' ? '' : String(proxyValue.password || '')
    settingsForm.proxy.tmdb = proxyValue.tmdb !== false
    settingsForm.proxy.images = proxyValue.images !== false
    settingsForm.proxy.pan = proxyValue.pan === true
    settingsForm.proxy.wecom = proxyValue.wecom === true
    settingsForm.proxy.jellyfin = proxyValue.jellyfin === true
    settingsForm.proxy.bypass_private = proxyValue.bypass_private !== false
    settingsForm.proxy.bypass = Array.isArray(proxyValue.bypass) ? proxyValue.bypass.join('\n') : String(proxyValue.bypass || '')
    Object.assign(settingsForm.logging, logging || {})
    Object.assign(settingsForm.recognition, recognition || {})
    if (currentRole.value === 'admin') {
      try {
        const users = await request<{ items?: ManagedUser[] }>('/api/v1/users')
        managedUsers.value = users.items || []
      } catch { managedUsers.value = [] }
    }
  } catch { /* empty settings are valid on a new install */ }
}
async function loadAll() { await Promise.allSettled([loadJobs(), loadOrganizationRecords(), loadDashboard(), loadAccount(), loadLogs(), loadFiles(), loadUploads(), loadUploadSettings(), loadCache(), loadSettings()]) }
async function restoreSession() { try { const value = await request<{ authenticated: boolean; csrf: string; role?: ManagedUser['role'] }>('/api/v1/session'); authenticated.value = value.authenticated; csrf.value = value.csrf || ''; currentRole.value = value.role || 'admin'; if (authenticated.value) await loadAll() } catch { authenticated.value = false } }
async function signIn() { busy.value = true; try { const value = await request<{ csrf: string; role?: ManagedUser['role'] }>('/api/v1/session/login', { method: 'POST', body: JSON.stringify(login) }); csrf.value = value.csrf; currentRole.value = value.role || 'admin'; authenticated.value = true; login.password = ''; await loadAll(); flash('登录成功') } catch (value) { fail(value) } finally { busy.value = false } }
async function signOut() { try { await request('/api/v1/session/logout', { method: 'POST', body: '{}' }) } catch { /* session may already be gone */ } authenticated.value = false; csrf.value = ''; currentRole.value = 'viewer' }
async function submitTransfer() { if (!newURL.value.trim()) { flash('请输入分享链接'); return }; busy.value = true; try { await request('/api/v1/transfers', { method: 'POST', body: JSON.stringify({ url: newURL.value.trim() }) }); newURL.value = ''; queuePulse.value++; flash('任务已加入转存队列'); await loadJobs(); navigate('transfers') } catch (value) { fail(value) } finally { busy.value = false } }
const matchCandidates = ref<Array<{ id: number; kind: string; title: string; year?: number }>>([])
const matchJob = ref<LocalJob | null>(null)
const matchRecord = ref<OrganizationRecord | null>(null)
const matchTarget = ref<MatchTarget | null>(null)
const matchQuery = ref('')
const matchKind = ref<'movie' | 'tv'>('movie')
const matchID = ref('')
const matchPreview = ref<TMDBPreview | null>(null)
const matchPreviewBusy = ref(false)
const matchSeasonPreview = ref<TMDBSeasonPreview | null>(null)
const matchSeasonBusy = ref(false)
const deletePreview = reactive({ open: false, busy: false, items: [] as DeletePreviewItem[] })
const selectedOrganizationKeys = ref<string[]>([])
const allOrganizationPageSelected = computed(() => pagedOrganizationRows.value.length > 0 && pagedOrganizationRows.value.every(row => selectedOrganizationKeys.value.includes(row.key)))
const selectedOrganizationRows = computed(() => organizationRows.value.filter(row => selectedOrganizationKeys.value.includes(row.key)))
function clearOrganizationSelection() { selectedOrganizationKeys.value = [] }
function toggleOrganizationSelection(key: string) { selectedOrganizationKeys.value = selectedOrganizationKeys.value.includes(key) ? selectedOrganizationKeys.value.filter(item => item !== key) : [...selectedOrganizationKeys.value, key] }
function toggleOrganizationPageSelection() {
  const pageKeys = pagedOrganizationRows.value.map(row => row.key)
  if (allOrganizationPageSelected.value) selectedOrganizationKeys.value = selectedOrganizationKeys.value.filter(key => !pageKeys.includes(key))
  else selectedOrganizationKeys.value = [...new Set([...selectedOrganizationKeys.value, ...pageKeys])]
}
function closeMatchModal() { matchJob.value = null; matchRecord.value = null; matchTarget.value = null; matchCandidates.value = []; matchPreview.value = null; matchPreviewBusy.value = false; matchSeasonPreview.value = null; matchSeasonBusy.value = false }
function openMatchTarget(target: MatchTarget, correction = false) {
  matchTarget.value = target
  matchJob.value = target.job
  matchRecord.value = target.record || null
  matchKind.value = (target.record?.kind || target.job.kind || 'movie') === 'tv' ? 'tv' : 'movie'
  matchID.value = String(target.record?.tmdb_id || target.job.tmdbId || '')
  matchQuery.value = target.job.title
  matchCandidates.value = []
  matchPreview.value = null
  matchSeasonPreview.value = null
  if (!correction) void searchMatchCandidates()
}
function openOrganizationCorrection(row: OrganizationRow) { openMatchTarget({ source: 'organization', job: row.job, record: row.record }, true) }
function openTransferCorrection(job: LocalJob) { openMatchTarget({ source: 'transfer', job, record: recordFor(job) }, true) }
function openBatchOrganizationCorrection() {
  const rows = selectedOrganizationRows.value
  if (!rows.length) { flash('请先选择媒体整理记录'); return }
  const first = rows[0]
  openMatchTarget({ source: 'organization-batch', job: first.job, record: first.record }, true)
  matchRecord.value = null
}
async function previewMatchByID() {
  const id = Number(matchID.value)
  if (!Number.isInteger(id) || id <= 0) { fail(new Error('请输入有效的 TMDB ID')); return }
  matchPreviewBusy.value = true
  try {
    const value = await request<Record<string, unknown>>(`/api/v1/tmdb/details?kind=${encodeURIComponent(matchKind.value)}&id=${id}`)
    const title = String(value.title || value.name || matchQuery.value || '未命名媒体')
    const date = String(value.date || value.release_date || value.first_air_date || '')
    const yearValue = Number(value.year || (date ? date.slice(0, 4) : 0))
    matchPreview.value = { id, kind: matchKind.value, title, original_title: String(value.original_title || value.original_name || ''), year: yearValue > 0 ? yearValue : undefined, release_date: String(value.release_date || value.date || ''), first_air_date: String(value.first_air_date || value.date || ''), overview: String(value.overview || ''), poster_path: String(value.poster_path || ''), seasons: Array.isArray(value.seasons) ? value.seasons as TMDBSeasonPreview[] : [] }
    matchSeasonPreview.value = null
    const recordSeason = matchRecord.value?.season
    const recordEpisode = matchRecord.value?.episode
    if (matchKind.value === 'tv' && Number.isInteger(recordSeason) && Number(recordEpisode || 0) > 0) await previewMatchSeason(id, Number(recordSeason), Number(recordEpisode))
  } catch (value) { matchPreview.value = null; fail(value) } finally { matchPreviewBusy.value = false }
}
async function previewMatchSeason(id: number, season: number, episode?: number) {
  matchSeasonBusy.value = true
  try {
    const value = await request<TMDBSeasonPreview & { episodes?: TMDBEpisodePreview[] }>(`/api/v1/tmdb/seasons?id=${id}&season=${season}`)
    const selected = value.episodes?.find(item => !episode || item.episode_number === episode)
    matchSeasonPreview.value = { ...value, season_number: value.season_number ?? season, episodes: value.episodes || [], ...(selected ? { name: value.name, episode_count: value.episode_count, } : {}) }
    if (matchPreview.value && selected) {
      matchPreview.value = { ...matchPreview.value, season, episode: selected.episode_number, episodeTitle: selected.name || `第 ${selected.episode_number} 集`, episodeOverview: selected.overview || '', episodeAirDate: selected.air_date || '' }
    }
  } catch (value) { matchSeasonPreview.value = null; fail(value) } finally { matchSeasonBusy.value = false }
}
async function confirmMatchPreview() {
  if (!matchTarget.value || !matchPreview.value) return
  const target = matchTarget.value
  try {
    const jobIDs: string[] = []
    const recordIDs: string[] = []
    if (target.source === 'organization-batch') {
      const rows = selectedOrganizationRows.value
      for (const row of rows) {
        if (row.record) {
          const value = await request<{ job?: { id?: string } }>(`/api/v1/records/organization/${encodeURIComponent(row.record.id)}/retry`, { method: 'POST', body: JSON.stringify({ kind: matchPreview.value.kind, id: matchPreview.value.id }) })
          if (value.job?.id) jobIDs.push(value.job.id)
          recordIDs.push(row.record.id)
        } else {
          await request(`/api/v1/organize/jobs/${encodeURIComponent(row.job.id)}/confirm`, { method: 'POST', body: JSON.stringify({ kind: matchPreview.value.kind, id: matchPreview.value.id }) })
          jobIDs.push(row.job.id)
        }
      }
      await waitForMutation({ jobIDs, recordIDs })
      flash(`已为 ${rows.length} 条记录提交 TMDB 纠错`)
    } else if (target.source === 'organization' && target.record) {
      const value = await request<{ job?: { id?: string } }>(`/api/v1/records/organization/${encodeURIComponent(target.record.id)}/retry`, { method: 'POST', body: JSON.stringify({ kind: matchPreview.value.kind, id: matchPreview.value.id }) })
      if (value.job?.id) jobIDs.push(value.job.id)
      recordIDs.push(target.record.id)
      await waitForMutation({ jobIDs, recordIDs })
      flash('已按新的 TMDB 匹配重新整理')
    } else {
      await request(`/api/v1/organize/jobs/${encodeURIComponent(target.job.id)}/confirm`, { method: 'POST', body: JSON.stringify({ kind: matchPreview.value.kind, id: matchPreview.value.id }) })
      await waitForMutation({ jobIDs: [target.job.id] })
      flash('已提交新的 TMDB 匹配')
    }
    closeMatchModal(); await Promise.all([loadJobs(), loadOrganizationRecords()]); organizePulse.value++; queuePulse.value++
  } catch (value) { fail(value) }
}
async function confirmJob(job: LocalJob, kind = job.kind, id = job.tmdbId) { if (!kind || !id) { openMatchTarget({ source: 'transfer', job }); return }; try { await request(`/api/v1/organize/jobs/${encodeURIComponent(job.id)}/confirm`, { method: 'POST', body: JSON.stringify({ kind, id }) }); await waitForMutation({ jobIDs: [job.id] }); flash('整理确认已完成'); organizePulse.value++ } catch (value) { fail(value) } }
async function searchMatchCandidates() { if (!matchQuery.value.trim()) return; try { const kind = matchKind.value || (matchJob.value?.kind === 'tv' ? 'tv' : 'movie'); const value = await request<{ items?: Array<{ id: number; kind: string; title: string; year?: number }> }>(`/api/v1/tmdb/search?q=${encodeURIComponent(matchQuery.value.trim())}&kind=${encodeURIComponent(kind)}`); matchCandidates.value = value.items || [] } catch (value) { fail(value) } }
async function chooseMatch(candidate: { id: number; kind: string }) { if (!matchTarget.value) return; matchKind.value = candidate.kind === 'tv' ? 'tv' : 'movie'; matchID.value = String(candidate.id); await previewMatchByID() }
async function retryJob(job: LocalJob) { try { await request(`/api/v1/organize/jobs/${encodeURIComponent(job.id)}/retry`, { method: 'POST', body: '{}' }); await waitForMutation({ jobIDs: [job.id] }); const latest = state.jobs.find(item => item.id === job.id); flash(latest?.status === 'failed' ? '整理失败，详情已更新' : '整理状态已更新'); queuePulse.value++ } catch (value) { fail(value) } }
async function reorganizeOrganizationRow(row: OrganizationRow) {
  try {
    if (row.record) {
      const value = await request<{ job?: { id?: string } }>(`/api/v1/records/organization/${encodeURIComponent(row.record.id)}/retry`, { method: 'POST', body: '{}' })
      await waitForMutation({ jobIDs: value.job?.id ? [value.job.id] : [], recordIDs: [row.record.id] })
      flash('整理状态已更新')
    } else await retryJob(row.job)
    organizePulse.value++
  } catch (value) { fail(value) }
}
async function batchReorganizeOrganization() {
  const rows = selectedOrganizationRows.value
  if (!rows.length) { flash('请先选择媒体整理记录'); return }
  busy.value = true
  try {
    const jobIDs: string[] = []
    const recordIDs: string[] = []
    for (const row of rows) {
      if (row.record) {
        const value = await request<{ job?: { id?: string } }>(`/api/v1/records/organization/${encodeURIComponent(row.record.id)}/retry`, { method: 'POST', body: '{}' })
        if (value.job?.id) jobIDs.push(value.job.id)
        recordIDs.push(row.record.id)
      } else {
        jobIDs.push(row.job.id)
        await request(`/api/v1/organize/jobs/${encodeURIComponent(row.job.id)}/retry`, { method: 'POST', body: '{}' })
      }
    }
    await waitForMutation({ jobIDs, recordIDs })
    flash(`已更新 ${rows.length} 条整理记录`); clearOrganizationSelection(); organizePulse.value++
  } catch (value) { fail(value) } finally { busy.value = false }
}
async function prepareOrganizationDelete(rows: OrganizationRow[] = selectedOrganizationRows.value) {
  const candidates = rows.filter(row => row.record?.remote_id)
  if (!candidates.length) { flash('所选记录没有可删除的媒体源'); return }
  deletePreview.busy = true
  try {
    const items: DeletePreviewItem[] = []
    for (const row of candidates) {
      const remoteID = row.record?.remote_id || ''
      const value = await request<{ remote_id?: string; output?: string; source?: string; cloud?: string; retained?: string[]; assets?: string[]; digest: string }>(`/api/v1/media/${encodeURIComponent(remoteID)}/delete-preview`)
      items.push({ remoteID, title: rowTitle(row), output: value.output, source: value.source, cloud: value.cloud, retained: value.retained || [], assets: value.assets || [], digest: value.digest })
    }
    deletePreview.items = items; deletePreview.open = true
  } catch (value) { fail(value) } finally { deletePreview.busy = false }
}
function closeDeletePreview() { if (!deletePreview.busy) { deletePreview.open = false; deletePreview.items = [] } }
async function confirmOrganizationDelete() {
  if (!deletePreview.items.length) return
  deletePreview.busy = true
  try {
    for (const item of deletePreview.items) await request(`/api/v1/media/${encodeURIComponent(item.remoteID)}/delete`, { method: 'POST', body: JSON.stringify({ digest: item.digest }) })
    const count = deletePreview.items.length
    deletePreview.open = false; deletePreview.items = []
    flash(`已删除 ${count} 条媒体记录`); clearOrganizationSelection(); await Promise.all([loadJobs(), loadOrganizationRecords()]); organizePulse.value++
  } catch (value) { fail(value) } finally { deletePreview.busy = false }
}
async function runTransferOrganize(full: boolean) {
  const mode = full ? 'transfer-full' : 'transfer-incremental'
  if (organizeRun.value) return
  organizeRun.value = mode
  try {
    const value = await request<{ queued?: number; requeued?: number; skipped?: number; needs_confirmation?: number }>(full ? '/api/v1/organize/received/full' : '/api/v1/organize/received', { method: 'POST', body: '{}' })
    const queued = value.queued || 0
    const requeued = value.requeued || 0
    const skipped = value.skipped || 0
    flash(`${full ? '全量' : '增量'}整理已完成：新排队 ${queued}，重新检查 ${requeued}，跳过 ${skipped}`)
    await Promise.all([loadJobs(), loadOrganizationRecords(), loadDashboard()])
  } catch (value) { fail(value) } finally { organizeRun.value = '' }
}

async function runLocalOrganize(full: boolean) {
  const mode = full ? 'local-full' : 'local-incremental'
  if (organizeRun.value) return
  organizeRun.value = mode
  try {
    const value = await request<{ queued?: number; requeued?: number; skipped?: number }>(full ? '/api/v1/organize/local/queue/full' : '/api/v1/organize/local/queue', { method: 'POST', body: '{}' })
    const queued = value.queued || 0
    const requeued = value.requeued || 0
    const skipped = value.skipped || 0
    flash(`${full ? '全量' : '增量'}媒体整理已完成：新排队 ${queued}，重新整理 ${requeued}，跳过 ${skipped}`)
    await Promise.all([loadJobs(), loadOrganizationRecords(), loadDashboard()])
    organizePulse.value++
  } catch (value) { fail(value) } finally { organizeRun.value = '' }
}
function mergeUploadSession(session: UploadSession) { const index = uploadSessions.value.findIndex(item => item.id === session.id); if (index >= 0) uploadSessions.value.splice(index, 1, session); else uploadSessions.value.unshift(session) }
async function uploadFetch(path: string, options: RequestInit = {}) {
  const headers = new Headers(options.headers)
  if (csrf.value && options.method && options.method !== 'GET' && options.method !== 'HEAD') headers.set('X-CSRF-Token', csrf.value)
  const response = await fetch(path, { ...options, headers, credentials: 'same-origin' })
  if (response.status === 401) { authenticated.value = false; throw new Error('登录已失效') }
  if (!response.ok) {
    const body = await response.json().catch(() => ({})) as { error?: { message?: string } }
    throw new Error(body.error?.message || `HTTP ${response.status}`)
  }
  return response
}
async function createBrowserUploadSession(file: File) {
  const resume = uploadResumeEntries().find(item => item.name === file.name && item.size === file.size && item.lastModified === file.lastModified)
  if (resume) {
    try {
      const detail = await request<UploadSession>(`/api/v1/upload/sessions/${encodeURIComponent(resume.id)}`)
      if (['completed', 'queued', 'remote_uploading'].includes(detail.status)) return detail
      if (detail.status === 'failed' && detail.upload_task_id && detail.received_size >= detail.total_size) {
        await request(`/api/v1/uploads/${encodeURIComponent(detail.upload_task_id)}/retry`, { method: 'POST', body: '{}' })
        detail.status = 'queued'
        mergeUploadSession(detail)
        return detail
      }
      if (detail.status === 'failed') throw new Error('上次上传校验失败，请重新上传')
      if (['cancelled', 'expired'].includes(detail.status)) throw new Error('上次上传会话已结束，请重新选择文件')
      const head = await uploadFetch(`/api/v1/upload/sessions/${encodeURIComponent(resume.id)}`, { method: 'HEAD', headers: { 'Tus-Resumable': '1.0.0' } })
      const offset = Number(head.headers.get('Upload-Offset') || 0)
      const existing = uploadSessions.value.find(item => item.id === resume.id)
      const session = existing || detail || { id: resume.id, filename: file.name, total_size: file.size, received_size: offset, status: 'receiving' } as UploadSession
      session.received_size = offset
      session.total_size = Number(head.headers.get('Upload-Length') || file.size)
      session.status = 'receiving'
      mergeUploadSession(session)
      return session
    } catch {
      forgetUploadSession(resume.id)
    }
  }
  const session = await request<UploadSession>('/api/v1/upload/sessions', { method: 'POST', body: JSON.stringify({ filename: file.name, size: file.size, target_cid: uploadConfig.target_cid, channel: uploadConfig.channel }) })
  rememberUploadSession(file, session.id)
  mergeUploadSession(session)
  return session
}
async function patchBrowserUpload(session: UploadSession, file: File, offset: number, chunk: Blob) {
  let lastError: unknown
  for (let attempt = 0; attempt < 5; attempt++) {
    try {
      const response = await uploadFetch(`/api/v1/upload/sessions/${encodeURIComponent(session.id)}`, {
        method: 'PATCH',
        headers: { 'Tus-Resumable': '1.0.0', 'Upload-Offset': String(offset), 'Upload-Chunk-Length': String(chunk.size), 'Content-Type': 'application/offset+octet-stream' },
        body: chunk,
      })
      return Number(response.headers.get('Upload-Offset') || offset + chunk.size)
    } catch (value) {
      lastError = value
      await waitFor(2 ** attempt * 500)
      try {
        const head = await uploadFetch(`/api/v1/upload/sessions/${encodeURIComponent(session.id)}`, { method: 'HEAD', headers: { 'Tus-Resumable': '1.0.0' } })
        const serverOffset = Number(head.headers.get('Upload-Offset') || 0)
        if (serverOffset !== offset) return serverOffset
      } catch { /* retry the same chunk */ }
    }
  }
  throw lastError instanceof Error ? lastError : new Error('分片上传失败')
}
async function runBrowserUpload(file: File, session: UploadSession) {
  if (session.status === 'completed' || session.status === 'queued' || session.status === 'remote_uploading') return
  if (['ready', 'verifying'].includes(session.status) && session.received_size >= session.total_size) {
    const completed = await request<UploadSession>(`/api/v1/upload/sessions/${encodeURIComponent(session.id)}/complete`, { method: 'POST', body: '{}' })
    mergeUploadSession(completed)
    if (completed.status === 'queued' || completed.status === 'completed' || completed.status === 'remote_uploading') forgetUploadSession(session.id)
    return
  }
  let offset = Math.max(0, session.received_size || 0)
  const chunkSize = 32 * 1024 * 1024
  session.status = 'receiving'; session.error = ''; mergeUploadSession({ ...session })
  while (offset < file.size) {
    const chunk = file.slice(offset, Math.min(file.size, offset + chunkSize))
    const next = await patchBrowserUpload(session, file, offset, chunk)
    if (next < offset || next > file.size) throw new Error('服务端返回了无效的上传偏移')
    offset = next
    session.received_size = offset
    mergeUploadSession({ ...session })
  }
  const completed = await request<UploadSession>(`/api/v1/upload/sessions/${encodeURIComponent(session.id)}/complete`, { method: 'POST', body: '{}' })
  mergeUploadSession(completed)
  forgetUploadSession(session.id)
}
async function uploadSelectedFiles(files: File[]) {
  for (const file of files) {
    if (uploadRunning.has(`${file.name}:${file.size}:${file.lastModified}`)) continue
    const key = `${file.name}:${file.size}:${file.lastModified}`
    const task = (async () => {
      try {
        const session = await createBrowserUploadSession(file)
        await runBrowserUpload(file, session)
        flash(`${file.name} 已进入 115 上传队列`)
      } catch (value) {
        fail(value)
      } finally {
        await loadUploads()
        uploadRunning.delete(key)
      }
    })()
    uploadRunning.set(key, task)
    await task
  }
}
function startUpload() { uploadInput.value?.click() }
function handleUploadInput(event: Event) { const input = event.target as HTMLInputElement; if (input.files?.length) void uploadSelectedFiles([...input.files]); input.value = '' }
function handleUploadDrop(event: DragEvent) { uploadDragging.value = false; if (event.dataTransfer?.files?.length) void uploadSelectedFiles([...event.dataTransfer.files]) }
function retryBrowserUpload(session: UploadSession) {
  const file = uploadInput.value?.files?.[0]
  if (file && file.name === session.filename && file.size === session.total_size) void uploadSelectedFiles([file])
  else flash('请点击选择文件，重新选择“'+session.filename+'”后继续')
}
async function resumeUploadSession(session: UploadSession) {
  if (session.upload_task_id && session.received_size >= session.total_size) {
    try {
      await request(`/api/v1/uploads/${encodeURIComponent(session.upload_task_id)}/retry`, { method: 'POST', body: '{}' })
      await loadUploads()
      flash(`${session.filename} 已重新加入 115 队列`)
    } catch (value) { fail(value) }
    return
  }
  startUpload()
}
async function cancelBrowserUpload(session: UploadSession) { try { await request(`/api/v1/upload/sessions/${encodeURIComponent(session.id)}`, { method: 'DELETE', body: '{}' }); session.status = 'cancelled'; forgetUploadSession(session.id); mergeUploadSession({ ...session }); flash('上传已取消') } catch (value) { fail(value) } }
async function scanReceivedDirectory() { directoryPulse.value++; await runTransferOrganize(false) }
async function refreshLocalOrganization() { organizePulse.value++; try { await Promise.all([loadJobs(), loadOrganizationRecords()]); flash('整理记录已刷新') } catch (value) { fail(value) } }
async function openJob(job: LocalJob) {
  expandedJob.value = job.id
  if (isCloudTransferJob(job)) {
    navigate('transfers')
    await loadCloudPaths(job)
  } else {
    navigate('organize')
  }
}
function directoryPayload() {
  return { ...state.directorySettings, gateway_url: state.settings.gateway, pending_path: state.settings.pendingPath, strm_path: state.settings.strmPath, inbox_cid: state.settings.inboxCID, library_cid: state.settings.libraryCID }
}
async function saveDirectories() {
  await request('/api/v1/settings/directories', { method: 'PUT', body: JSON.stringify(directoryPayload()) })
  state.directorySettings = { ...state.directorySettings, ...directoryPayload() }
  flash('目录配置已保存')
}
async function saveSettings() { settingsPulse.value++; try { await request('/api/v1/settings/sync', { method: 'PUT', body: JSON.stringify({ enabled: state.settings.syncEnabled, interval_minutes: state.settings.interval }) }); if (state.settings.inboxCID && state.settings.libraryCID) await request('/api/v1/settings/directories', { method: 'PUT', body: JSON.stringify(directoryPayload()) }); flash('设置已保存') } catch (value) { fail(value) } }
async function createManagedUser() {
  if (!newUser.username.trim() || !newUser.password) { fail(new Error('请输入用户名和密码')); return }
  try {
    await request('/api/v1/users', { method: 'POST', body: JSON.stringify({ username: newUser.username.trim(), password: newUser.password, role: newUser.role }) })
    newUser.username = ''; newUser.password = ''; newUser.role = 'operator'
    await loadSettings(); flash('用户已创建')
  } catch (value) { fail(value) }
}
async function updateManagedUser(user: ManagedUser, changes: Partial<ManagedUser>) {
  try { await request(`/api/v1/users/${encodeURIComponent(user.username)}`, { method: 'PUT', body: JSON.stringify(changes) }); await loadSettings(); flash('用户设置已更新') } catch (value) { fail(value) }
}
async function deleteManagedUser(user: ManagedUser) {
  if (!window.confirm(`确定删除用户“${user.username}”？`)) return
  try { await request(`/api/v1/users/${encodeURIComponent(user.username)}`, { method: 'DELETE', body: '{}' }); await loadSettings(); flash('用户已删除') } catch (value) { fail(value) }
}
async function savePolicies() { try { const mode = cleanupPreset.value === 'disabled' ? 'disabled' : 'after_days'; const days = cleanupPreset.value === '3' || cleanupPreset.value === '7' || cleanupPreset.value === '10' ? Number(cleanupPreset.value) : Math.max(1, Number(policyForm.receiveCleanupDays) || 1); policyForm.receiveCleanupMode = mode; policyForm.receiveCleanupDays = days; await request('/api/v1/settings/organization', { method: 'PUT', body: JSON.stringify({ mode: policyForm.mode, tv_version_policy: policyForm.tvVersionPolicy, movie_version_policy: policyForm.movieVersionPolicy, delete_policy: policyForm.deletePolicy, retire_policy: policyForm.retirePolicy, receive_cleanup_mode: mode, receive_cleanup_days: days, movie_template: policyForm.movieTemplate, tv_template: policyForm.tvTemplate, episode_regex: policyForm.episodeRegex }) }); flash('整理策略已保存') } catch (value) { fail(value) } }
async function saveClassification() { classificationBusy.value = true; try { await request('/api/v1/settings/classification', { method: 'PUT', body: JSON.stringify(classification) }); flash('分类策略已保存') } catch (value) { fail(value) } finally { classificationBusy.value = false } }
function addClassificationRule() { classification.rules.push({ name: `自定义规则 ${classification.rules.length + 1}`, enabled: true, kind: 'movie', countries: [], languages: [], genres: [], keyword: '', target: '其他' }) }
function removeClassificationRule(index: number) { classification.rules.splice(index, 1) }
async function previewClassification() { if (!classificationPreviewID.value.trim()) { fail(new Error('请输入 TMDB ID')); return }; try { classificationPreview.value = await request(`/api/v1/classification/preview`, { method: 'POST', body: JSON.stringify({ kind: 'movie', tmdb_id: Number(classificationPreviewID.value), config: classification }) }) } catch (value) { fail(value) } }
function beginFileOperation(action: FileOperation) { const item = selectedFileItem.value; if (action !== 'mkdir' && !item?.path) { flash('请先选择一个文件或目录'); return }; operation.action = action; operation.scope = directorySource.value === 'pan' ? '115' : 'output'; operation.paths = action === 'mkdir' ? [directorySource.value === 'pan' ? panCID.value : (directoryPath.value || '')] : [item?.path || '']; operation.target = ''; operation.label = item?.name || (directorySource.value === 'pan' ? `CID ${panCID.value}` : '当前目录'); operation.digest = ''; operation.changes = []; operation.previewed = false; operation.open = true }
async function previewFileOperation() { operation.busy = true; try { const requestPaths = operation.scope === 'output' && operation.action === 'mkdir' ? [operation.paths[0] ? `${operation.paths[0]}/${operation.target}` : operation.target] : operation.paths; const value = await request<{ digest?: string; changes?: Array<{ source?: string; target?: string; name?: string }> }>('/api/v1/files/preview', { method: 'POST', body: JSON.stringify({ scope: operation.scope, action: operation.action, paths: requestPaths, target: operation.scope === 'output' && operation.action === 'mkdir' ? '' : operation.target }) }); operation.digest = value.digest || ''; operation.changes = value.changes || []; operation.previewed = true } catch (value) { fail(value) } finally { operation.busy = false } }
async function executeFileOperation() { if (!operation.previewed || !operation.digest) return; operation.busy = true; try { await request('/api/v1/files/execute', { method: 'POST', body: JSON.stringify({ scope: operation.scope, action: operation.action, paths: operation.paths, target: operation.target, digest: operation.digest }) }); operation.open = false; flash('目录操作已完成'); selectedFileItem.value = null; selectedFile.value = null; await loadFiles() } catch (value) { fail(value) } finally { operation.busy = false } }
function closeFileOperation() { if (!operation.busy) operation.open = false }
async function saveSettingTab(tab: SettingsTab) {
  try {
    if (tab === 'directories') await saveDirectories()
    if (tab === 'uploads') { await request('/api/v1/upload/settings', { method: 'PUT', body: JSON.stringify(uploadConfig) }); await loadUploadSettings() }
    if (tab === 'tmdb') await request('/api/v1/settings/tmdb', { method: 'PUT', body: JSON.stringify({ token: settingsForm.tmdbToken }) })
    if (tab === 'jellyfin') await request('/api/v1/settings/jellyfin', { method: 'PUT', body: JSON.stringify(settingsForm.jellyfin) })
    if (tab === 'wecom') await request('/api/v1/settings/wecom', { method: 'PUT', body: JSON.stringify({ ...settingsForm.wecom, allow_users: settingsForm.wecom.allow_users.split(/\s+/).filter(Boolean) }) })
    if (tab === 'cms') {
      await request('/api/v1/settings/cms', {
        method: 'PUT',
        body: JSON.stringify({
          ...settingsForm.cms,
          origins: settingsForm.cms.origins.split(/\s+/).filter(Boolean),
          strm_roots: settingsForm.cms.strm_roots.split(/\s+/).filter(Boolean),
        }),
      })
      await loadSettings()
    }
    if (tab === 'advanced') {
      await request('/api/v1/settings/playback', { method: 'PUT', body: JSON.stringify(settingsForm.playback) })
      await request('/api/v1/settings/proxy', { method: 'PUT', body: JSON.stringify({ ...settingsForm.proxy, bypass: settingsForm.proxy.bypass.split(/\s+/).filter(Boolean) }) })
      await request('/api/v1/settings/logging', { method: 'PUT', body: JSON.stringify(settingsForm.logging) })
      await request('/api/v1/settings/recognition', { method: 'PUT', body: JSON.stringify(settingsForm.recognition) })
    }
    if (tab !== 'directories') flash('设置已保存')
  } catch (value) { fail(value) }
}
async function checkCMS() { try { await request('/api/v1/cms/check', { method: 'POST', body: '{}' }); flash('CMS Cookie 检查通过'); await loadSettings() } catch (value) { fail(value) } }
async function savePanCookie() { if (!settingsForm.panCookie.trim()) { fail(new Error('请输入 115 Cookie')); return } try { await request('/api/v1/115/cookie', { method: 'POST', body: JSON.stringify({ cookie: settingsForm.panCookie.trim() }) }); settingsForm.panCookie = ''; flash('115 Cookie 已验证并保存'); await loadAccount() } catch (value) { fail(value) } }
async function startQRLogin() { qr.busy = true; try { const value = await request<{ id: string; png_data?: string }>('/api/v1/115/qr', { method: 'POST', body: '{}' }); qr.id = value.id; qr.image = value.png_data || ''; qr.state = 'waiting'; pollQRLogin() } catch (value) { fail(value) } finally { qr.busy = false } }
async function pollQRLogin() { if (!qr.id) return; try { const value = await request<{ state?: string; cookie?: string }>(`/api/v1/115/qr/${encodeURIComponent(qr.id)}`); qr.state = value.state || 'waiting'; if (value.state === 'confirmed' || value.cookie) { qr.id = ''; flash('115 扫码登录成功'); await loadAccount(); return }; if (!['expired', 'canceled'].includes(qr.state)) qrTimer = window.setTimeout(pollQRLogin, 1800) } catch (value) { qr.id = ''; fail(value) } }
function closeQR() { qr.id = ''; qr.image = ''; window.clearTimeout(qrTimer) }
async function test115() { try { await request('/api/v1/115/check', { method: 'POST', body: '{}' }); flash('115 连接正常') } catch (value) { fail(value) } }
async function testTMDB() { try { await request('/api/v1/proxy/test', { method: 'POST', body: JSON.stringify({ service: 'tmdb' }) }); flash('TMDB 连接正常') } catch (value) { fail(value) } }
async function testJellyfin() { try { await request('/api/v1/jellyfin/test', { method: 'POST', body: '{}' }); flash('Jellyfin 连接正常') } catch (value) { fail(value) } }
async function testWeCom() { try { await request('/api/v1/proxy/test', { method: 'POST', body: JSON.stringify({ service: 'wecom' }) }); flash('企业微信连接正常') } catch (value) { fail(value) } }
async function clearCache() { try { await request('/api/v1/recognition-cache/all/delete', { method: 'POST', body: JSON.stringify({ confirm: true }) }); state.cacheHits = 0; state.cacheTotal = 0; flash('缓存已清空') } catch (value) { fail(value) } }
async function refreshSection(value: PrototypeSection = section.value) { if (!authenticated.value) return; try { if (value === 'transfers' || value === 'organize' || value === 'records') await Promise.all([loadJobs(), loadOrganizationRecords()]); else if (value === 'logs') await loadLogs(); else if (value === 'files') await loadFiles(); else if (value === 'uploads') await Promise.all([loadUploads(), loadUploadSettings()]); else if (value === 'cache') await loadCache(); else if (value === 'settings' || value === 'policies') await Promise.all([loadSettings(), loadUploadSettings()]); else if (value === 'dashboard') await Promise.all([loadDashboard(), loadJobs(), loadOrganizationRecords(), loadLogs(), loadAccount()]) } catch (value) { fail(value) } }
onMounted(() => { void restoreSession(); refreshTimer = window.setInterval(() => { if (authenticated.value && !document.hidden) void refreshSection() }, 10000) })
onBeforeUnmount(() => { window.clearInterval(refreshTimer); window.clearTimeout(noticeTimer); window.clearTimeout(qrTimer) })
</script>

<template>
  <div class="board-shell" :class="[`board-${colorMode}`]">
    <div v-if="!authenticated" class="board-login-shell">
      <form class="board-login-panel" @submit.prevent="signIn">
        <div class="board-brand login-brand"><span><Cloud :size="22" /></span><strong>115 Direct</strong><small>MEDIA CONTROL CENTER</small></div>
        <span class="board-kicker">SECURE ACCESS</span><h1>管理台登录</h1>
        <label>用户名<input v-model="login.username" autocomplete="username" /></label>
        <label>密码<input v-model="login.password" type="password" autocomplete="current-password" autofocus /></label>
        <button class="board-button solid login-submit" :disabled="busy"><LoaderCircle v-if="busy" class="spin" :size="16" /><Cloud v-else :size="16" />登录</button>
        <p v-if="error" class="board-login-error">{{ error }}</p>
      </form>
    </div>
    <template v-else>
    <header class="board-header">
      <div class="board-brand"><span><Cloud :size="19" /></span><strong>115 Direct</strong><small>MEDIA HUB</small></div>
      <button class="board-mobile-menu" @click="mobileNav = !mobileNav"><LayoutGrid :size="18" />菜单</button>
      <nav :class="{ open: mobileNav }"><button v-for="item in sections" :key="item.id" :class="{ active: section === item.id }" @click="navigate(item.id)"><component :is="item.icon === 'grid' ? Grid2X2 : item.icon === 'arrow' ? ArrowDownToLine : item.icon === 'sync' ? RefreshCw : item.icon === 'records' ? FileText : item.icon === 'wand' ? WandSparkles : item.icon === 'upload' ? Upload : item.icon === 'folder' ? Folder : item.icon === 'logs' ? ListFilter : item.icon === 'sliders' ? SlidersHorizontal : item.icon === 'database' ? Database : Settings" :size="15" />{{ item.label }}</button></nav>
      <div class="board-header-tools"><button class="board-icon color-mode-toggle" :title="colorMode === 'dark' ? '切换浅色主题' : '切换深色主题'" :aria-label="colorMode === 'dark' ? '切换浅色主题' : '切换深色主题'" :aria-pressed="colorMode === 'light'" @click="toggleColorMode"><Sun v-if="colorMode === 'dark'" :size="17" /><Moon v-else :size="17" /></button><button class="board-icon"><Bell :size="17" /></button><span class="board-avatar">MA</span></div>
    </header>

    <main class="board-main">
      <div class="board-topline"><div><span class="board-kicker">MEDIA CONTROL CENTER / 2026.10</span><h1>{{ title }}</h1></div><div class="board-top-actions"><span class="board-online"><i></i>115 {{ state.account.username ? 'ONLINE' : 'OFFLINE' }}</span><button class="board-button solid" @click="navigate('transfers')"><Plus :size="15" />新建任务</button></div></div>

      <Transition name="board-page" mode="out-in">
      <section v-if="section === 'dashboard'" class="board-section board-dashboard">
        <div class="cinema-hero"><div><span class="board-kicker light">NOW PLAYING / SERVICE</span><h2>媒体库正在持续生长</h2><p>{{ state.stats.pending }} 个任务处理中，{{ waitingJobs.length }} 个条目等待确认。</p><button class="board-button light-button" @click="navigate('transfers')">进入实时队列 <ArrowRight :size="15" /></button></div><div class="hero-stat"><strong>{{ state.stats.media.toLocaleString() }}</strong><span>MEDIA ITEMS</span><div class="hero-spark"><i v-for="n in 12" :key="n" :style="{ height: `${20 + (n * 13) % 58}%` }"></i></div></div></div>
        <div class="board-stat-strip"><div><span>处理中</span><strong>{{ state.stats.pending }}</strong><small>队列</small></div><div><span>已完成</span><strong>{{ completedJobs.length }}</strong><small>当前记录</small></div><div><span>缓存命中</span><strong>{{ state.cacheTotal ? Math.round(state.cacheHits / state.cacheTotal * 100) : 0 }}%</strong><small>TMDB</small></div><div><span>空间使用</span><strong>{{ Math.round(state.account.used / state.account.total * 100) }}%</strong><small>{{ state.account.used.toFixed(2) }} TB</small></div></div>
        <div class="board-kanban"><article class="board-column"><header><span><i class="column-dot active"></i>正在处理</span><b>{{ activeJobs.filter(item => item.status === 'transferring').length }}</b></header><button v-for="job in activeJobs.filter(item => item.status === 'transferring')" :key="job.id" class="kanban-card" @click="openJob(job)"><div class="kanban-poster"><Play :size="16" fill="currentColor" /></div><strong>{{ job.title }}</strong><small>{{ job.quality }} · {{ job.size }}</small><div class="kanban-progress"><i :style="{ width: `${job.progress}%` }"></i></div><span>{{ job.progress }}% · {{ formatRelative(job.createdAt) }}</span></button><div v-if="!activeJobs.filter(item => item.status === 'transferring').length" class="kanban-empty">队列为空</div></article><article class="board-column attention"><header><span><i class="column-dot warn"></i>需要确认</span><b>{{ waitingJobs.length }}</b></header><button v-for="job in waitingJobs" :key="job.id" class="kanban-card" @click="openJob(job)"><div class="kanban-poster purple"><Search :size="16" /></div><strong>{{ job.title }}</strong><small>TMDB 匹配待确认</small><span class="kanban-cta">{{ isCloudTransferJob(job) ? '打开转存链路' : '打开整理链路' }} <ArrowRight :size="13" /></span></button><div v-if="!waitingJobs.length" class="kanban-empty">没有待确认任务</div></article><article class="board-column complete"><header><span><i class="column-dot done"></i>最近完成</span><b>{{ completedJobs.length }}</b></header><button v-for="job in completedJobs.slice(0, 3)" :key="job.id" class="kanban-card done" @click="navigate('records')"><div class="kanban-poster green"><Check :size="16" /></div><strong>{{ job.title }}</strong><small>{{ job.quality }} · {{ formatRelative(job.createdAt) }}</small><span class="kanban-cta">已写入媒体库</span></button></article></div>
        <div class="board-lower-grid"><article class="board-panel activity-feed"><header><div><span class="board-kicker">SIGNALS</span><h2>系统信号</h2></div><button class="board-icon"><MoreHorizontal :size="17" /></button></header><div v-for="item in state.logs.slice(0, 4)" :key="item.time + item.message" class="signal-row"><span :class="['signal-mark', item.tone]"></span><div><strong>{{ item.message }}</strong><small>{{ item.category }} · {{ formatRelative(item.time) }}</small></div></div></article><article class="board-panel quick-panel"><header><div><span class="board-kicker">SHORTCUTS</span><h2>快捷入口</h2></div><Zap :size="17" /></header><div class="quick-grid"><button @click="navigate('uploads')"><Upload :size="17" /><span>本地上传</span></button><button @click="navigate('files')"><Folder :size="17" /><span>浏览目录</span></button><button @click="navigate('policies')"><SlidersHorizontal :size="17" /><span>整理策略</span></button><button @click="navigate('settings')"><Settings :size="17" /><span>系统设置</span></button></div></article></div>
      </section>

      <section v-else-if="section === 'transfers'" class="board-section">
        <div class="transfer-command"><div><span class="board-kicker">TRANSFER COMMAND</span><h2>实时转存队列</h2><p>增量整理只处理新的接收内容；全量整理会重新检查已完成的接收任务，但不会重复接收已有成品。</p></div><div class="transfer-actions"><form @submit.prevent="submitTransfer"><input v-model="newURL" placeholder="粘贴 115 分享链接" /><button class="board-button solid"><ArrowDownToLine :size="15" />加入队列</button></form><div class="organize-mode-actions"><button class="board-button quiet" :disabled="Boolean(organizeRun)" @click="runTransferOrganize(false)"><RefreshCw :class="{ spin: organizeRun === 'transfer-incremental' }" :size="15" />增量整理</button><button class="board-button quiet" :disabled="Boolean(organizeRun)" @click="runTransferOrganize(true)"><RefreshCw :class="{ spin: organizeRun === 'transfer-full' }" :size="15" />全量整理</button></div></div></div>
        <div class="queue-toolbar"><label class="board-search"><Search :size="16" /><input v-model="search" placeholder="搜索任务 / 媒体 / ID" /></label><span>{{ filteredJobs.length }} 个任务</span><button class="board-button quiet" @click="refreshSection('transfers')"><RefreshCw :size="15" />刷新</button></div>
        <div class="queue-board" :key="queuePulse">
          <template v-for="job in filteredJobs" :key="job.id">
            <article class="queue-row" @click="toggleExpandedJob(job)"><span class="queue-index">{{ String(transferJobs.indexOf(job) + 1).padStart(2, '0') }}</span><div class="queue-poster" :class="{ done: ['completed', 'cleaned', 'success'].includes(job.status) }"><Play v-if="['completed', 'cleaned', 'success'].includes(job.status)" :size="16" fill="currentColor" /><LoaderCircle v-else-if="job.status === 'transferring'" class="spin" :size="16" /><Search v-else :size="16" /></div><div class="queue-title"><strong>{{ job.title }}</strong><small>{{ job.id }} · {{ job.source }} · {{ job.quality }} · 整理时间 {{ formatDate(job.createdAt) }}</small><span v-if="isTransferDuplicate(job)" class="duplicate-note">{{ transferDuplicateReason(job) }}</span></div><div class="queue-progress"><div><span :style="{ width: `${job.progress}%` }"></span></div><small>{{ ['completed', 'cleaned', 'success'].includes(job.status) ? '转存完成' : `${job.progress}% 正在处理` }}</small></div><span :class="['board-status', isTransferDuplicate(job) ? 'warn' : statusTone(job.status)]">{{ isTransferDuplicate(job) ? '重复记录' : statusLabel(job.status) }}</span><ChevronDown :class="{ rotated: expandedJob === job.id }" :size="16" /></article>
            <div v-if="expandedJob === job.id" class="queue-expanded"><div><span class="board-kicker">115 TRANSFER PIPELINE</span><h3>{{ job.title }}</h3><p v-if="transferEpisodeSummary(job)" class="transfer-episode-summary"><strong>剧集：</strong>{{ transferEpisodeSummary(job) }}</p><p v-if="isTransferDuplicate(job)" class="duplicate-reason"><strong>重复原因：</strong>{{ transferDuplicateReason(job) }}</p></div><div class="pipeline"><span>115 源文件</span><ArrowRight :size="15" /><code>{{ transferSourcePath(job) }}</code><ArrowRight :size="15" /><span>115 媒体库</span><code>{{ transferTargetPath(job) }}</code></div><p class="transfer-path-hint">{{ transferPathReason(job) }}</p><div class="expanded-actions"><button v-if="['waiting_match', 'needs_confirmation', 'unrecognized'].includes(job.status)" class="board-button solid" @click.stop="confirmJob(job)"><Check :size="15" />确认匹配</button><button v-if="['completed', 'cleaned', 'success', 'failed'].includes(job.status)" class="board-button solid" @click.stop="retryJob(job)"><RefreshCw :size="15" />重新整理</button><button class="board-button quiet" @click.stop="openTransferCorrection(job)"><Search :size="15" />纠错匹配</button><button class="board-button quiet" @click.stop="navigate('logs')">查看日志</button></div></div>
          </template>
          <div v-if="!filteredJobs.length" class="kanban-empty">暂无转存任务</div>
        </div>
      </section>

      <section v-else-if="section === 'records'" class="board-section"><div class="board-panel full-panel"><header><div><span class="board-kicker">ARCHIVE</span><h2>记录中心</h2></div><button class="board-button quiet" @click="refreshSection('records')"><RefreshCw :size="15" />刷新记录</button></header><div class="records-table"><div class="records-head"><span>媒体</span><span>来源</span><span>质量</span><span>整理路径</span><span>状态</span><span>时间</span></div><div v-for="job in state.jobs" :key="job.id" class="records-line"><strong>{{ mediaItemLabel(job) }}</strong><span>{{ job.source }}</span><span>{{ job.quality }}</span><code :title="targetPath(job)">{{ targetPath(job) }}</code><span :class="['board-status', statusTone(job.status)]">{{ statusLabel(job.status) }}</span><time>{{ formatDate(job.createdAt) }}</time></div></div></div></section>

      <section v-else-if="section === 'organize'" class="board-section"><div class="organize-intro" :key="directoryPulse"><div><span class="board-kicker">ORGANIZE PIPELINE</span><h2>媒体整理链路</h2><p>显示完整整理历史，包括已经完成、跳过和重复整理的每一条记录；记录按最近更新时间排序。</p></div><div class="organize-mode-actions"><button class="board-button quiet" :disabled="Boolean(organizeRun)" @click="runLocalOrganize(false)"><RefreshCw :class="{ spin: organizeRun === 'local-incremental' }" :size="15" />增量同步</button><button class="board-button quiet" :disabled="Boolean(organizeRun)" @click="runLocalOrganize(true)"><RefreshCw :class="{ spin: organizeRun === 'local-full' }" :size="15" />全量同步</button><button class="board-button solid" @click="refreshLocalOrganization"><WandSparkles :size="15" />刷新记录</button></div></div><div class="organize-filterbar"><div class="organize-filter-count">显示 <strong>{{ filteredOrganizationRows.length }}</strong> / {{ organizationRows.length }} 条记录</div><div class="organize-filter-buttons"><button v-for="filter in organizationFilterOptions" :key="filter.key" class="board-button quiet tiny" :class="{ active: organizationFilter === filter.key }" @click="setOrganizationFilter(filter.key)">{{ filter.label }} <span>{{ organizationFilterCount(filter.key) }}</span></button></div></div><div class="organize-batchbar"><label><input type="checkbox" :checked="allOrganizationPageSelected" @change="toggleOrganizationPageSelection" />选择当前页</label><span v-if="selectedOrganizationKeys.length">已选 {{ selectedOrganizationKeys.length }} 条</span><button class="board-button quiet tiny" :disabled="!selectedOrganizationKeys.length" @click="openBatchOrganizationCorrection"><Search :size="13" />批量纠错</button><button class="board-button quiet tiny" :disabled="!selectedOrganizationKeys.length" @click="batchReorganizeOrganization"><RefreshCw :size="13" />批量重新整理</button><button class="board-button quiet tiny danger-button" :disabled="!selectedOrganizationKeys.length" @click="prepareOrganizationDelete()"><Database :size="13" />批量删除</button><button v-if="selectedOrganizationKeys.length" class="board-button quiet tiny" @click="clearOrganizationSelection">清除选择</button></div><div class="organize-table" :key="organizePulse"><div class="organize-head"><span class="organize-check-head"></span><span>媒体条目</span><span>匹配结果</span><span>源文件</span><span>目标路径</span><span>操作</span></div><template v-for="row in pagedOrganizationRows" :key="row.key"><div :class="['organize-row', { selected: expandedJob === row.key }]" :aria-expanded="expandedJob === row.key"><div class="organize-check"><input type="checkbox" :checked="selectedOrganizationKeys.includes(row.key)" @click.stop @change="toggleOrganizationSelection(row.key)" /></div><div class="organize-title"><span class="queue-poster small"><Play :size="14" /></span><strong>{{ rowTitle(row) }}</strong><span v-if="isOrganizationDuplicate(row.record)" class="duplicate-badge">重复整理</span></div><span>{{ statusLabel(rowStatus(row)) }} · {{ row.record?.kind === 'tv' || row.job.kind === 'tv' ? '电视剧' : '电影' }}{{ row.record?.tmdb_id || row.job.tmdbId ? ` · TMDB #${row.record?.tmdb_id || row.job.tmdbId}` : '' }}{{ rowEpisodeLabel(row) }}<small class="organization-time">整理时间：{{ formatDate(row.record?.updated_at || row.job.createdAt) }}</small><small v-if="isOrganizationDuplicate(row.record)" class="duplicate-note">{{ organizationDuplicateReason(row.record) }}</small></span><code :title="rowSourcePath(row)">{{ rowSourcePath(row) }}</code><code :title="rowTargetPath(row)">{{ rowTargetPath(row) }}</code><div class="organize-row-actions"><button class="board-button quiet tiny" @click.stop="toggleExpandedOrganizationRow(row)">{{ expandedJob === row.key ? '收起链路' : '展开链路' }} <ChevronDown :class="{ rotated: expandedJob === row.key }" :size="13" /></button><button class="board-button quiet tiny" @click.stop="openOrganizationCorrection(row)"><Search :size="13" />纠错</button><button class="board-button quiet tiny" @click.stop="reorganizeOrganizationRow(row)"><RefreshCw :size="13" />重新整理</button><button class="board-button quiet tiny danger-button" @click.stop="prepareOrganizationDelete([row])"><Database :size="13" />删除</button></div><div v-if="expandedJob === row.key" class="organize-inline-detail"><div class="inline-step step-identify"><b>01</b><span>识别</span><strong>{{ rowTitle(row) }}</strong><small>{{ row.record?.kind === 'tv' || row.job.kind === 'tv' ? '电视剧' : '电影' }}<template v-if="row.record?.tmdb_id || row.job.tmdbId"> · TMDB #{{ row.record?.tmdb_id || row.job.tmdbId }}</template></small></div><ArrowRight :size="15" /><div class="inline-step step-name"><b>02</b><span>命名</span><strong :title="rowSourcePath(row)">{{ rowSourcePath(row) }}</strong><small>源文件</small></div><ArrowRight :size="15" /><div class="inline-step step-place"><b>03</b><span>落位</span><strong :title="rowTargetPath(row)">{{ rowTargetPath(row) }}</strong><small>{{ rowStatus(row) === 'waiting_match' ? '等待匹配后执行' : `已写入 ${rowQualityPath(row)}` }}</small></div><p v-if="isOrganizationDuplicate(row.record)" class="duplicate-reason"><strong>重复原因：</strong>{{ organizationDuplicateReason(row.record) }}</p><button v-if="['waiting_match', 'needs_confirmation', 'unrecognized'].includes(rowStatus(row))" class="board-button solid tiny" @click.stop="confirmJob(row.job)"><Check :size="13" />确认</button></div></div></template><div v-if="!pagedOrganizationRows.length" class="kanban-empty">当前筛选没有媒体整理记录</div></div><div v-if="filteredOrganizationRows.length" class="organize-pagination"><span>第 {{ organizationPage }} / {{ organizationPageCount }} 页 · 共 {{ filteredOrganizationRows.length }} 条</span><label>每页<select :value="organizationPageSize" @change="changeOrganizationPageSize"><option :value="25">25</option><option :value="50">50</option><option :value="100">100</option></select> 条</label><div><button class="board-button quiet tiny" :disabled="organizationPage <= 1" @click="moveOrganizationPage(-1)">上一页</button><button class="board-button quiet tiny" :disabled="organizationPage >= organizationPageCount" @click="moveOrganizationPage(1)">下一页</button></div></div></section>

      <section v-else-if="section === 'uploads'" class="board-section"><input ref="uploadInput" class="upload-file-input" type="file" multiple @change="handleUploadInput" /><div class="upload-stage"><div class="upload-callout" :class="{ dragging: uploadDragging }" :key="uploadPulse" @dragover.prevent="uploadDragging = true" @dragleave.prevent="uploadDragging = false" @drop.prevent="handleUploadDrop"><Upload :size="28" /><h2>把本地媒体拖到这里</h2><p>采用 32 MiB 可恢复分片，网络中断或刷新页面后可以继续。</p><button class="board-button solid" :disabled="!uploadConfig.enabled" @click="startUpload"><Plus :size="15" />选择文件</button><small v-if="!uploadConfig.enabled" class="upload-config-hint">请先到“系统设置 → 上传设置”启用本地上传并配置 115 接收目录。</small><small v-else class="upload-config-hint">目标：{{ uploadConfig.target_cid || '未配置 115 接收目录' }} · 通道：{{ uploadConfig.channel }}</small></div><div class="upload-stats"><span>本地上传服务</span><strong>{{ uploadConfig.enabled ? 'READY' : 'PAUSED' }}</strong><small>{{ uploadConfig.local_dir || '/media/115-upload' }}</small><div class="upload-bars"><i v-for="n in 9" :key="n" :style="{ height: `${28 + n * 7}%` }"></i></div></div></div><div class="board-panel full-panel"><header><div><span class="board-kicker">RESUMABLE SESSIONS</span><h2>上传会话</h2></div><span class="board-status progress">{{ uploadSessions.length }} 个文件</span></header><div class="upload-table"><div v-for="session in uploadSessions" :key="session.id" class="upload-line upload-session-line"><File :size="18" /><div><strong>{{ session.filename }}</strong><small>{{ formatBytes(session.total_size) }} · {{ uploadStatusLabel(session.status) }}<template v-if="session.error"> · {{ session.error }}</template></small></div><div class="line-progress"><span :style="{ width: `${uploadProgress(session)}%` }"></span></div><span class="upload-percent">{{ uploadProgress(session) }}%</span><span :class="['board-status', uploadStatusTone(session.status)]">{{ uploadStatusLabel(session.status) }}</span><div class="upload-line-actions"><button v-if="['failed', 'paused'].includes(session.status)" class="board-button quiet tiny" @click="resumeUploadSession(session)"><RefreshCw :size="13" />继续</button><button v-if="canCancelUpload(session)" class="board-button quiet tiny danger-button" @click="cancelBrowserUpload(session)">取消</button></div></div><div v-if="!uploadSessions.length" class="kanban-empty">暂无上传会话</div></div></div><div class="board-panel full-panel upload-remote-panel"><header><div><span class="board-kicker">115 QUEUE</span><h2>115 上传任务</h2></div><span class="board-status progress">{{ state.uploadTasks.length }} 个任务</span></header><div class="upload-table"><div v-for="task in state.uploadTasks" :key="`${task.name}-${task.size}`" class="upload-line"><File :size="18" /><div><strong>{{ task.name }}</strong><small>{{ task.size }} · {{ task.channel }}</small></div><div class="line-progress"><span :style="{ width: `${task.progress}%` }"></span></div><span :class="['board-status', statusTone(task.status)]">{{ statusLabel(task.status) }}</span></div><div v-if="!state.uploadTasks.length" class="kanban-empty">暂无 115 上传任务</div></div></div></section>

      <section v-else-if="section === 'files'" class="board-section">
        <div class="directory-switcher"><button :class="{ active: directorySource === 'pan' }" @click="selectDirectorySource('pan')"><Cloud :size="16" />115 网盘</button><button :class="{ active: directorySource === 'strm' }" @click="selectDirectorySource('strm')"><HardDrive :size="16" />本地 STRM</button></div>
        <div class="directory-layout"><aside class="directory-tree board-panel"><header><div><span class="board-kicker">{{ directorySource === 'pan' ? '115 LIBRARY' : 'LOCAL LIBRARY' }}</span><h2>目录树</h2></div><button class="board-icon" :class="{ scanning: directoryPulse > 0 }" @click="directorySource === 'pan' ? scanReceivedDirectory() : loadFiles()"><RefreshCw :size="16" /></button></header><button class="tree-root active" @click="resetDirectory"><HardDrive v-if="directorySource === 'strm'" :size="16" /><Cloud v-else :size="16" />{{ directorySource === 'pan' ? '115 根目录' : 'STRM 根目录' }}</button><button v-for="folder in filteredFiles.filter(item => item.type === 'folder')" :key="folder.name" class="tree-item" @click="enterDirectory(folder)"><Folder :size="16" />{{ folder.name }}<ChevronRight :size="14" /></button><div class="tree-foot"><span>空间使用</span><strong>{{ Math.round(state.account.used / state.account.total * 100) }}%</strong><div><i :style="{ width: `${state.account.used / state.account.total * 100}%` }"></i></div></div></aside><section class="directory-list board-panel"><header><div><span class="board-kicker">{{ directorySource === 'pan' ? `CID ${panCID}` : (directoryPath || 'ROOT') }}</span><h2>{{ directorySource === 'pan' ? '115 文件列表' : '本地文件列表' }}</h2></div><div class="directory-tools"><label class="board-search"><Search :size="15" /><input v-model="directoryFilter" placeholder="筛选文件" /></label><button class="board-button quiet" @click="beginFileOperation('mkdir')"><Plus :size="15" />新建目录</button><button v-if="selectedFileItem" class="board-button quiet" @click="beginFileOperation('rename')"><FileText :size="15" />重命名</button><button v-if="selectedFileItem" class="board-button quiet danger-button" @click="beginFileOperation('delete')"><Database :size="15" />删除</button><button class="board-button quiet" @click="loadFiles"><RefreshCw :size="15" />刷新</button></div></header><div class="directory-breadcrumb"><button class="board-button quiet tiny directory-up" :disabled="!canGoUpDirectory" @click="goUpDirectory"><ArrowLeft :size="14" />返回上一级</button><button @click="resetDirectory">{{ directorySource === 'pan' ? '115 根目录' : 'STRM 根目录' }}</button><ChevronRight :size="13" /><span>{{ selectedFile || '全部媒体' }}</span></div><div class="file-table"><div class="file-head"><span>名称</span><span>类型</span><span>大小</span><span>更新时间</span><span></span></div><button v-for="file in filteredFiles" :key="file.name" :class="['file-line', { selected: selectedFileItem?.name === file.name }]" @click="selectedFileItem = file; selectedFile = file.name" @dblclick="file.type === 'folder' ? enterDirectory(file) : undefined"><span><Folder v-if="file.type === 'folder'" :size="17" /><File v-else :size="17" /><strong>{{ file.name }}</strong></span><span>{{ file.type === 'folder' ? '目录' : '文件' }}</span><span>{{ file.size }}</span><span>{{ file.updated }}</span><ChevronRight :size="15" /></button><div v-if="!filteredFiles.length" class="kanban-empty">当前目录为空或服务未连接</div></div><div v-if="selectedFileItem" class="directory-selection"><strong>已选择：{{ selectedFileItem.name }}</strong><span>单击选择，双击目录进入；可以重命名或删除，115 分类目录还支持移动和复制。</span><button v-if="directorySource === 'pan'" class="board-button quiet tiny" @click="beginFileOperation('move')">移动到…</button><button v-if="directorySource === 'pan'" class="board-button quiet tiny" @click="beginFileOperation('copy')">复制到…</button></div></section><div class="directory-footer board-panel"><div><span class="board-kicker">DIRECTORY INSIGHT</span><h2>{{ selectedFile || (directorySource === 'pan' ? '115 根目录' : 'STRM 根目录') }}</h2><p>{{ directorySource === 'pan' ? '可浏览 115 网盘目录、创建目录、重命名、移动、复制和删除已关联文件。' : '可浏览本地 STRM 目录并执行重命名、移动、复制和删除已关联文件。' }}</p></div><button v-if="directorySource === 'pan'" class="board-button solid" :disabled="Boolean(organizeRun)" @click="runTransferOrganize(false)"><RefreshCw :class="{ spin: organizeRun === 'transfer-incremental' }" :size="15" />增量整理接收目录</button><button v-else class="board-button solid" @click="loadFiles"><RefreshCw :size="15" />刷新本地目录</button></div></div>
      </section>
      <section v-else-if="section === 'logs'" class="board-section"><div class="board-panel full-panel"><header><div><span class="board-kicker">EVENT STREAM</span><h2>运行日志</h2></div><label class="board-search"><Search :size="15" /><input placeholder="搜索日志" /></label></header><div class="event-stream"><div v-for="item in state.logs" :key="item.time + item.message" class="event-row"><time>{{ formatDate(item.time) }}</time><span :class="['event-level', item.tone]">{{ item.level }}</span><div><strong>{{ item.message }}</strong><small>{{ item.category }}</small></div></div></div></div></section>

      <section v-else-if="section === 'policies'" class="board-section"><div class="policy-ribbon"><div><span class="board-kicker">AUTOMATION RULES</span><h2>整理策略</h2><p>版本、命名、分类规则集中管理，所有启用状态和当前值都直接显示。</p></div><div class="policy-actions"><button class="board-button quiet" :class="{ active: policyTab === 'version' }" @click="policyTab = 'version'">版本与命名</button><button class="board-button quiet" :class="{ active: policyTab === 'classification' }" @click="policyTab = 'classification'">分类策略</button><button v-if="policyTab === 'version'" class="board-button solid" @click="savePolicies"><Check :size="15" />保存策略</button><button v-else class="board-button solid" :disabled="classificationBusy" @click="saveClassification"><Check :size="15" />保存分类策略</button></div></div><template v-if="policyTab === 'version'"><div class="policy-board"><article v-for="(row, rowIndex) in policyRows" :key="`${row.field}-${rowIndex}`" class="policy-tile policy-edit"><div class="policy-title"><SlidersHorizontal :size="18" /><strong>{{ row.name }}</strong></div><label class="policy-toggle"><input v-model="row.enabled" type="checkbox" /><span></span>{{ row.enabled ? '已启用' : '已停用' }}</label><select v-if="row.field === 'movieVersionPolicy'" v-model="policyForm.movieVersionPolicy"><option value="quality">清晰度优先</option><option value="keep">存在则跳过</option><option value="coexist">多版本共存</option><option value="overwrite">覆盖旧版</option><option value="newest">最近入库优先</option></select><select v-else-if="row.field === 'tvVersionPolicy'" v-model="policyForm.tvVersionPolicy"><option value="keep">存在同集则跳过</option><option value="coexist">多版本共存</option><option value="overwrite">覆盖旧版</option><option value="newest">最近入库优先</option><option value="quality">清晰度优先</option></select><select v-else v-model="policyForm.receiveCleanupMode" @change="cleanupPreset = policyForm.receiveCleanupMode === 'disabled' ? 'disabled' : (cleanupPreset === 'disabled' ? '7' : cleanupPreset)"><option value="disabled">关闭自动清理</option><option value="after_days">完成后按天数清理</option></select><select v-if="policyForm.receiveCleanupMode === 'after_days'" v-model="cleanupPreset"><option value="3">完成后 3 天清理</option><option value="7">完成后 7 天清理</option><option value="10">完成后 10 天清理</option><option value="custom">自定义天数</option></select><input v-if="policyForm.receiveCleanupMode === 'after_days' && cleanupPreset === 'custom'" v-model.number="policyForm.receiveCleanupDays" type="number" min="1" max="3650" placeholder="输入天数" /></article><article class="policy-tile policy-add"><Plus :size="18" /><strong>添加策略</strong><p>新增一条版本或清理策略，保存后沿用整理服务的策略模型。</p><button class="board-button quiet tiny" @click="policyRows.push({ name: `自定义策略 ${policyRows.length + 1}`, field: 'movieVersionPolicy', enabled: true })">添加一条</button></article></div><div class="policy-options board-panel"><label>整理方式<select v-model="policyForm.mode"><option value="copy">复制</option><option value="hardlink">硬链接</option><option value="symlink">软链接</option><option value="move">移动</option></select></label><label>电影删除策略<select v-model="policyForm.deletePolicy"><option value="output">仅删除成品</option><option value="local">删除本地源</option><option value="chain">删除整理链路</option></select></label><label>版本淘汰策略<select v-model="policyForm.retirePolicy"><option value="output">仅淘汰成品</option><option value="local">淘汰本地源</option><option value="chain">淘汰完整链路</option></select></label><label>清理保留天数<input v-model.number="policyForm.receiveCleanupDays" type="number" min="1" max="3650" /></label></div></template><template v-else><div class="classification-layout board-panel"><div class="classification-roots"><label>电影一级目录<input v-model="classification.movie_root" /></label><label>电视剧一级目录<input v-model="classification.tv_root" /></label><div class="classification-preview"><strong>分类预览</strong><span>输入 TMDB ID，检查当前规则会落到哪个目录。</span><div><input v-model="classificationPreviewID" type="number" min="1" placeholder="TMDB ID" /><button class="board-button quiet tiny" @click="previewClassification">预览分类</button></div><p v-if="classificationPreview">{{ classificationPreview.title }} → {{ classificationPreview.root }}/{{ classificationPreview.subcategory || '待确认' }} · {{ classificationPreview.rule }}</p></div></div><div class="classification-rules"><header><div><h3>规则顺序</h3><small>从上到下匹配，停用规则会跳过。</small></div><button class="board-button quiet tiny" @click="addClassificationRule"><Plus :size="14" />添加规则</button></header><div v-for="(rule, index) in classification.rules" :key="`${rule.name}-${index}`" class="classification-rule"><input v-model="rule.name" aria-label="规则名称" /><select v-model="rule.kind" aria-label="媒体类型"><option value="movie">电影</option><option value="tv">电视剧</option></select><input v-model="rule.target" aria-label="分类目录" placeholder="分类目录" /><input v-model="rule.keyword" aria-label="关键词" placeholder="关键词可选" /><label class="policy-toggle"><input v-model="rule.enabled" type="checkbox" />{{ rule.enabled ? '启用' : '停用' }}</label><button class="board-icon" title="删除规则" @click="removeClassificationRule(index)"><Database :size="15" /></button></div><div v-if="!classification.rules.length" class="kanban-empty">暂无分类规则，请添加一条。</div></div></div></template></section>

      <section v-else-if="section === 'cache'" class="board-section"><div class="cache-stage"><div class="cache-orb"><strong>{{ state.cacheTotal ? Math.round(state.cacheHits / state.cacheTotal * 100) : 0 }}%</strong><span>CACHE HIT</span></div><div><span class="board-kicker">TMDB RECOGNITION</span><h2>识别缓存</h2><p>缓存减少重复查询，统计来自识别缓存接口。</p><button class="board-button quiet" @click="clearCache"><RefreshCw :size="15" />清空缓存</button></div></div><div class="cache-grid"><article v-for="item in state.cacheItems" :key="item.label + item.kind" class="cache-card"><span class="queue-poster small"><Search :size="14" /></span><div><strong>{{ item.label }}</strong><small>{{ item.kind }} · 命中 {{ item.hits }} 次</small></div><ChevronRight :size="15" /></article><div v-if="!state.cacheItems.length" class="kanban-empty">暂无识别缓存</div></div></section>

      <section v-else class="board-section"><div class="settings-board"><aside><span class="board-kicker">SETTINGS</span><h2>系统设置</h2><button :class="{ active: settingsTab === 'general' }" @click="settingsTab = 'general'">连接与服务</button><button :class="{ active: settingsTab === 'directories' }" @click="settingsTab = 'directories'">目录配置</button><button :class="{ active: settingsTab === 'uploads' }" @click="settingsTab = 'uploads'">上传设置</button><button :class="{ active: settingsTab === 'pan' }" @click="settingsTab = 'pan'">115 登录</button><button :class="{ active: settingsTab === 'tmdb' }" @click="settingsTab = 'tmdb'">TMDB 识别</button><button :class="{ active: settingsTab === 'jellyfin' }" @click="settingsTab = 'jellyfin'">Jellyfin 网关</button><button :class="{ active: settingsTab === 'wecom' }" @click="settingsTab = 'wecom'">企业微信</button><button :class="{ active: settingsTab === 'cms' }" @click="settingsTab = 'cms'">CMS 兼容</button><button :class="{ active: settingsTab === 'advanced' }" @click="settingsTab = 'advanced'">高级设置</button><button v-if="currentRole === 'admin'" :class="{ active: settingsTab === 'users' }" @click="settingsTab = 'users'">用户与权限</button></aside><article class="board-panel settings-form">
        <template v-if="settingsTab === 'general'"><header><div><span class="board-kicker">GENERAL</span><h2>连接与服务</h2></div><span class="board-status success">配置正常</span></header><label>Jellyfin 网关地址<input v-model="state.settings.gateway" placeholder="未配置" /></label><label>自动媒体库同步间隔<select v-model.number="state.settings.interval"><option :value="5">5 分钟</option><option :value="10">10 分钟</option><option :value="30">30 分钟</option></select></label><div class="setting-toggle"><span>自动更新本地媒体库</span><button class="toggle-control" :class="{ on: state.settings.syncEnabled }" @click="state.settings.syncEnabled = !state.settings.syncEnabled"><i></i>{{ state.settings.syncEnabled ? '已启用' : '已停用' }}</button></div><div class="setting-toggle"><span>播放链路诊断</span><span class="board-status success">可用</span></div><footer><span>目录：{{ state.settings.strmPath }}</span><button class="board-button solid" :class="{ saved: settingsPulse > 0 }" @click="saveSettings"><Check :size="15" />保存设置</button></footer></template>
        <template v-else-if="settingsTab === 'directories'"><header><div><span class="board-kicker">DIRECTORY ROUTING</span><h2>目录配置</h2></div><span class="board-status success">整理链路</span></header><p class="settings-help">配置本地待整理目录、STRM 媒体库，以及 115 接收和整理目录。点击浏览选择可直接从本地或 115 目录树选取目标。</p><div class="settings-grid"><label class="wide">本地待整理目录<div class="setting-picker-field"><input v-model="state.settings.pendingPath" placeholder="/media/115-pending" /><button class="board-button quiet tiny" @click="openDirectoryPicker('pending')"><Folder :size="14" />浏览选择</button></div></label><label class="wide">本地 STRM 媒体库<div class="setting-picker-field"><input v-model="state.settings.strmPath" placeholder="/media/115-strm" /><button class="board-button quiet tiny" @click="openDirectoryPicker('strm')"><Folder :size="14" />浏览选择</button></div></label><label>115 接收目录 CID<div class="setting-picker-field"><input v-model="state.settings.inboxCID" placeholder="例如 0" /><button class="board-button quiet tiny" @click="openDirectoryPicker('inbox')"><Cloud :size="14" />选择目录</button></div></label><label>115 整理目录 CID<div class="setting-picker-field"><input v-model="state.settings.libraryCID" placeholder="例如 10000000000000000" /><button class="board-button quiet tiny" @click="openDirectoryPicker('library')"><Cloud :size="14" />选择目录</button></div></label><label class="wide">播放网关地址<input v-model="state.settings.gateway" placeholder="http://jellyfin:8096" /></label></div><footer><span>接收目录与整理目录必须不同</span><button class="board-button solid" @click="saveSettingTab('directories')"><Check :size="15" />保存目录配置</button></footer></template>
        <template v-else-if="settingsTab === 'uploads'"><header><div><span class="board-kicker">RESUMABLE UPLOAD</span><h2>上传设置</h2></div><span class="board-status" :class="{ success: uploadConfig.enabled, warn: !uploadConfig.enabled }">{{ uploadConfig.enabled ? '已启用' : '已停用' }}</span></header><p class="settings-help">浏览器拖拽上传采用可恢复分片传输。文件先写入本地上传目录，校验完成后再进入 115 队列；刷新页面或网络中断后可继续。</p><div class="settings-grid"><label class="setting-toggle"><span>启用本地上传</span><button class="toggle-control" :class="{ on: uploadConfig.enabled }" @click="uploadConfig.enabled = !uploadConfig.enabled"><i></i>{{ uploadConfig.enabled ? '已启用' : '已停用' }}</button></label><label>默认上传通道<select v-model="uploadConfig.channel"><option value="auto">自动（Open OAuth → Cookie）</option><option value="open_oauth">Open OAuth</option><option value="cookie">Cookie</option></select></label><label class="wide">本地接收目录<input v-model="uploadConfig.local_dir" placeholder="/media/115-upload" /></label><label class="wide">115 接收目录 CID<input v-model="uploadConfig.target_cid" placeholder="例如 0 或通过目录管理选择" /></label><label>目录扫描间隔（秒）<input v-model.number="uploadConfig.scan_interval_seconds" type="number" min="1" max="3600" /></label><label>文件稳定等待（秒）<input v-model.number="uploadConfig.stable_seconds" type="number" min="1" max="3600" /></label></div><footer><span>分片大小：32 MiB · 单文件顺序上传 · 服务端断点恢复</span><button class="board-button solid" @click="saveSettingTab('uploads')"><Check :size="15" />保存上传设置</button></footer></template>
        <template v-else-if="settingsTab === 'pan'"><header><div><span class="board-kicker">115 ACCOUNT</span><h2>115 登录</h2></div><span class="board-status" :class="{ success: state.account.username, danger: !state.account.username }">{{ state.account.username ? '已连接' : '未连接' }}</span></header><p class="settings-help">Cookie 和扫码登录共用同一 115 会话，登录成功后目录管理与转存任务立即可用。</p><label>115 Cookie<textarea v-model="settingsForm.panCookie" rows="5" autocomplete="off" placeholder="粘贴完整 Cookie"></textarea></label><div class="button-row"><button class="board-button solid" @click="savePanCookie"><Check :size="15" />验证并保存 Cookie</button><button class="board-button quiet" @click="test115"><RefreshCw :size="15" />测试连接</button><button class="board-button quiet" :disabled="qr.busy" @click="startQRLogin"><Cloud :size="15" />扫码登录</button></div><div v-if="qr.image" class="qr-login-card"><img :src="qr.image" alt="115 登录二维码" /><div><strong>使用 115 App 扫码确认</strong><span>状态：{{ qr.state === 'scanned' ? '已扫码，等待确认' : qr.state === 'confirmed' ? '登录成功' : qr.state === 'expired' ? '二维码已过期' : '等待扫码' }}</span><button class="board-button quiet tiny" @click="closeQR">关闭二维码</button></div></div><footer><span>账号：{{ state.account.username || '未读取' }}</span></footer></template>
        <template v-else-if="settingsTab === 'tmdb'"><header><div><span class="board-kicker">TMDB RECOGNITION</span><h2>TMDB 识别</h2></div><span class="board-status success">接口配置</span></header><p class="settings-help">用于媒体匹配、识别缓存和整理链路预览。</p><label>API Read Access Token<input v-model="settingsForm.tmdbToken" type="password" autocomplete="off" placeholder="输入后保存" /></label><footer><span>令牌由服务端加密保存</span><div class="button-row"><button class="board-button quiet" @click="testTMDB"><RefreshCw :size="15" />测试 TMDB</button><button class="board-button solid" @click="saveSettingTab('tmdb')"><Check :size="15" />保存 TMDB</button></div></footer></template>
        <template v-else-if="settingsTab === 'jellyfin'"><header><div><span class="board-kicker">JELLYFIN GATEWAY</span><h2>Jellyfin 网关</h2></div><span class="board-status success">可测试</span></header><div class="settings-grid"><label>协议<select v-model="settingsForm.jellyfin.protocol"><option value="http">HTTP</option><option value="https">HTTPS</option></select></label><label>主机<input v-model="settingsForm.jellyfin.host" placeholder="jellyfin" /></label><label>端口<input v-model.number="settingsForm.jellyfin.port" type="number" min="1" max="65535" /></label><label>基础路径<input v-model="settingsForm.jellyfin.base_path" placeholder="/" /></label><label class="wide">兼容地址<input v-model="settingsForm.jellyfin.url" placeholder="http://jellyfin:8096" /></label><label class="wide">API Key<input v-model="settingsForm.jellyfin.api_key" type="password" autocomplete="off" /></label></div><footer><span>保存后可测试原始后端连接</span><div class="button-row"><button class="board-button quiet" @click="testJellyfin"><RefreshCw :size="15" />测试连接</button><button class="board-button solid" @click="saveSettingTab('jellyfin')"><Check :size="15" />保存 Jellyfin</button></div></footer></template>
        <template v-else-if="settingsTab === 'wecom'"><header><div><span class="board-kicker">WECOM CALLBACK</span><h2>企业微信</h2></div><span class="board-status" :class="{ success: settingsForm.wecom.enabled, warn: !settingsForm.wecom.enabled }">{{ settingsForm.wecom.enabled ? '已启用' : '未启用' }}</span></header><label class="setting-toggle"><span>启用企业微信</span><button class="toggle-control" :class="{ on: settingsForm.wecom.enabled }" @click="settingsForm.wecom.enabled = !settingsForm.wecom.enabled"><i></i>{{ settingsForm.wecom.enabled ? '已启用' : '已停用' }}</button></label><div class="settings-grid"><label class="wide">公网管理地址<input v-model="settingsForm.wecom.callback_base_url" placeholder="https://direct.example.com" /></label><label>CorpID<input v-model="settingsForm.wecom.corp_id" /></label><label>AgentID<input v-model="settingsForm.wecom.agent_id" /></label><label class="wide">应用 Secret<input v-model="settingsForm.wecom.secret" type="password" autocomplete="off" /></label><label class="wide">签名 Token<input v-model="settingsForm.wecom.token" type="password" autocomplete="off" /></label><label class="wide">EncodingAESKey<input v-model="settingsForm.wecom.encoding_aes_key" type="password" autocomplete="off" /></label><label class="wide">允许成员 UserID<textarea v-model="settingsForm.wecom.allow_users" rows="3"></textarea></label></div><footer><span>回调设置保存后生效</span><div class="button-row"><button class="board-button quiet" @click="testWeCom"><RefreshCw :size="15" />测试企业微信</button><button class="board-button solid" @click="saveSettingTab('wecom')"><Check :size="15" />保存企业微信</button></div></footer></template>
        <template v-else-if="settingsTab === 'users'"><header><div><span class="board-kicker">ACCESS CONTROL</span><h2>用户与权限</h2></div><span class="board-status success">多用户基础</span></header><p class="settings-help">管理员可以创建操作员和只读用户。权限由后端校验，会话失效和禁用用户会立即生效。</p><div class="advanced-setting-block"><h3>创建用户</h3><div class="settings-grid"><label>用户名<input v-model="newUser.username" autocomplete="off" placeholder="例如 operator" /></label><label>初始密码<input v-model="newUser.password" type="password" autocomplete="new-password" placeholder="至少 8 个字符" /></label><label>角色<select v-model="newUser.role"><option value="operator">操作员</option><option value="viewer">只读</option><option value="admin">管理员</option></select></label><label class="setting-action"><span>&nbsp;</span><button class="board-button solid" @click="createManagedUser"><Check :size="15" />创建用户</button></label></div></div><div class="advanced-setting-block"><h3>已配置用户</h3><div class="user-management-list"><div v-for="user in managedUsers" :key="user.username" class="user-management-row"><div><strong>{{ user.username }}</strong><small>{{ user.role === 'admin' ? '管理员' : user.role === 'operator' ? '操作员' : '只读用户' }} · {{ user.enabled ? '已启用' : '已停用' }}</small></div><div class="button-row"><button class="board-button quiet tiny" @click="updateManagedUser(user, { enabled: !user.enabled })">{{ user.enabled ? '停用' : '启用' }}</button><button class="board-button quiet tiny danger-button" :disabled="user.username === login.username" @click="deleteManagedUser(user)">删除</button></div></div><div v-if="!managedUsers.length" class="kanban-empty">暂无用户</div></div></div></template>
        <template v-else-if="settingsTab === 'advanced'"><header><div><span class="board-kicker">ADVANCED OPTIONS</span><h2>高级设置</h2></div><span class="board-status success">完整配置</span></header><p class="settings-help">补齐播放、代理、日志和识别缓存的服务端设置，敏感字段留空会保持已保存值。</p><div class="advanced-setting-block"><h3>播放链路</h3><div class="settings-grid"><label class="wide">公网播放地址<input v-model="settingsForm.playback.public_url" placeholder="https://media.example.com" /></label><label class="setting-toggle"><span>播放链接诊断</span><button class="toggle-control" :class="{ on: settingsForm.playback.check_link }" @click="settingsForm.playback.check_link = !settingsForm.playback.check_link"><i></i>{{ settingsForm.playback.check_link ? '已启用' : '已停用' }}</button></label></div></div><div class="advanced-setting-block"><h3>网络代理</h3><div class="settings-grid"><label class="setting-toggle"><span>启用代理</span><button class="toggle-control" :class="{ on: settingsForm.proxy.enabled }" @click="settingsForm.proxy.enabled = !settingsForm.proxy.enabled"><i></i>{{ settingsForm.proxy.enabled ? '已启用' : '已停用' }}</button></label><label>代理地址<input v-model="settingsForm.proxy.url" placeholder="socks5h://proxy:1080" /></label><label>代理用户名<input v-model="settingsForm.proxy.username" /></label><label>代理密码<input v-model="settingsForm.proxy.password" type="password" autocomplete="off" placeholder="留空保持不变" /></label><label class="wide">绕过主机 / CIDR<textarea v-model="settingsForm.proxy.bypass" rows="2" placeholder="127.0.0.1，10.0.0.0/8"></textarea></label></div><div class="settings-checks"><label><input v-model="settingsForm.proxy.tmdb" type="checkbox" />TMDB</label><label><input v-model="settingsForm.proxy.images" type="checkbox" />图片</label><label><input v-model="settingsForm.proxy.pan" type="checkbox" />115</label><label><input v-model="settingsForm.proxy.wecom" type="checkbox" />企业微信</label><label><input v-model="settingsForm.proxy.jellyfin" type="checkbox" />Jellyfin</label><label><input v-model="settingsForm.proxy.bypass_private" type="checkbox" />绕过内网</label></div></div><div class="advanced-setting-block"><h3>日志与识别缓存</h3><div class="settings-grid"><label>日志保留天数<input v-model.number="settingsForm.logging.days" type="number" min="1" max="365" /></label><label>日志最大条数<input v-model.number="settingsForm.logging.max_entries" type="number" min="100" max="500000" /></label><label>日志级别<select v-model="settingsForm.logging.level"><option value="debug">debug</option><option value="info">info</option><option value="warning">warning</option><option value="error">error</option></select></label><label>缓存有效期（天）<input v-model.number="settingsForm.recognition.ttl_days" type="number" min="1" max="365" /></label><label>缓存最大条数<input v-model.number="settingsForm.recognition.max_entries" type="number" min="10" max="10000" /></label><label class="setting-toggle"><span>启用识别缓存</span><button class="toggle-control" :class="{ on: settingsForm.recognition.enabled }" @click="settingsForm.recognition.enabled = !settingsForm.recognition.enabled"><i></i>{{ settingsForm.recognition.enabled ? '已启用' : '已停用' }}</button></label></div></div><footer><span>四类设置会分别保存到服务端</span><button class="board-button solid" @click="saveSettingTab('advanced')"><Check :size="15" />保存高级设置</button></footer></template>
        <template v-else><header><div><span class="board-kicker">CMS COMPATIBILITY</span><h2>CMS 兼容</h2></div><span class="board-status" :class="{ success: cmsStatus.resolver_ready, warn: !cmsStatus.resolver_ready }">{{ cmsStatus.resolver_ready ? '解析器就绪' : '未启用' }}</span></header><p class="settings-help">兼容旧 CMS 播放地址并将 STRM 媒体导入本地记录。系统只复用 Cookie 读取直链，不会调用旧 CMS 登录接口。</p><label class="setting-toggle"><span>启用 CMS 接管</span><button class="toggle-control" :class="{ on: settingsForm.cms.enabled }" @click="settingsForm.cms.enabled = !settingsForm.cms.enabled"><i></i>{{ settingsForm.cms.enabled ? '已启用' : '已停用' }}</button></label><div class="settings-grid"><label class="wide">旧 CMS 播放地址（每行一个）<textarea v-model="settingsForm.cms.origins" rows="3" placeholder="https://cms.example.com"></textarea></label><label class="wide">继承 STRM 根目录（每行一个）<textarea v-model="settingsForm.cms.strm_roots" rows="3" placeholder="/media/115-strm"></textarea></label><label class="wide">复用 Cookie<input v-model="settingsForm.cms.cookie" type="password" autocomplete="off" :placeholder="cmsStatus.cookie_configured ? '已配置，留空保持不变' : '粘贴旧 CMS Cookie'" /></label><label>115 请求间隔（毫秒）<input v-model.number="settingsForm.cms.interval_ms" type="number" min="200" max="60000" /></label></div><label class="setting-toggle"><span>启用旧 /d 兼容入口</span><button class="toggle-control" :class="{ on: settingsForm.cms.legacy_enabled }" @click="settingsForm.cms.legacy_enabled = !settingsForm.cms.legacy_enabled"><i></i>{{ settingsForm.cms.legacy_enabled ? '已启用' : '已停用' }}</button></label><label class="setting-toggle"><span>迁移只读模式</span><button class="toggle-control" :class="{ on: settingsForm.cms.migration_read_only }" @click="settingsForm.cms.migration_read_only = !settingsForm.cms.migration_read_only"><i></i>{{ settingsForm.cms.migration_read_only ? '已启用' : '已停用' }}</button></label><div class="cms-status-grid"><span>Cookie：<strong>{{ cmsStatus.cookie_configured ? '已配置' : '未配置' }}</strong></span><span>已导入：<strong>{{ cmsStatus.imported || 0 }}</strong></span><span>运行模式：<strong>{{ cmsStatus.mode || 'independent-115' }}</strong></span><span v-if="cmsStatus.restart_required" class="board-status warn">重启后完全生效</span></div><footer><span>保存后可检查 Cookie 和兼容状态</span><div class="button-row"><button class="board-button quiet" @click="checkCMS"><RefreshCw :size="15" />检查 CMS Cookie</button><button class="board-button solid" @click="saveSettingTab('cms')"><Check :size="15" />保存 CMS 设置</button></div></footer></template>
      </article></div></section>
      </Transition>
    </main>
    <div v-if="directoryPicker.open" class="board-modal-backdrop" @click.self="closeDirectoryPicker"><article class="board-modal directory-picker-modal"><header><div><span class="board-kicker">DIRECTORY PICKER</span><h2>{{ pickerLabel(directoryPicker.field) }}</h2></div><button class="board-icon" @click="closeDirectoryPicker">×</button></header><p class="settings-help">当前选择：{{ pickerPathDisplay() }}。只选择目录，文件不会被写入配置。</p><div class="directory-picker-toolbar"><button class="board-button quiet tiny" :disabled="directoryPicker.source === 'pan' ? directoryPicker.crumbs.length <= 1 : !directoryPicker.path" @click="pickerGoUp"><ArrowLeft :size="14" />返回上一级</button><span>{{ directoryPicker.loading ? '读取目录中…' : `${directoryPicker.files.filter(item => item.type === 'folder').length} 个子目录` }}</span><button class="board-button quiet tiny" @click="loadDirectoryPicker"><RefreshCw :size="14" />刷新</button></div><div class="directory-picker-list"><button v-for="file in directoryPicker.files" :key="file.path || file.name" :class="['directory-picker-row', { disabled: file.type !== 'folder' }]" :disabled="file.type !== 'folder'" @dblclick="enterPickerDirectory(file)"><span><Folder v-if="file.type === 'folder'" :size="18" /><File v-else :size="18" /><strong>{{ file.name }}</strong></span><small>{{ file.type === 'folder' ? '双击进入目录' : '文件' }}</small><ChevronRight v-if="file.type === 'folder'" :size="15" /></button><div v-if="!directoryPicker.files.length && !directoryPicker.loading" class="kanban-empty">当前目录为空，仍可选择当前目录。</div></div><footer><button class="board-button quiet" @click="closeDirectoryPicker">取消</button><button class="board-button solid" :disabled="directoryPicker.loading" @click="selectPickerDirectory"><Check :size="15" />选择当前目录</button></footer></article></div>
    <div v-if="matchTarget" class="board-modal-backdrop" @click.self="closeMatchModal"><article class="board-modal match-modal"><header><div><span class="board-kicker">TMDB MATCH PREVIEW</span><h2>{{ matchTarget.source === 'organization-batch' ? '批量纠错匹配' : matchTarget.source === 'organization' ? '媒体整理纠错' : '确认媒体匹配' }}</h2></div><button class="board-icon" @click="closeMatchModal">×</button></header><p class="settings-help">{{ matchTarget.source === 'organization-batch' ? `将为已选 ${selectedOrganizationKeys.length} 条记录使用同一个 TMDB 条目。` : `${matchTarget.job.title}：输入 TMDB ID 后先预览详情，确认无误再提交。` }}</p><div class="match-id-form"><label>媒体类型<select v-model="matchKind"><option value="movie">电影</option><option value="tv">电视剧</option></select></label><label>TMDB ID<input v-model="matchID" inputmode="numeric" placeholder="例如 603" @keyup.enter="previewMatchByID" /></label><button class="board-button solid" :disabled="matchPreviewBusy" @click="previewMatchByID"><Search :size="15" />{{ matchPreviewBusy ? '读取中' : '预览详情' }}</button></div><div v-if="matchPreview" class="tmdb-preview-card"><img v-if="matchPreview.poster_path" class="tmdb-preview-poster" :src="`/api/v1/tmdb/image?path=${encodeURIComponent(matchPreview.poster_path)}`" :alt="`${matchPreview.title} 海报`" /><div class="tmdb-preview-main"><strong>{{ matchPreview.title }}</strong><span>{{ matchPreview.year || '年份未知' }} · {{ matchPreview.kind === 'tv' ? '电视剧' : '电影' }} · TMDB #{{ matchPreview.id }}</span><small v-if="matchPreview.original_title">原名：{{ matchPreview.original_title }}</small><small v-if="matchPreview.kind === 'tv' && matchPreview.seasons?.length">剧集信息：{{ matchPreview.seasons.length }} 季</small><small v-if="matchPreview.episodeTitle || matchPreview.season !== undefined">剧集：S{{ String(matchPreview.season || 0).padStart(2, '0') }}-E{{ String(matchPreview.episode || 0).padStart(2, '0') }}{{ matchPreview.episodeTitle ? ` · ${matchPreview.episodeTitle}` : '' }}{{ matchSeasonBusy ? ' · 读取剧集详情…' : '' }}</small></div><p v-if="matchPreview.overview">{{ matchPreview.overview }}</p><p v-if="matchPreview.episodeOverview" class="tmdb-episode-overview">本集简介：{{ matchPreview.episodeOverview }}<template v-if="matchPreview.episodeAirDate"> · 播出：{{ matchPreview.episodeAirDate }}</template></p></div><div class="match-search"><input v-model="matchQuery" placeholder="也可以按片名搜索" @keyup.enter="searchMatchCandidates" /><button class="board-button quiet" @click="searchMatchCandidates"><Search :size="15" />搜索候选</button></div><div class="match-candidates"><button v-for="candidate in matchCandidates" :key="`${candidate.kind}-${candidate.id}`" class="match-candidate" @click="chooseMatch(candidate)"><strong>{{ candidate.title }}</strong><span>{{ candidate.year || '年份未知' }} · TMDB #{{ candidate.id }} · {{ candidate.kind === 'tv' ? '电视剧' : '电影' }}</span></button><div v-if="!matchCandidates.length" class="kanban-empty">输入片名可搜索候选，或直接输入 TMDB ID 预览。</div></div><footer><button class="board-button quiet" @click="closeMatchModal">取消</button><button class="board-button solid" :disabled="!matchPreview || matchPreviewBusy" @click="confirmMatchPreview"><Check :size="15" />确认并提交</button></footer></article></div>
    <div v-if="deletePreview.open" class="board-modal-backdrop" @click.self="closeDeletePreview"><article class="board-modal delete-modal"><header><div><span class="board-kicker">DELETE PREVIEW</span><h2>确认删除媒体记录</h2></div><button class="board-icon" @click="closeDeletePreview">×</button></header><p class="settings-help">删除前已生成联动预览。共 {{ deletePreview.items.length }} 条记录，执行后会按当前删除策略处理成品、源文件和关联元数据。</p><div class="delete-preview-list"><article v-for="item in deletePreview.items" :key="item.remoteID"><strong>{{ item.title }}</strong><code>{{ item.output || '尚未生成本地成品路径' }}</code><small v-if="item.retained?.length">保留项：{{ item.retained.join('；') }}</small><small v-if="item.assets?.length">元数据：{{ item.assets.join('；') }}</small></article></div><footer><button class="board-button quiet" :disabled="deletePreview.busy" @click="closeDeletePreview">取消</button><button class="board-button solid danger-button" :disabled="deletePreview.busy" @click="confirmOrganizationDelete"><Database :size="15" />{{ deletePreview.busy ? '删除中…' : '确认删除' }}</button></footer></article></div>
    <div v-if="operation.open" class="board-modal-backdrop" @click.self="closeFileOperation"><article class="board-modal"><header><div><span class="board-kicker">FILE OPERATION</span><h2>{{ operation.action === 'mkdir' ? '新建目录' : operation.action === 'rename' ? '重命名' : operation.action === 'move' ? '移动' : operation.action === 'copy' ? '复制' : '删除' }}</h2></div><button class="board-icon" @click="closeFileOperation">×</button></header><p class="settings-help">{{ operation.scope === '115' ? '115 分类目录操作会先生成预览，再按摘要执行。' : '本地 STRM 操作只允许处理已关联的媒体文件。' }}<br />当前对象：{{ operation.label }}</p><label v-if="operation.action !== 'delete'">{{ operation.action === 'move' || operation.action === 'copy' ? '目标目录（115 CID 或本地相对目录）' : operation.action === 'rename' ? '新名称' : '新目录名称' }}<input v-model="operation.target" :placeholder="operation.action === 'mkdir' ? '例如：华语电影' : '输入目标'" /></label><div v-if="operation.previewed" class="operation-preview"><strong>预览变更</strong><div v-for="change in operation.changes" :key="`${change.source}-${change.target}`"><code>{{ change.source || '-' }}</code><ArrowRight :size="14" /><code>{{ change.target || change.name || '-' }}</code></div></div><footer><button class="board-button quiet" @click="closeFileOperation">取消</button><button v-if="!operation.previewed" class="board-button solid" :disabled="operation.busy || (operation.action !== 'delete' && !operation.target.trim())" @click="previewFileOperation"><RefreshCw :size="15" />预览范围</button><button v-else class="board-button solid" :disabled="operation.busy" @click="executeFileOperation"><Check :size="15" />确认执行</button></footer></article></div>
    <div v-if="notice" class="board-toast"><Check :size="16" />{{ notice }}</div>
    </template>
  </div>
</template>
