import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const output = process.env.SCREENSHOT_DIR || '/var/tmp/115-direct-cms-config-deploy/ui'
await mkdir(output, { recursive: true })
const browser = await chromium.launch({headless: true, executablePath: process.env.CHROME_PATH, args: ['--no-sandbox']})
try {
 for (const width of [1440, 390]) {
  const page = await browser.newPage({viewport: {width, height: 1000}}), errors = []
  let saved = {enabled: false, cookie: '********', origins: null, strm_roots: null, interval_ms: 1000}, imported = false, posted
  page.on('pageerror', e => errors.push(e.message))
  await page.route('**/api/v1/**', async route => {
   const r = route.request(), p = new URL(r.url()).pathname
   let data = {}
   if (p === '/api/v1/session') data = {authenticated: true, csrf: 'fixture-csrf'}
   else if (p === '/api/v1/dashboard') data = {stats: {jobs: 0, media: 0}, cookie_ok: true}
   else if (p === '/api/v1/115/account') data = {user_id: 1, username: 'Fixture'}
   else if (p === '/api/v1/settings/cms') {
    if (r.method() === 'PUT') { posted = r.postDataJSON(); saved = {...posted, cookie: '********'}; assert.equal(r.headers()['x-csrf-token'], 'fixture-csrf'); data = {ok: true} } else data = saved
   } else if (p === '/api/v1/cms/status') data = {enabled: saved.enabled, resolver_ready: saved.enabled, imported: imported ? 1 : 0, read_only_active: false, restart_required: !!saved.migration_read_only, legacy_listen: ':9528'}
   else if (p === '/api/v1/cms/records') data = {items: imported ? [{path: '/mnt/cms-media/中文电影.strm', pick_code: 'registered123'}] : [], total: imported ? 1 : 0}
   else if (p === '/api/v1/cms/parse') { assert.equal(r.headers()['x-csrf-token'], 'fixture-csrf'); data = {pick_code: 'registered123', extension: 'mkv', registered: imported} }
   else if (p === '/api/v1/cms/preview') data = {items: [{name: '中文电影.strm', path: '中文电影.strm', status: 'ready', nfo: true, artwork: ['poster.jpg']}, {name: '未识别.strm', path: '未识别.strm', status: 'unrecognized', artwork: []}], next_cursor: 2, has_more: false, directory: '.'}
   else if (p === '/api/v1/cms/import') { assert.equal(r.headers()['x-csrf-token'], 'fixture-csrf'); assert.deepEqual(r.postDataJSON(), {root: '/mnt/cms-media', paths: ['中文电影.strm']}); imported = true; data = {imported: 1, failed: [], files_modified: 0} }
   else if (p === '/api/v1/settings/sync') data = {enabled: false, interval_minutes: 5}
   else if (p === '/api/v1/media') data = {media: [], links: []}
   else if (['/api/v1/transfers', '/api/v1/executions', '/api/v1/deletion-reviews'].includes(p)) data = {items: []}
   await route.fulfill({json: data})
  })
  await page.goto(process.env.BASE_URL || 'http://127.0.0.1:29529')
  if (width < 600) await page.getByRole('button', {name: '导航', exact: true}).click()
  await page.locator('aside nav').getByRole('button', {name: '系统设置', exact: true}).click()
  await page.getByRole('heading', {name: 'CMS 兼容与继承', exact: true}).waitFor()
  await page.getByLabel('CMS 复用 Cookie', {exact: true}).waitFor()
  assert.equal(await page.getByLabel('CMS 复用 Cookie', {exact: true}).getAttribute('type'), 'password')
  assert.equal(await page.getByLabel('CMS 复用 Cookie', {exact: true}).inputValue(), '********')
  assert.equal(await page.getByLabel('旧 CMS 播放源地址', {exact: true}).inputValue(), '', 'null array defaults')
  await page.getByLabel('旧 CMS 播放源地址', {exact: true}).fill('http://cms.example.test:9527')
  await page.getByLabel('继承 STRM 根目录', {exact: true}).fill('/mnt/cms-media')
  await page.getByText('迁移只读模式（重启生效）', {exact: true}).click()
  assert.equal(await page.getByLabel('迁移只读模式（重启生效）', {exact: true}).isChecked(), true)
  await page.getByRole('button', {name: '保存 CMS 配置', exact: true}).click()
  await page.getByText('配置已保存；迁移只读模式将在重启服务后生效', {exact: true}).waitFor()
  assert.deepEqual(posted.origins, ['http://cms.example.test:9527']); assert.deepEqual(posted.strm_roots, ['/mnt/cms-media'])
  await page.getByLabel('单个旧 STRM 内容', {exact: true}).fill('http://cms.example.test:9527/d/registered123.mkv?/中文电影.mkv')
  await page.getByRole('button', {name: '解析预览', exact: true}).click()
  await page.getByText(/pickcode：registered123/).waitFor()
  const cms = page.locator('.cms-settings')
  await cms.locator('select').first().selectOption('/mnt/cms-media')
  await page.getByRole('button', {name: '读取本级目录', exact: true}).click()
  await page.getByLabel('继承 中文电影.strm', {exact: true}).check()
  await page.getByRole('button', {name: '继承所选 1 个文件', exact: true}).click()
  await page.getByText(/已继承 1 条记录，失败 0 条/).waitFor()
  assert.equal(await page.getByLabel('继承 未识别.strm', {exact: true}).count(), 0)
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'mobile horizontal overflow')
  await page.getByRole('heading', {name: 'CMS 兼容与继承', exact: true}).scrollIntoViewIfNeeded()
  await page.screenshot({path: output + '/cms-' + width + '.png', fullPage: true})
  assert.deepEqual(errors, []); await page.close()
 }
 console.log('CMS settings desktop/mobile: null arrays, masked credentials, save, restart state, parse, preview and selected inheritance passed')
} finally { await browser.close() }
