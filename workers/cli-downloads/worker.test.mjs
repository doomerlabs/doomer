import { test } from 'node:test';
import assert from 'node:assert/strict';
import worker, { archive, handle } from './worker.mjs';
const path = '/cli/releases/2026.11.2/doomer_2026.11.2_darwin_arm64.tar.gz';

async function run({ url = path, method = 'GET', headers = {}, status = 200, fail = false } = {}) {
  const events = [];
  const request = new Request('https://downloads.doomer.ai' + url, { method, headers });
  const expected = new Response(method === 'HEAD' || status === 304 ? null : 'archive bytes', { status, headers: { etag: 'test', 'cache-control': 'public, max-age=31536000' } });
  const result = await handle(request, { DOWNLOADS: { writeDataPoint(event) { if (fail) throw Error(); events.push(event); } } }, async req => { assert.equal(req, request); return expected; });
  assert.equal(result, expected);
  return events;
}

test('counts full downloads by version and platform, without request identity', async () => {
  assert.deepEqual(await run(), [{ indexes: ['2026.11.2'], blobs: ['2026.11.2', 'darwin', 'arm64', 'download'], doubles: [1] }]);
  assert.deepEqual(archive('/cli/releases/2026.11.2-beta.1/doomer_2026.11.2-beta.1_linux_amd64.tar.gz'), { version: '2026.11.2-beta.1', os: 'linux', arch: 'amd64' });
});
test('excludes metadata, Mac assets, mismatched versions, failed requests and verification', async () => {
  for (const url of ['/cli/stable.json', '/macos/stable.json', '/cli/releases/2026.11.2/checksums.txt', path.replace('doomer_2026.11.2', 'doomer_2026.11.1')]) assert.deepEqual(await run({ url }), []);
  for (const status of [304, 404, 500]) {
    assert.deepEqual(await run({ status }), []);
  }
  assert.deepEqual(await run({ method: 'HEAD' }), []);
  assert.deepEqual(await run({ headers: { 'X-Doomer-Release-Verification': '1' } }), []);
});
test('counts managed installer downloads by release without counting publisher checks or ranges', async () => {
  const version = '2026.11.22.1-beta.2';
  const url = `/cli/releases/${version}/Doomer-MDM-${version}.pkg`;
  assert.deepEqual(await run({ url }), [{ indexes: [version], blobs: [version, 'darwin', 'universal', 'download'], doubles: [1] }]);
  assert.deepEqual(await run({ url, headers: { 'X-Doomer-Release-Verification': '1' } }), []);
  assert.equal((await run({ url, status: 206 }))[0].blobs[3], 'range');
  assert.deepEqual(await run({ url: url.replace(`MDM-${version}`, 'MDM-2026.11.22.1') }), []);
  assert.deepEqual(await run({ url: `/cli/releases/${version}/mdm-release.json` }), []);
  const bootstrap = '/cli/releases/2026.11.22.1/Doomer-MDM-Bootstrap-2026.11.22.1.pkg';
  assert.equal((await run({ url: bootstrap }))[0].blobs[3], 'download');
});

test('accepts stable daily sequence archives', () => {
  assert.deepEqual(archive('/cli/releases/2026.11.2.1/doomer_2026.11.2.1_linux_arm64.tar.gz'), { version: '2026.11.2.1', os: 'linux', arch: 'arm64' });
});

test('tracks partial transfers separately and never counts them as full downloads', async () => {
  for (const options of [{ status: 206 }, { headers: { Range: 'bytes=0-1' } }]) assert.equal((await run(options))[0].blobs[3], 'range');
});
test('analytics failure preserves the original response', async () => { await run({ fail: true }); });

test('Cloudflare entry point accepts execution context without treating it as origin', async t => {
  const response = new Response('archive');
  t.mock.method(globalThis, 'fetch', async () => response);
  let count = 0;
  assert.equal(await worker.fetch(new Request('https://downloads.doomer.ai' + path), { DOWNLOADS: { writeDataPoint() { count++; } } }, { waitUntil() {} }), response);
  assert.equal(count, 1);
});
