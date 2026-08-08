export function nextScheduledUTCMinute(now = Date.now()) {
  const minimum = now + 4 * 60_000;
  return new Date(Math.ceil(minimum / 60_000) * 60_000);
}
