import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const base = process.env.BASE_URL || 'http://127.0.0.1:29527'
const output = process.env.SCREENSHOT_DIR || '/tmp/115-direct-upgrade-screenshots'
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] })
const movie = '{{title}}{% if year %} ({{year}}){% endif %}/{{title}}{{fileExt}}'
const tv = '{{title}}{% if year %} ({{year}}){% endif %}/Season {{season}}/{{title}} - {{season_episode}}{% if episode %} - 第 {{episode}} 集{% endif %}{{fileExt}}'
const organization = { movie_template: movie, tv_template: tv, episode_regex: 'S(\\d+)E(\\d+)', mode: 'copy', version_policy: 'coexist', tv_version_policy: 'keep', movie_version_policy: 'quality', delete_policy: 'output', retire_policy: 'output' }
const classification = { movie_root: '电影', tv_root: '电视剧', rules: [{ name: '欧美电影', enabled: true, kind: 'movie', countries: null, languages: ['en'], genres: null, keyword: '', target: '欧美电影' }, { name: '国产剧', enabled: true, kind: 'tv', countries: ['CN'], languages: ['zh'], genres: [], keyword: '', target: '国产剧' }] }
const cacheConfig = { enabled: true, ttl_days: 30, max_entries: 5000, max_bytes: 67108864 }
const directories = { inbox_cid: 'inbox', library_cid: 'library', strm_path: '/fixture/output', pending_path: '/fixture/pending', gateway_url: 'https://gateway.example' }
const name = '春花焰.2024.S01E32.2160p.60FPS.DV.HEVC.Atmos.mkv'
const link = { remote_id: 'remote', source_path: '/fixture/pending/series/episode.strm', output_path: '', tmdb_id: 1, kind: 'tv', deleted: '', unavailable: false, quality: { resolution: 2160, fps: '60', hdr: 'DV' } }
const media = { remote_id: 'remote', id: 'stable', name, strm_path: link.source_path }
const calls = []
try {
  for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
    const page = await browser.newPage({ viewport })
    page.setDefaultTimeout(10000)
    let configured = true
    let cacheCleared = false
    const errors = []
    page.on('pageerror', e => errors.push(e.message))
    await page.route('**/api/v1/**', async route => {
      const req = route.request(), url = new URL(req.url()), path = url.pathname
      const body = req.postDataJSON()
      calls.push({ path, query: url.search, body })
      let data = { ok: true }
      if (path === '/api/v1/session') data = { authenticated: true, csrf: 'fixture-csrf' }
      else if (path === '/api/v1/dashboard') data = { stats: { jobs: 2, media: 1, failed: 0, pending: 1 }, cookie_ok: true }
      else if (path === '/api/v1/115/account') data = { user_id: 1, username: 'Fixture', vip: true, vip_level: 1, space_total: 100, space_used: 50, space_remain: 50 }
      else if (path === '/api/v1/transfers') data = req.method() === 'POST' ? { id: 'saved-share', title: '间谍过家家 白', status: 'completed', duplicate: true } : { items: [] }
      else if (path === '/api/v1/sync/status') data = { running: false, mode: 'full', files: 96, created: 2, unavailable: 0, last_run: new Date().toISOString() }
      else if (path === '/api/v1/records/transfers') data = { items: [{ id: 'saved-share', title: '间谍过家家 白', status: 'completed', source: 'web', tmdb_kind: 'movie', tmdb_id: 1062807, created_at: new Date().toISOString(), share_url: 'https://115.com/s/fixture' }], total: 27 }
      else if (path === '/api/v1/records/sync') data = { items: [{ id: 'sync-fixture', mode: 'full', trigger: 'manual', status: 'completed', files: 96, created: 2, unavailable: 0, library_cid: 'library', started_at: new Date().toISOString(), finished_at: new Date().toISOString() }], total: 1 }
      else if (path === '/api/v1/settings/classification' || path === '/api/v1/classification/defaults') data = classification
      else if (path === '/api/v1/classification/preview') data = { title: 'The Matrix', root: body.config.movie_root, subcategory: '欧美电影', rule: '欧美电影', needs_confirmation: false }
      else if (path === '/api/v1/settings/recognition') data = cacheConfig
      else if (path === '/api/v1/recognition-cache/all/delete') { cacheCleared = true; data = { ok: true } }
      else if (path === '/api/v1/recognition-cache') data = { config: cacheConfig, items: cacheCleared ? [] : [{ key: 'fixture-cache', kind: 'match', label: 'The Matrix', hits: 5, bytes: 100, expires_at: new Date(Date.now()+86400000).toISOString(), last_used_at: new Date().toISOString() }], total: cacheCleared ? 0 : 1 }
      else if (path === '/api/v1/settings/sync') data = { enabled: true, interval_minutes: 5 }
      else if (path === '/api/v1/settings/directories') data = configured ? directories : { ...directories, inbox_cid: '', library_cid: '' }
      else if (path === '/api/v1/settings/organization' || path === '/api/v1/organize/defaults') data = organization
      else if (path === '/api/v1/settings/proxy') data = { enabled: false, url: '', tmdb: true, images: true, pan: false, bypass: null }
      else if (path === '/api/v1/settings/logging') data = { days: 45, max_entries: 50000, level: 'info' }
      else if (path.startsWith('/api/v1/settings/')) data = {}
      else if (path === '/api/v1/media') data = { media: [media], links: [link] }
      else if (path === '/api/v1/executions') data = { items: [{ id: 'organize:fixture:remote', kind: 'organize', status: 'mapped', error: '清理失败，等待重试', updated_at: new Date().toISOString() }] }
      else if (path === '/api/v1/deletion-reviews') data = { items: [{ id: 'cloud:remote', remote_id: 'remote', reason: '云端源缺失；保留成品和关联，等待确认删除或移出目录', status: 'pending' }] }
      else if (path === '/api/v1/files/strm/content') data = { content: 'https://gateway.example/direct/stable?sig=fixture' }
      else if (path === '/api/v1/files/strm') data = { path: '', entries: [{ path: 'series', name: '春花焰 (2024)', directory: true, size: 0 }, { path: 'episode.strm', name: '春花焰 - S01E32 - 第 32 集.strm', directory: false, size: 190 }] }
      else if (path === '/api/v1/files/115') data = { entries: url.searchParams.get('cid') === '0'
        ? [{ id: 'library', name: '云端分类库', directory: true, size: 0 }, { id: 'inbox', name: '转存接收库', directory: true, size: 0 }, { id: 'other', name: '其他目录', directory: true, size: 0 }]
        : [{ id: 'series', name: '原始接收分类副本', directory: true, size: 0 }] }
      else if (path === '/api/v1/files/preview') data = { digest: 'file-preview', request: body, changes: [{ source: body.paths[0], target: body.target }], affected: [] }
      else if (path === '/api/v1/tmdb/search') data = { items: [{ id: 1, kind: 'tv', title: '春花焰', year: 2024 }] }
      else if (path === '/api/v1/organize/template/preview') data = { path: '春花焰 (2024)/Season 01/春花焰 - S01E32 - 第 32 集.strm' }
      else if (path === '/api/v1/organize/preview') data = { digest: 'preview-digest', request: body, details: { title: '春花焰', id: 1, kind: 'tv' }, retire_policy: 'output', items: [{ media, link: { ...link, mode: body.mode, output_path: '/fixture/output/电视剧/国产剧/春花焰 (2024)/Season 01/春花焰 - S01E32 - 第 32 集 - 2160P [v-unique].strm', version_group: 'tv:1:1:32-32' }, retire: [] }] }
      else if (path.endsWith('/delete-preview')) data = { remote_id: 'remote', digest: 'delete-digest', policy: 'output', output: '/fixture/output/series/episode.strm', source: '', cloud: '', retained: ['共享海报仍有引用'], assets: [] }
      else if (path === '/api/v1/logs') data = { items: [{ id: 1, category: 'organize', level: 'info', message: '任务已完成；原播放ID保留', created_at: new Date().toISOString() }], total: 150 }
      await route.fulfill({ json: data })
    })
    await page.goto(base)
    const navigate = async name => {
      if (viewport.width < 600) await page.getByRole('button', { name: '导航', exact: true }).click()
      await page.locator('aside nav').getByRole('button', { name, exact: false }).click()
    }
    // A numeric policies key previously collided with the overview branch key.
    await navigate('整理策略')
    await page.getByLabel('电视剧版本策略').waitFor()
    assert.equal(await page.locator('.overview-banner,.transfer-form,.account-overview').count(), 0, 'overview DOM leaked into policies')
    await page.getByRole('tab', { name: '分类策略', exact: true }).click()
    await page.getByRole('button', { name: '保存分类策略', exact: true }).waitFor()
    const savePolicy = page.waitForRequest(r => r.method() === 'PUT' && new URL(r.url()).pathname === '/api/v1/settings/classification')
    await page.getByRole('button', { name: '保存分类策略', exact: true }).click()
    await savePolicy
    await page.getByText('分类策略已保存', { exact: true }).waitFor()
    assert.equal(await page.locator('.qr-modal, .modal-wrap').count(), 0, 'classification save opened a cookie or QR dialog')
    await page.getByLabel('分类预览TMDB ID', { exact: true }).fill('603')
    const previewPolicy = page.waitForRequest(r => r.method() === 'POST' && new URL(r.url()).pathname === '/api/v1/classification/preview')
    await page.getByRole('button', { name: '预览分类', exact: true }).click()
    await previewPolicy
    await page.getByText('电影/欧美电影', { exact: true }).waitFor()
    assert.equal(await page.locator('.modal-wrap').count(), 0, 'classification preview opened cookie dialog')
    await page.screenshot({ path: `${output}/overview-to-policies-${viewport.width}.png`, fullPage: true })
    for (let i = 0; i < 8; i++) {
      await page.getByRole('button', { name: '刷新当前页面', exact: true }).click()
      await page.getByRole('button', { name: '保存分类策略', exact: true }).waitFor()
      assert.equal(await page.locator('.overview-banner,.transfer-form,.account-overview').count(), 0, 'refresh revision collided with a page branch key')
    }
    await navigate('运行概览')
    await page.getByRole('button', { name: '导入 Cookie', exact: true }).waitFor()
    await navigate('整理策略')
    assert.equal(await page.getByRole('button', { name: '导入 Cookie', exact: true }).count(), 0, 'cookie button retained after re-entering policies')
    await page.getByRole('tab', { name: '版本与命名', exact: true }).click()
    await navigate('目录管理')
    const location = page.locator('[aria-label="当前存储位置"]')
    // Named storage tabs, roots, breadcrumbs, and operation dialogs must agree.
    await page.getByRole('tab', { name: '115网盘', exact: true }).click()
    await page.getByRole('button', { name: '云端分类库', exact: true }).waitFor()
    assert.match(await location.textContent(), /115网盘根目录 · CID: 0/)
    assert(calls.some(c => c.path === '/api/v1/files/115' && c.query === '?cid=0'))
    assert.equal(await page.getByRole('button', { name: '115网盘：新建目录', exact: true }).isEnabled(), true)
    await page.getByRole('button', { name: '115网盘：新建目录', exact: true }).click()
    await page.getByLabel('新目录名称', { exact: true }).fill('上传目标目录')
    assert.equal(await page.locator('.workflow-modal select option').count(), 1, 'root create dialog offered media mutations')
    await page.getByRole('button', { name: '预览范围', exact: true }).click()
    await page.waitForFunction(() => !document.querySelector('.workflow-modal button.primary')?.disabled)
    assert(calls.some(c => c.path === '/api/v1/files/preview' && c.body.action === 'mkdir' && c.body.paths[0] === '0' && c.body.target === '上传目标目录'))
    await page.getByRole('button', { name: '关闭', exact: true }).click()
    await page.getByRole('button', { name: '其他目录', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('[aria-label="当前存储位置"]')?.textContent.includes('CID: other'))
    await page.getByRole('button', { name: '网盘根目录', exact: true }).click()
    await page.getByRole('button', { name: '云端分类库', exact: true }).waitFor()
    await page.screenshot({ path: `${output}/cloud-root-${viewport.width}.png`, fullPage: true })
    await page.getByRole('button', { name: '云端分类库', exact: true }).click()
    await page.getByRole('button', { name: '原始接收分类副本', exact: true }).waitFor()
    assert.equal(await page.getByRole('button', { name: '115网盘：新建目录', exact: true }).isEnabled(), true)
    await page.getByRole('button', { name: '115网盘根目录', exact: true }).click()
    await page.getByRole('button', { name: '云端分类库', exact: true }).waitFor()
    await page.getByRole('button', { name: '接收目录', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('[aria-label="当前存储位置"]')?.textContent.includes('CID: inbox'))
    assert.equal(await page.getByRole('button', { name: '115网盘：新建目录', exact: true }).isEnabled(), true)
    await page.getByRole('button', { name: '分类目录', exact: true }).click()
    await page.getByRole('button', { name: '原始接收分类副本', exact: true }).waitFor()
    assert.match(await location.textContent(), /115分类目录 · CID: library/)
    await page.getByRole('button', { name: '原始接收分类副本', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('[aria-label="当前存储位置"]')?.textContent.includes('CID: series'))
    await page.getByRole('button', { name: '115网盘：新建目录', exact: true }).click()
    assert.match(await page.locator('[aria-label="操作存储位置"]').textContent(), /115网盘.*CID: series/)
    await page.locator('.workflow-modal input').fill('新建网盘文件夹')
    await page.getByRole('button', { name: '预览范围', exact: true }).click()
    await page.getByRole('button', { name: '确认执行', exact: true }).waitFor()
    await page.waitForFunction(() => !document.querySelector('button[title="115网盘：新建目录"]')?.disabled)
    assert(calls.some(c => c.path === '/api/v1/files/preview' && c.body.scope === '115' && c.body.paths[0] === 'series'))
    await page.screenshot({ path: `${output}/cloud-operation-${viewport.width}.png`, fullPage: true })
    await page.getByRole('button', { name: '关闭', exact: true }).click()
    await page.screenshot({ path: `${output}/cloud-files-${viewport.width}.png`, fullPage: true })
    await page.getByRole('tab', { name: '本地 · 刮削成品', exact: true }).click()
    await page.getByRole('checkbox', { name: '选择 春花焰 (2024)', exact: true }).waitFor()
    assert.match(await location.textContent(), /本地 · 刮削成品.*\/fixture\/output/)
    assert(calls.some(c => c.path === '/api/v1/files/strm' && c.query.includes('scope=output')))
    await page.screenshot({ path: `${output}/output-files-${viewport.width}.png`, fullPage: true })
    await page.getByRole('tab', { name: '本地 · 待整理STRM', exact: true }).click()
    await page.getByRole('checkbox', { name: '选择 春花焰 (2024)', exact: true }).waitFor()
    assert.match(await location.textContent(), /本地 · 待整理STRM.*\/fixture\/pending/)
    assert(calls.some(c => c.path === '/api/v1/files/strm' && c.query.includes('scope=pending')))
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'directory overflow')
    await page.getByRole('checkbox', { name: '选择 春花焰 (2024)', exact: true }).check()
    await page.getByRole('button', { name: '整理 / 纠错' }).click()
    assert.match(await page.locator('[aria-label="操作存储位置"]').textContent(), /本地 · 待整理STRM/)
    await page.locator('.workflow-modal select').first().selectOption('tv')
    await page.locator('.workflow-modal input[type=number]').first().fill('1')
    await page.getByRole('button', { name: '预览', exact: true }).click()
    await page.getByRole('button', { name: '确认整理' }).waitFor({ state: 'visible' })
    await page.screenshot({ path: `${output}/organize-${viewport.width}.png`, fullPage: true })
    assert.equal(await page.getByRole('button', { name: '确认整理' }).isEnabled(), true)
    await page.getByRole('button', { name: '确认整理' }).click()
    assert(calls.some(c => c.path === '/api/v1/organize/execute' && c.body.digest === 'preview-digest'))
    await page.getByRole('button', { name: '明确删除预览' }).click()
    await page.getByRole('button', { name: '确认删除', exact: true }).waitFor()
    await page.screenshot({ path: `${output}/delete-${viewport.width}.png`, fullPage: true })
    await page.getByRole('button', { name: '取消', exact: true }).click()
    await page.screenshot({ path: `${output}/files-${viewport.width}.png`, fullPage: true })
    await navigate('系统设置')
    await page.waitForFunction(() => document.querySelector('.workflow input[type=number]')?.value === '45')
    assert.equal(await page.getByLabel('保留天数', { exact: true }).inputValue(), '45')
    assert.equal(await page.locator('.workflow-error').count(), 0, 'null proxy bypass must not interrupt settings loading')
    assert.equal(await page.getByLabel('绕过主机 / CIDR').inputValue(), '')
    assert.equal(await page.getByLabel('同步STRM目录（待整理）', { exact: true }).inputValue(), directories.pending_path)
    assert.equal(await page.getByLabel('刮削成品目录（Jellyfin扫描）', { exact: true }).inputValue(), directories.strm_path)
    assert.equal(await page.getByLabel('STRM 本地目录', { exact: true }).count(), 0)
    await page.getByRole('button', { name: '保存代理', exact: true }).click()
    assert(calls.some(c => c.path === '/api/v1/settings/proxy' && Array.isArray(c.body?.bypass) && c.body.bypass.length === 0))
    await page.screenshot({ path: `${output}/settings-${viewport.width}.png`, fullPage: true })
    await navigate('整理策略')
    assert.equal(await page.getByLabel('电视剧版本策略').inputValue(), 'keep')
    assert.equal(await page.getByLabel('电影版本策略').inputValue(), 'quality')
    await page.getByText('命名预览', { exact: true }).click()
    await page.getByRole('button', { name: '预览', exact: true }).click()
    await page.getByText('春花焰 (2024)/Season 01/春花焰 - S01E32 - 第 32 集.strm', { exact: true }).waitFor()
    await page.evaluate(() => scrollTo(0, 0))
    await page.screenshot({ path: `${output}/policies-${viewport.width}.png`, fullPage: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'horizontal overflow')
    await page.getByRole('tab', { name: '分类策略', exact: true }).click()
    await page.getByLabel('电影一级目录', { exact: true }).fill('Films')
    await page.getByRole('button', { name: '保存分类策略', exact: true }).click()
    assert(calls.some(c => c.path === '/api/v1/settings/classification' && c.body?.movie_root === 'Films'))
    await page.getByLabel('分类预览TMDB ID', { exact: true }).fill('603')
    await page.getByRole('button', { name: '预览分类', exact: true }).click()
    await page.getByText('Films/欧美电影', { exact: true }).waitFor()
    await page.screenshot({ path: `${output}/classification-${viewport.width}.png`, fullPage: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'classification overflow')
    await navigate('媒体同步')
    const incremental = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/sync/incremental')
    await page.getByRole('button', { name: '增量同步', exact: true }).click()
    await incremental
    assert(calls.some(c => c.path === '/api/v1/sync/incremental' && c.body))
    await page.getByRole('button', { name: '全量同步', exact: true }).click()
    await page.getByRole('dialog', { name: '确认全量同步' }).waitFor()
    await page.getByRole('button', { name: '确认全量同步', exact: true }).click()
    assert(calls.some(c => c.path === '/api/v1/sync/full' && c.body))
    await page.getByRole('dialog').waitFor({ state: 'hidden' })
    await page.screenshot({ path: `${output}/sync-${viewport.width}.png`, fullPage: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'sync overflow')
    await navigate('记录中心')
    await page.getByRole('button', { name: '查看记录详情', exact: true }).click()
    await page.getByRole('dialog', { name: '记录详情' }).waitFor()
    await page.getByRole('button', { name: '关闭记录弹窗' }).click()
    const nextPage = page.waitForResponse(r => r.url().includes('/api/v1/records/transfers?') && r.url().includes('offset=25'))
    await page.getByRole('button', { name: '下一页', exact: true }).click()
    await nextPage
    assert(calls.some(c => c.path === '/api/v1/records/transfers' && c.query.includes('offset=25')))
    await page.getByRole('tab', { name: '同步记录', exact: true }).click()
    await page.getByText('sync-fixture', { exact: true }).waitFor()
    await page.screenshot({ path: `${output}/records-${viewport.width}.png`, fullPage: true })
    await navigate('识别缓存')
    await page.getByText('The Matrix', { exact: true }).waitFor()
    await page.getByRole('button', { name: '清空缓存', exact: true }).click()
    await page.getByRole('button', { name: '确认清空', exact: true }).click()
    await page.getByText('暂无缓存', { exact: true }).waitFor()
    assert(calls.some(c => c.path === '/api/v1/recognition-cache/all/delete' && c.body.confirm))
    await page.screenshot({ path: `${output}/cache-${viewport.width}.png`, fullPage: true })
    await navigate('转存与整理')
    await page.getByLabel('分享链接', { exact: true }).fill('https://115.com/s/fixture')
    await page.getByRole('button', { name: '提交任务', exact: true }).click()
    await page.locator('.success-toast').filter({ hasText: '分享已保存' }).waitFor()
    await page.getByRole('button', { name: '打开实时日志窗口' }).click()
    await page.getByRole('dialog', { name: '实时日志窗口' }).waitFor()
    await page.getByLabel('窗口日志分类').selectOption('recognition')
    await page.getByRole('button', { name: '暂停日志更新' }).click()
    await page.screenshot({ path: `${output}/live-logs-${viewport.width}.png`, fullPage: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'live log overflow')
    await page.keyboard.press('Escape')
    await navigate('运行日志')
    await page.screenshot({ path: `${output}/logs-${viewport.width}.png`, fullPage: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'log overflow')
    assert.deepEqual(errors, [])
    configured = false
    await page.reload()
    await navigate('目录管理')
    await page.getByRole('tab', { name: '115网盘', exact: true }).click()
    await page.getByRole('button', { name: '云端分类库', exact: true }).waitFor()
    assert.match(await location.textContent(), /CID: 0/)
    assert.equal(await page.getByRole('button', { name: '分类目录', exact: true }).isEnabled(), false)
    assert.equal(await page.locator('.workflow-error').count(), 0, 'Unconfigured classification must not block root browsing')
    assert.deepEqual(errors, [])
    await page.close()
  }
  if (process.env.UI_ONLY !== '1') {
  // Real backend session, CSRF, rooted file preview, and persisted mkdir step.
  const context = await browser.newContext()
  const testPassword = process.env.TEST_PASSWORD
  assert.ok(testPassword, 'TEST_PASSWORD must be set for the backend smoke test')
  const login = await context.request.post(`${base}/api/v1/session/login`, { data: { username: 'admin', password: testPassword } })
  assert.equal(login.status(), 200)
  const session = await login.json(), headers = { 'X-CSRF-Token': session.csrf }
  const proxy = await context.request.get(`${base}/api/v1/settings/proxy`)
  assert.equal(proxy.status(), 200)
  assert(Array.isArray((await proxy.json()).bypass))
  const file = { scope: 'pending', action: 'mkdir', paths: [`smoke-${Date.now()}`], target: '' }
  const preview = await context.request.post(`${base}/api/v1/files/preview`, { headers, data: file })
  assert.equal(preview.status(), 200, await preview.text())
  const p = await preview.json()
  const execute = await context.request.post(`${base}/api/v1/files/execute`, { headers, data: { ...file, digest: p.digest } })
  assert.equal(execute.status(), 200, await execute.text())
  const result = await execute.json()
  assert.equal(result.items[0].status, 'completed')
  const denied = await context.request.post(`${base}/api/v1/files/execute`, { data: file })
  assert.equal(denied.status(), 403)
  await context.close()
  }
  console.log(`Desktop/mobile storage labels, preview scope, and screenshots passed.${process.env.UI_ONLY === '1' ? '' : ' Real backend mkdir and CSRF checks passed.'}`)
} finally { await browser.close() }
