import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const base = process.env.BASE_URL || 'http://127.0.0.1:29528'
const output = process.env.SCREENSHOT_DIR || '/tmp/115-direct-media-organize-screenshots'
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_EXECUTABLE_PATH || undefined, args: ['--no-sandbox'] })
try {
  for (const width of [1440, 390]) {
    const page = await browser.newPage({ viewport: { width, height: 900 } })
    const calls = [], errors = []
    let delayPreview = false, releasePreview
    const detail = { kind: 'tv', id: 247718, title: '黑帮领地', original_title: 'MobLand', year: 2025, date: '2025-03-30', overview: '两大家族的故事，用于人工核对影片。', poster_path: '/poster.jpg', backdrop_path: '/backdrop.jpg', rating: 8.2, genres: ['犯罪', '剧情'], origin_countries: ['GB'], original_language: 'en', runtime: 45, episode_count: 12, seasons: [{ id: 100, season_number: 0, name: '特别篇', episode_count: 1 }, { id: 101, season_number: 1, name: '第 1 季', episode_count: 10 }, { id: 102, season_number: 2, name: '第 2 季', episode_count: 2 }] }
    page.on('pageerror', e => errors.push(e.message))
    const row = { id: 'organize:task:remote', task_id: 'task', file_id: 'play-stable', remote_id: 'remote', name: '[黑帮领地 第一季].MobLand.2025.S01E10.2160p.PMTP.WEB-DL.DoVi.H265.10bit.DDP5.1-UBWEB.strm', source_path: '/pending/电视剧/欧美剧/' + 'source'.repeat(20) + '.strm', output_path: '/output/电视剧/欧美剧/MobLand (2025)/Season 01/MobLand - S01E10.strm', mode: 'hardlink', kind: 'tv', tmdb_id: 247718, version_group: 'tv:247718:1:10-10:', status: 'success', updated_at: new Date().toISOString() }
    await page.route('**/api/v1/**', async route => {
      const req = route.request(), u = new URL(req.url()), body = req.postDataJSON()
      calls.push({ path: u.pathname, query: u.search, body, csrf: req.headers()['x-csrf-token'] })
      let data = {}
      if (u.pathname === '/api/v1/session') data = { authenticated: true, csrf: 'fixture-csrf' }
      else if (u.pathname === '/api/v1/dashboard') data = { stats: { media: 3, jobs: 1, pending: 0, failed: 0 }, cookie_ok: true }
      else if (u.pathname === '/api/v1/transfers') data = { items: [] }
      else if (u.pathname === '/api/v1/115/account') data = { user_id: 1, username: 'Fixture' }
      else if (u.pathname === '/api/v1/records/organization') {
        const state = u.searchParams.get('status')
        const records = [row, { ...row, id: 'job:failed:other', task_id: 'failed', remote_id: 'other', status: 'failed', error: 'Jellyfin 更新通知失败' }, { ...row, id: 'media:unknown', remote_id: 'unknown', tmdb_id: 0, status: 'unrecognized', output_path: '' }]
        data = { items: state ? records.filter(r => r.status === state) : records, total: state ? 1 : 80 }
      } else if (u.pathname.endsWith('/retry')) data = { ok: true, job: { id: 'single-file' } }
      else if (u.pathname === '/api/v1/tmdb/search') data = { items: [detail, { kind: 'tv', id: 247719, title: '另一个条目', year: 2024 }] }
      else if (u.pathname === '/api/v1/tmdb/details') data = u.searchParams.get('kind') === 'movie' ? { id: 100, kind: 'movie', title: '电影示例', year: 2023, overview: '暂无图片的电影' } : detail
      else if (u.pathname === '/api/v1/tmdb/seasons') {
        const number = Number(u.searchParams.get('season'))
        data = { id: 101 + number, season_number: number, name: number === 0 ? '特别篇' : '第 ' + number + ' 季', poster_path: '/season.jpg', episodes: Array.from({ length: number === 1 ? 10 : number === 0 ? 1 : 2 }, (_, index) => ({ id: index + 1, episode_number: index + 1, name: '单集标题 ' + (index + 1), overview: '单集简介 ' + (index + 1), air_date: '2025-06-01', still_path: '/still.jpg', vote_average: 8.5 })) }
      } else if (u.pathname === '/api/v1/tmdb/image') {
        if (u.searchParams.get('path') === '/poster.jpg') return route.fulfill({ status: 502, json: { error: { message: '图片暂不可用' } } })
        return route.fulfill({ contentType: 'image/png', body: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aPrsAAAAASUVORK5CYII=', 'base64') })
      } else if (u.pathname === '/api/v1/organize/preview') {
        if (delayPreview) await new Promise(resolve => { releasePreview = resolve })
        data = { digest: 'fixture-preview', details: detail, items: [{ upload_archive: { relative: '电影/动画电影/fixture.mkv' }, link: { source_path: row.source_path, output_path: row.output_path }, retire: [] }] }
      }
      else if (u.pathname === '/api/v1/organize/execute') data = { id: 'single-file-manual', status: 'queued' }
      await route.fulfill({ json: data })
    })
    await page.goto(base)
    if (width < 600) await page.getByRole('button', { name: '导航', exact: true }).click()
    await page.locator('aside nav').getByRole('button', { name: '媒体整理', exact: true }).click()
    await page.getByText('本地媒体整理记录', { exact: true }).waitFor()
    await page.locator('.record-details').first().locator('summary').click()
    await page.getByText(row.source_path, { exact: true }).first().waitFor()
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'records overflow')
    await page.getByRole('button', { name: '重新整理', exact: true }).first().click()
    await page.getByRole('status').filter({ hasText: '重新整理已提交' }).waitFor()
    assert(calls.some(c => c.path.includes('/records/organization/organize%3Atask%3Aremote/retry') && c.csrf === 'fixture-csrf'))
    await page.getByRole('button', { name: '下一页', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('.record-pagination')?.textContent.includes('第 2 /'))
    assert(calls.some(c => c.query.includes('offset=25')))
    await page.getByLabel('媒体整理状态').selectOption('failed')
    await page.getByText('Jellyfin 更新通知失败', { exact: true }).waitFor()
    await page.getByLabel('媒体整理状态').selectOption('unrecognized')
    await page.getByRole('button', { name: '手动识别', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: '单文件媒体整理' })
    await dialog.getByLabel('关键词', { exact: true }).fill('MobLand')
    await dialog.getByRole('button', { name: '查询条目', exact: true }).click()
    await dialog.getByRole('button', { name: '黑帮领地 · 2025 · #247718', exact: true }).click()
    await dialog.getByRole('heading', { name: '黑帮领地 (2025)', exact: true }).waitFor()
    await dialog.getByText('原名：MobLand', { exact: true }).waitFor()
    await dialog.getByText('两大家族的故事，用于人工核对影片。', { exact: true }).last().waitFor()
    await dialog.getByText('评分 8.2', { exact: true }).waitFor()
    await dialog.locator('.metadata-card').getByText('图片加载失败', { exact: true }).waitFor()
    assert.equal(await dialog.getByLabel('季号', { exact: true }).inputValue(), '1', 'source season parsed')
    assert.equal(await dialog.getByLabel('集号', { exact: true }).inputValue(), '10', 'source episode parsed')
    await dialog.getByLabel('选择集', { exact: true }).selectOption('10')
    await dialog.getByText('第 10 集 · 单集标题 10', { exact: true }).waitFor()
    assert((await dialog.getByAltText('单集标题 10剧照').getAttribute('src')).includes('/api/v1/tmdb/image?'))
    await dialog.locator('.backdrop-preview summary').click()
    await dialog.getByAltText('黑帮领地背景图').waitFor()
    await dialog.getByLabel('选择季', { exact: true }).selectOption('2')
    assert.equal(await dialog.getByLabel('集号', { exact: true }).inputValue(), '', 'changing season clears episode')
    await dialog.getByLabel('选择集', { exact: true }).selectOption('2')
    await dialog.getByText('第 2 集 · 单集标题 2', { exact: true }).waitFor()
    await dialog.getByLabel('集号', { exact: true }).fill('1')
    await dialog.getByText('第 1 集 · 单集标题 1', { exact: true }).waitFor()
    assert.equal(await dialog.getByLabel('选择集', { exact: true }).inputValue(), '1', 'numeric override updates picker')
    await dialog.getByLabel('选择季', { exact: true }).selectOption('0')
    await dialog.getByLabel('选择集', { exact: true }).selectOption('1')
    assert.equal(await dialog.getByLabel('季号', { exact: true }).inputValue(), '0', 'special season retained')
    assert.equal(await dialog.getByLabel('确认特别篇').isChecked(), false, 'special consent stays explicit')
    await dialog.getByLabel('选择季', { exact: true }).selectOption('1')
    await dialog.getByLabel('选择集', { exact: true }).selectOption('10')
    await dialog.getByRole('button', { name: '预览整理', exact: true }).click()
    await dialog.getByRole('button', { name: '确认整理', exact: true }).waitFor()
    await page.waitForFunction(() => ![...document.querySelectorAll('button')].find(b => b.textContent === '确认整理')?.disabled)
    await dialog.getByLabel('选择集', { exact: true }).selectOption('9')
    assert.equal(await dialog.getByRole('button', { name: '确认整理', exact: true }).isDisabled(), true, 'episode edit invalidates plan')
    delayPreview = true
    const pending = page.waitForRequest(r => new URL(r.url()).pathname === '/api/v1/organize/preview')
    await dialog.getByRole('button', { name: '预览整理', exact: true }).click()
    await pending
    await dialog.getByLabel('选择集', { exact: true }).selectOption('10')
    const response = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/organize/preview')
    releasePreview(); await response; delayPreview = false
    await dialog.getByRole('button', { name: '预览整理', exact: true }).waitFor({ state: 'visible' })
    assert.equal(await dialog.getByRole('button', { name: '确认整理', exact: true }).isDisabled(), true, 'stale async preview discarded')
    await dialog.getByRole('button', { name: '预览整理', exact: true }).click()
    await page.waitForFunction(() => ![...document.querySelectorAll('button')].find(b => b.textContent === '确认整理')?.disabled)
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'dialog overflow')
    await page.screenshot({ path: output + '/manual-' + width + '.png', fullPage: true })
    await dialog.getByText('115 整理目录：电影/动画电影/fixture.mkv → ' + row.source_path, { exact: true }).waitFor()
    await dialog.getByRole('button', { name: '确认整理', exact: true }).click()
    await dialog.waitFor({ state: 'hidden' })
    assert(calls.some(c => c.path === '/api/v1/organize/execute' && c.body.ids.length === 1 && c.body.ids[0] === 'unknown' && c.body.digest === 'fixture-preview' && c.body.season === 1 && c.body.episode === 10))
    await page.getByRole('button', { name: '手动识别', exact: true }).click()
    await dialog.getByLabel('媒体类型', { exact: true }).selectOption('movie')
    await dialog.getByLabel('TMDB ID', { exact: true }).fill('100')
    await dialog.getByRole('heading', { name: '电影示例 (2023)', exact: true }).waitFor()
    await dialog.locator('.metadata-card').getByText('暂无图片', { exact: true }).waitFor()
    assert.equal(await dialog.getByLabel('选择季', { exact: true }).count(), 0, 'movie has no TV season picker')
    await dialog.getByRole('button', { name: '关闭媒体整理弹窗', exact: true }).click()
    await page.getByRole('button', { name: '刷新当前页面', exact: true }).click()
    await page.getByText('本地媒体整理记录', { exact: true }).waitFor()
    assert.deepEqual(errors, [])
    await page.screenshot({ path: output + '/records-' + width + '.png', fullPage: true })
    await page.close()
  }
  console.log('Media organization desktop/mobile, metadata/artwork fallback, season/episode/special selection, stale preview rejection, movie details and single-file retry passed')
} finally { await browser.close() }
