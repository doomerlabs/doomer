# CLI download analytics

This Worker runs only on `downloads.doomer.ai/cli/releases/*`, before the existing
R2 custom domain. It forwards the original request and returns the original
response, preserving caching, conditional requests, range support, and headers.
It has no R2 binding or upload credential and cannot modify release objects.

`doomer_cli_downloads` in Cloudflare Workers Analytics Engine contains:

- `blob1`: version (also the sampling index)
- `blob2`: OS
- `blob3`: architecture
- `blob4`: `download` for successful full responses; `range` for partial/range requests
- `double1`: 1

Only matching archive GETs returning 200 or 206 produce events. Manifest,
checksum, HEAD, failed requests, and requests carrying
`X-Doomer-Release-Verification: 1` are excluded. The release publisher sets that
header. It is an exclusion convention, not authentication; a caller can opt out
of counting. Bot requests and retries can still count. These are responses
served, not proof of completed transfers or unique installations.

No IP addresses, cookies, query strings, user agents, or persistent identifiers
are written to the dataset. If analytics fails, the download is still returned.
Cloudflare's normal infrastructure logging is independent of this dataset.

Use `downloads.sql` in Cloudflare's Analytics Engine SQL interface for daily
counts by release and platform. Sum only `kind = 'download'` for the main total;
range requests are reported separately. Weighted totals account for sampling;
Analytics Engine data is retained for its service retention period (currently
three months). Export aggregates if longer history is needed.

## Test and deploy

Run `node --test workers/cli-downloads/worker.test.mjs` from the repository root.
CI includes this in the quality job. Deploy with an authorized Cloudflare account:

```sh
wrangler deploy --config workers/cli-downloads/wrangler.toml
```

The account must be Replicated (`c2592895222dce604580569f98ef7094`), which owns
`doomer.ai`. Deployment grants only the analytics binding; the Worker fetches the
existing public origin. Do not attach it as a custom domain or change DNS.
To roll back, remove its specific route or deploy the previous Worker version.

For a post-deploy smoke test, compare an archive's SHA-256 with the published
manifest using the verification header. A normal GET should then produce one
`download` event; HEAD, a manifest read, and a verification GET should not.
