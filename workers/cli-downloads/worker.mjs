// The route runs before the existing R2 custom domain; fetch preserves its CDN.
export function archive(path) {
  const match = /^\/cli\/releases\/(20\d{2}\.\d{1,2}\.\d{1,2}(?:-beta\.\d+)?)\/doomer_\1_(darwin|linux)_(amd64|arm64)\.tar\.gz$/.exec(path);
  return match && { version: match[1], os: match[2], arch: match[3] };
}

export async function handle(request, env, origin = fetch) {
  const response = await origin(request);
  const item = archive(new URL(request.url).pathname);
  if (!item || request.method !== 'GET' || ![200, 206].includes(response.status)
      || request.headers.get('X-Doomer-Release-Verification') === '1') return response;
  const kind = response.status === 206 || request.headers.has('Range') ? 'range' : 'download';
  try {
    env.DOWNLOADS.writeDataPoint({
      indexes: [item.version],
      blobs: [item.version, item.os, item.arch, kind],
      doubles: [1],
    });
  } catch {
    // Analytics must never stop an otherwise successful download.
    console.error('CLI download analytics unavailable');
  }
  return response;
}

export default { fetch(request, env) { return handle(request, env); } };
