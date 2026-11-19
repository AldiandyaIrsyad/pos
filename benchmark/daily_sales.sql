SELECT to_char(created_at AT TIME ZONE 'Asia/Jakarta', 'YYYY-MM-DD') AS day,
       count(*),
       sum(total)::bigint,
       round(avg(total))::bigint
FROM transactions
WHERE outlet_id = 1 AND status = 'PAID'
  AND created_at >= '2026-01-01 00:00:00+07'
  AND created_at <  '2026-04-01 00:00:00+07'
GROUP BY day
ORDER BY day;
