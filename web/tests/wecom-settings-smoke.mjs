import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const base = process.env.BASE_URL || 'http://127.0.0.1:29528'
const output = process.env.SCREENSHOT_DIR || '/tmp/115-direct-wecom-settings-screenshots'
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] })
try {
  for (const width of [1440,390]) {
    const page=await browser.newPage({viewport:{width,height:1000}}), errors=[]
    let saved={enabled:false,corp_id:'corp-fixture',agent_id:'1',secret:'********',token:'********',encoding_aes_key:'********',allow_users:['user1'],callback_access_token:'********',callback_base_url:'https://direct.example.com'}, posted, revealCSRF
    page.on('pageerror',e=>errors.push(e.message))
    await page.route('**/api/v1/**',async route=>{
      const r=route.request(),u=new URL(r.url());let data={}
      if(u.pathname==='/api/v1/session')data={authenticated:true,csrf:'fixture-csrf'}
      else if(u.pathname==='/api/v1/dashboard')data={stats:{jobs:0,media:0},cookie_ok:true}
      else if(u.pathname==='/api/v1/transfers')data={items:[]}
      else if(['/api/v1/executions','/api/v1/deletion-reviews'].includes(u.pathname))data={items:[]}
      else if(u.pathname==='/api/v1/media')data={media:[],links:[]}
      else if(u.pathname==='/api/v1/115/account')data={user_id:1,username:'Fixture'}
      else if(u.pathname==='/api/v1/settings/wecom') {
        if(r.method()==='PUT'){posted=r.postDataJSON();saved={...posted,callback_access_token:'********',allow_users:posted.allow_users};assert.equal(r.headers()['x-csrf-token'],'fixture-csrf');data={ok:true}}
        else data=saved
      } else if(u.pathname==='/api/v1/wecom/callback-url') {
        assert.equal(r.method(),'POST');revealCSRF=r.headers()['x-csrf-token'];data={url:'https://direct.example.com/callbacks/wecom?access_token='+posted.callback_access_token}
      } else if(u.pathname==='/api/v1/settings/sync')data={enabled:false,interval_minutes:5}
      await route.fulfill({json:data})
    })
    await page.goto(base)
    if(width<600)await page.getByRole('button',{name:'导航',exact:true}).click()
    await page.locator('aside nav').getByRole('button',{name:'系统设置',exact:true}).click()
    await page.getByLabel('回调入口 Token',{exact:true}).waitFor()
    assert.equal(await page.getByLabel('回调入口 Token',{exact:true}).getAttribute('type'),'password')
    assert.equal(await page.getByLabel('回调入口 Token',{exact:true}).inputValue(),'********')
    assert.equal(await page.getByLabel('重复分享处理',{exact:true}).inputValue(),'updates','missing legacy settings use update detection')
    assert.equal(await page.getByLabel('重复分享检查间隔（分钟）',{exact:true}).inputValue(),'10')
    await page.getByLabel('重复分享处理',{exact:true}).selectOption('skip')
    await page.getByLabel('重复分享检查间隔（分钟）',{exact:true}).fill('30')
    await page.getByRole('button',{name:'随机生成',exact:true}).click()
    const token=await page.getByLabel('回调入口 Token',{exact:true}).inputValue()
    assert.match(token,/^[a-f0-9]{64}$/)
    assert.equal(await page.getByLabel('企业微信签名 Token',{exact:true}).inputValue(),'********','entry token must not replace WeCom signature token')
    await page.getByRole('button',{name:'保存企业微信',exact:true}).click()
    await page.waitForFunction(()=>document.querySelector('input[placeholder="随机生成，或输入32至128位随机密钥"]')?.value==='********')
    assert.equal(posted.callback_access_token,token);assert.deepEqual(posted.allow_users,['user1'])
    assert.equal(posted.share_repeat_policy,'skip');assert.equal(posted.share_check_minutes,30);assert.equal(posted.share_enabled,true)
    await page.getByRole('button',{name:'显示已保存回调地址',exact:true}).click()
    await page.getByLabel('企业微信后台填写的完整回调地址',{exact:true}).waitFor()
    assert.equal(revealCSRF,'fixture-csrf')
    const address=await page.getByLabel('企业微信后台填写的完整回调地址',{exact:true}).inputValue()
    assert.equal(new URL(address).searchParams.get('access_token'),token)
    assert.equal(new URL(address).pathname,'/callbacks/wecom')
    await page.getByLabel('公网管理地址',{exact:true}).fill('https://new.example.com')
    assert.equal(await page.getByLabel('企业微信后台填写的完整回调地址',{exact:true}).count(),0,'edit hides previously revealed URL')
    await page.getByRole('button',{name:'显示已保存回调地址',exact:true}).click()
    await page.getByRole('button',{name:'隐藏回调地址',exact:true}).click()
    assert.equal(await page.getByLabel('企业微信后台填写的完整回调地址',{exact:true}).count(),0)
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'settings overflow')
    assert.equal(await page.locator('.workflow-error').count(),0,'settings displayed an error')
    await page.getByLabel('回调入口 Token',{exact:true}).scrollIntoViewIfNeeded()
    await page.screenshot({path:output+'/settings-'+width+'.png',fullPage:true})
    assert.deepEqual(errors,[])
    await page.close()
  }
  console.log('WeCom settings desktop/mobile: random entry token, independent signature token, masked save, CSRF URL reveal and hiding passed')
}finally{await browser.close()}
