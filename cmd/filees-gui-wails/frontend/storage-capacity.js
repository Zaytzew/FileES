// A quiet, passive filesystem gauge: no inferred quota or write permission.
export function storageCapacityMarkup(value, {t, bytes, escapeHTML, now = Date.now()}) {
  const total = value?.total_bytes, available = value?.available_bytes;
  const known = ['fresh', 'stale'].includes(value?.state) && Number.isSafeInteger(total) && total > 0 && Number.isSafeInteger(available) && available >= 0 && available <= total;
  const deadline = Date.parse(value?.expires_at || '');
  const fresh = known && value.state === 'fresh' && Number.isFinite(deadline) && deadline > now && deadline - now <= 180000;
  const state = known ? (fresh ? 'fresh' : 'stale') : 'unknown';
  const percent = known ? Math.min(100, Math.max(0, Math.round((1 - available / total) * 100))) : null;
  const severity = fresh ? (percent >= 95 ? 'critical' : percent >= 85 ? 'warning' : 'normal') : '';
  const label = known ? t('storageCapacity.details', {percent, free:bytes(available), total:bytes(total)}) + (fresh ? '' : ' · ' + t('storageCapacity.stale')) : t('storageCapacity.unknown');
  const text = known ? `${percent}%` : '—';
  return `<span class="server-capacity is-${state} ${severity ? 'is-' + severity : ''}" role="img" aria-label="${escapeHTML(label)}" title="${escapeHTML(label)}">
    <svg viewBox="0 0 20 20" aria-hidden="true"><ellipse cx="10" cy="4" rx="7" ry="2.5"/><path d="M3 4v11c0 3.3 14 3.3 14 0V4M3 9c0 3.3 14 3.3 14 0"/></svg>
    <span class="capacity-track" aria-hidden="true"><i style="width:${percent ?? 0}%"></i></span><span>${text}</span>${state === 'stale' ? '<span class="capacity-stale-mark" aria-hidden="true">◷</span>' : ''}
  </span>`;
}
