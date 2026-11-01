const test = require('node:test');
const assert = require('node:assert/strict');
const { externalRequestAction } = require('./workspace_browser_network.cjs');

function request(url, { method = 'GET', headers = {}, resourceType = 'script' } = {}) {
  return { url: () => url, method: () => method, resourceType: () => resourceType,
    // Playwright's short header view intentionally omits cookies.
    headers: () => Object.fromEntries(Object.entries(headers).filter(([name]) => name !== 'cookie')),
    allHeaders: async () => headers };
}

test('password acceptance stubs only the exact optional Google SDK script', async () => {
  assert.equal(await externalRequestAction(request('https://accounts.google.com/gsi/client')), 'stub-google');
  for (const url of ['https://accounts.google.com/gsi/client?secret=value', 'https://accounts.google.com/o/oauth2/auth', 'http://accounts.google.com/gsi/client', 'https://accounts.google.com.attacker.test/gsi/client']) {
    assert.equal(await externalRequestAction(request(url)), 'block');
  }
  for (const options of [{ method: 'POST' }, { resourceType: 'document' }, { headers: { authorization: 'Bearer fixture' } }, { headers: { cookie: 'fixture=value' } }]) {
    assert.equal(await externalRequestAction(request('https://accounts.google.com/gsi/client', options)), 'block');
  }
});

test('font assets require all headers to be credential-free', async () => {
  for (const url of ['https://fonts.googleapis.com/css2?family=Space+Grotesk', 'https://fonts.gstatic.com/s/font.woff2']) {
    assert.equal(await externalRequestAction(request(url)), 'continue');
    for (const options of [{ method: 'POST' }, { headers: { cookie: 'fixture=value' } }, { headers: { authorization: 'Bearer fixture' } }]) {
      assert.equal(await externalRequestAction(request(url, options)), 'block');
    }
  }
  assert.equal(await externalRequestAction(request('https://attacker.test/font.woff2')), 'block');
});
