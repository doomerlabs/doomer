-- Counts successful full-archive responses, not unique installations.
-- Weighted sums preserve estimated totals if Analytics Engine samples events.
SELECT
  toStartOfDay(timestamp) AS day,
  blob1 AS version,
  blob2 AS os,
  blob3 AS architecture,
  blob4 AS kind,
  SUM(_sample_interval) AS requests
FROM doomer_cli_downloads
WHERE timestamp > NOW() - INTERVAL '30' DAY
GROUP BY day, version, os, architecture, kind
ORDER BY day DESC, version, os, architecture, kind
