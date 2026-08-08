export function nextScheduledUTCMinute(now = Date.now(), aheadMinutes = 4) {
  const minimum = now + Math.max(1, Number(aheadMinutes)) * 60_000;
  return new Date(Math.ceil(minimum / 60_000) * 60_000);
}
