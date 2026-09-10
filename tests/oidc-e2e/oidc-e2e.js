// End-to-end OIDC login through a real Keycloak, driven in a real browser.
const { chromium } = require('playwright');
const fs = require('fs');
const [,, base, outDir] = process.argv;
fs.mkdirSync(outDir, { recursive: true });
let failures = 0;
const meta = (l) => { try { return typeof l.metadata === 'string' ? JSON.parse(l.metadata) : (l.metadata || {}); } catch { return {}; } };
const check = (ok, msg) => { console.log((ok ? 'PASS ' : 'FAIL ') + msg); if (!ok) failures++; };

async function loginVia(browser, user, pass, name) {
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  await page.goto(`${base}/login`);
  await page.waitForSelector('#username');
  const btn = page.getByRole('button', { name: /sign in with keycloak/i });
  check(await btn.count() === 1, `${name}: "Sign in with Keycloak" button rendered from /api/auth/sso/config`);
  await page.screenshot({ path: `${outDir}/${name}-01-login.png` });
  await btn.click();
  // Keycloak login form (wait for the cross-site navigation, not for a field bkt's own page also has)
  await page.waitForURL(u => u.hostname === 'oidc-e2e-keycloak', { timeout: 20000 });
  await page.waitForSelector('#kc-form-login', { timeout: 20000 });
  const kcURL = page.url();
  check(kcURL.includes('/realms/bkt/protocol/openid-connect/auth'), `${name}: redirected to Keycloak authorize endpoint`);
  check(/code_challenge=[A-Za-z0-9_-]{43}/.test(kcURL) && kcURL.includes('code_challenge_method=S256'), `${name}: authorize request carries PKCE S256 challenge`);
  check(kcURL.includes('nonce=') && kcURL.includes('state=') && kcURL.includes('scope=openid'), `${name}: state, nonce and openid scope present`);
  await page.screenshot({ path: `${outDir}/${name}-02-keycloak.png` });
  await page.fill('#username', user);
  await page.fill('#password', pass);
  await page.click('#kc-login');
  // Back at bkt: either the app (success) or the callback page showing an error.
  await page.waitForURL(u => u.origin === base, { timeout: 20000 }).catch(() => {});
  await page.waitForFunction(() => !!localStorage.getItem('token') || !!document.querySelector('.alert-error'), null, { timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${outDir}/${name}-03-after.png` });
  const url = page.url();
  const token = await page.evaluate(() => localStorage.getItem('token'));
  const bodyText = await page.locator('body').innerText();
  return { ctx, page, url, token, bodyText };
}

(async () => {
  const browser = await chromium.launch();

  // alice: member of bkt-admins and bkt-users → admin
  {
    const r = await loginVia(browser, 'alice', 'alice-pass-1', 'alice');
    check(!!r.token, 'alice: landed back in bkt with a session token');
    const me = await r.ctx.request.get(`${base}/api/users/me`, { headers: { Authorization: `Bearer ${r.token}` } });
    const j = me.ok() ? await me.json() : {};
    check(me.ok() && j.username === 'alice', `alice: /api/users/me username=${j.username}`);
    check(j.is_admin === true, 'alice: is_admin=true from bkt-admins group');
    check(j.sso_provider === 'oidc' && !!j.sso_id, `alice: sso_provider=${j.sso_provider} sso_id set`);
    check(j.email === 'alice@example.com', `alice: email=${j.email}`);
    // Admin can read the audit log: our login must be there with provider metadata
    const audit = await r.ctx.request.get(`${base}/api/audit?limit=20`, { headers: { Authorization: `Bearer ${r.token}` } });
    const logs = audit.ok() ? (await audit.json()).logs || [] : [];
    const mine = logs.find(l => l.action === 'auth.login' && l.username === 'alice' && l.status === 'success');
    check(!!mine && meta(mine).provider === 'oidc' && (meta(mine).groups || []).includes('bkt-admins'), 'alice: audit log records auth.login with provider=oidc and groups');
    // Second login must reuse the same account (matched by subject), not create alice1
    const r2 = await loginVia(browser, 'alice', 'alice-pass-1', 'alice-again');
    const me2 = await r2.ctx.request.get(`${base}/api/users/me`, { headers: { Authorization: `Bearer ${r2.token}` } });
    const j2 = me2.ok() ? await me2.json() : {};
    check(j2.id === j.id && j2.username === 'alice', 'alice: second login maps to the same account (no duplicate)');
    await r.ctx.close(); await r2.ctx.close();
  }

  // bob: bkt-users only → regular user
  {
    const r = await loginVia(browser, 'bob', 'bob-pass-1', 'bob');
    check(!!r.token, 'bob: landed back in bkt with a session token');
    const me = await r.ctx.request.get(`${base}/api/users/me`, { headers: { Authorization: `Bearer ${r.token}` } });
    const j = me.ok() ? await me.json() : {};
    check(j.username === 'bob' && j.is_admin === false, `bob: regular user (is_admin=${j.is_admin})`);
    const audit = await r.ctx.request.get(`${base}/api/audit?limit=5`, { headers: { Authorization: `Bearer ${r.token}` } });
    check(audit.status() === 403 || audit.status() === 401, `bob: cannot read audit log (status ${audit.status()})`);
    await r.ctx.close();
  }

  // carol: in neither group → denied with a specific message
  {
    const r = await loginVia(browser, 'carol', 'carol-pass-1', 'carol');
    check(!r.token, 'carol: no session token issued');
    check(/not in a group that grants access/i.test(r.bodyText), 'carol: denial message explains group requirement');
    check(r.url.includes('/auth/oidc/callback'), 'carol: error stays on the callback page (no auto-redirect) with a back button');
    // the denial must be in the audit log too (admin view)
    const a = await loginVia(browser, 'alice', 'alice-pass-1', 'alice-audit');
    const audit = await a.ctx.request.get(`${base}/api/audit?limit=50`, { headers: { Authorization: `Bearer ${a.token}` } });
    const logs = audit.ok() ? (await audit.json()).logs || [] : [];
    const denial = logs.find(l => l.action === 'auth.login' && l.username === 'carol' && l.status === 'failure');
    check(!!denial && /not in allowed group/.test(denial.error_message || '') && (meta(denial).groups || []).includes('unrelated'), 'carol: denial audit-logged with reason and groups');
    await a.ctx.close();
    await r.ctx.close();
  }

  await browser.close();
  console.log(failures ? `E2E FAILED (${failures})` : 'E2E OK');
  process.exit(failures ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
