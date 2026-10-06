// Secrets arrive through stdin; never print tokens, passwords or signed URLs.
import fs from 'node:fs';
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
const input = JSON.parse(fs.readFileSync(0, 'utf8'));
const root = process.env.CMS_ACCEPTANCE_DIR || '/var/tmp/115-direct-cms-acceptance';
const legacyMode = process.env.CMS_BROWSER_MODE === 'legacy';
const browser = await chromium.launch({headless: true, executablePath: process.env.CHROME_PATH || undefined, args: ['--no-sandbox', '--autoplay-policy=no-user-gesture-required']});
const context = await browser.newContext({viewport: {width: 1440, height: 900}});
let oldCMSRequests = 0;
const network = [];
await context.route('**/*', async route => {
  const u = new URL(route.request().url());
  if ((u.hostname === 'cms.example.test' || u.hostname === '192.0.2.7') && u.port === '9527') {
    oldCMSRequests++;
    await route.abort();
    return;
  }
  await route.continue();
});
context.on('response', async response => {
  const u = new URL(response.url());
  if (u.pathname.startsWith('/cms/play/') || (legacyMode && u.pathname.startsWith('/d/')) || !['127.0.0.1','cms.example.test'].includes(u.hostname)) {
    const headers = await response.allHeaders();
    network.push({host: u.hostname, status: response.status(), category: u.pathname.startsWith('/cms/play/') || (legacyMode && u.pathname.startsWith('/d/')) ? 'gateway' : 'external', mode: headers['x-cms-link-mode'] || null, range: response.request().headers()['range'] || null});
  }
});
const page = await context.newPage();
try {
  await page.goto(legacyMode ? 'http://127.0.0.1:29527/healthz' : 'http://127.0.0.1:29096/web/index.html', {waitUntil:'domcontentloaded', timeout:30000});
  await page.waitForTimeout(5000);
  const inputs = await page.locator('input').evaluateAll(items => items.map(i => ({id:i.id,type:i.type,placeholder:i.placeholder})));
  console.log(JSON.stringify({stage:'web-loaded', title:await page.title(), inputs, buttons:await page.getByRole('button').allTextContents()}));
  await page.screenshot({path:root+'/web-initial.png'});
  if (process.env.CMS_BROWSER_MODE === 'inspect') {
    await browser.close();
    process.exit(0);
  }
  if (!legacyMode && await page.locator('#txtManualName, #txtUsername').count()) {
    await page.locator('#txtManualName, #txtUsername').first().fill(input.username);
    await page.locator('#txtManualPassword, #txtPassword').first().fill(input.password);
    const response = page.waitForResponse(r=>new URL(r.url()).pathname.toLowerCase()==='/users/authenticatebyname',{timeout:45000});
    await page.locator('button[type="submit"]').first().click();
    const login = await response;
    console.log(JSON.stringify({stage:'web-login-response',status:login.status()}));
    if(login.status()!==200) throw new Error('Jellyfin web authentication failed');
    await page.locator('#txtManualPassword:visible, #txtPassword:visible').waitFor({state:'hidden',timeout:20000});
  }
  const loginRemaining = await page.locator('#txtManualPassword:visible, #txtPassword:visible').count();
  console.log(JSON.stringify({stage:'web-login', loginFormRemaining:loginRemaining}));
  if(loginRemaining) throw new Error('Jellyfin web login did not complete');
  await page.screenshot({path:root+'/web-login.png'});
  if (process.env.CMS_BROWSER_MODE === 'native') {
    const sample = input.samples.find(s=>s.kind==='Episode');
    const info = await context.request.get('http://127.0.0.1:29096/System/Info/Public');
    const server = await info.json();
    await page.goto(`http://127.0.0.1:29096/web/index.html#!/details?id=${sample.item_id}&serverId=${server.Id}`,{waitUntil:'domcontentloaded'});
    await page.waitForTimeout(5000);
    console.log(JSON.stringify({stage:'native-details',buttons:await page.locator('button').evaluateAll(items=>items.filter(x=>x.offsetParent!==null).map(x=>({title:x.title,label:x.getAttribute('aria-label'),text:x.textContent.trim().slice(0,60)})))}));
    await page.screenshot({path:root+'/native-details.png'});
    const play = page.locator('button[title="Play"], button[title="播放"], button.btnPlay').filter({visible:true}).first();
    if(await play.count()) {
      await play.click();
      await page.waitForTimeout(12000);
      const state = await page.locator('video').evaluateAll(items=>items.map(v=>({time:v.currentTime,width:v.videoWidth,height:v.videoHeight,paused:v.paused,error:v.error?{code:v.error.code,message:v.error.message}:null})));
      console.log(JSON.stringify({stage:'native-playback',state}));
      await page.screenshot({path:root+'/native-playback.png'});
      fs.writeFileSync(root+'/native.json',JSON.stringify({state,oldCMSRequests,network},null,2),{mode:0o600});
      if (oldCMSRequests !== 0 || !state.some(v=>v.width>0 && v.time>1 && !v.error)) throw new Error('Native Jellyfin playback acceptance did not pass');
      if(state.length && state[0].width>0 && state[0].time>1) {
        await page.locator('video').first().evaluate(v=>v.pause());
      }
    } else throw new Error('Native Jellyfin play button not found');
    await browser.close();process.exit(0);
  }
  const results = [];
  for (const sample of input.samples) {
    const result = await page.evaluate(async ({sample, token, uid, legacyMode}) => {
      const headers = {'X-Emby-Token':token};
      let source;
      if (legacyMode) {
        const old = new URL(sample.source.Path);
        source = {Path:'http://127.0.0.1:29528'+old.pathname+old.search};
      } else {
        const response = await fetch(`/Items/${sample.item_id}/PlaybackInfo?UserId=${uid}`, {headers});
        const payload = await response.json();
        source = payload.MediaSources.find(s => s.Id === sample.source_id);
        if (!source || !source.Path.startsWith('http://127.0.0.1:29096/cms/play/')) return {rewritten:false};
      }
      const video = document.createElement('video');
      video.controls=true;video.muted=true;video.style.cssText='position:fixed;inset:40px;width:calc(100% - 80px);height:calc(100% - 80px);z-index:999999;background:black';
      document.body.appendChild(video);
      video.src=source.Path;
      const out={rewritten:!legacyMode, legacy:legacyMode, browserCanPlay:video.canPlayType('video/x-matroska; codecs="avc1.640028, mp4a.40.2"')};
      try {
        await Promise.race([video.play(),new Promise((_,reject)=>setTimeout(()=>reject(new Error('play timeout')),30000))]);
        await new Promise(resolve=>setTimeout(resolve,8000));
        out.started=video.currentTime>1;
        out.currentTime=video.currentTime;out.width=video.videoWidth;out.height=video.videoHeight;out.duration=video.duration;
        video.pause();const pausedAt=video.currentTime;
        await new Promise(resolve=>setTimeout(resolve,500));
        out.paused=video.paused && Math.abs(video.currentTime-pausedAt)<0.2;
        await video.play();
        await new Promise(resolve=>setTimeout(resolve,2000));
        out.resumed=video.currentTime>pausedAt+0.5;
        const target=Math.min(120,video.duration/2);
        const seeked=new Promise((resolve,reject)=>{video.addEventListener('seeked',resolve,{once:true});setTimeout(()=>reject(new Error('seek timeout')),15000)});
        video.currentTime=target;await seeked;
        await new Promise(resolve=>setTimeout(resolve,4000));
        out.seeked=video.currentTime>=target;out.seekTime=video.currentTime;
      } catch(error) {out.error=String(error.message).replace(/https?:\/\/\S+/g,'<url>');out.mediaError=video.error ? {code:video.error.code,message:video.error.message.replace(/https?:\/\/\S+/g,'<url>')} : null;}
      video.pause();
      return out;
    }, {sample,token:input.token,uid:input.user_id,legacyMode});
    await page.screenshot({path:root+`/${legacyMode?'legacy':'gateway'}-${sample.kind.toLowerCase()}-playback.png`});
    await page.locator('video').evaluateAll(items=>items.forEach(v=>{v.removeAttribute('src');v.load();v.remove()}));
    result.name=sample.name;result.kind=sample.kind;
    results.push(result);console.log(JSON.stringify(result));
  }
  fs.writeFileSync(root+`/${legacyMode?'browser-legacy':'browser'}.json`,JSON.stringify({results,oldCMSRequests,network},null,2),{mode:0o600});
  if (oldCMSRequests !== 0 || results.some(r=>!r.started || !r.paused || !r.resumed || !r.seeked || r.width === 0)) throw new Error('Browser playback acceptance did not pass');
} finally {await browser.close();}
