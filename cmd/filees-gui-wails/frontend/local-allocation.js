// Presentation only: the Go GUI worker measures allocation, never the DOM.
export function localAllocationMarkup(value, {t, bytes, escapeHTML}) {
  if (!value) return '';
  const known = value.known === true && Number.isSafeInteger(value.bytes) && value.bytes >= 0;
  const state = ['ready', 'partial', 'stale', 'waiting'].includes(value.state) ? value.state : 'waiting';
  const amount = known ? `≈ ${bytes(value.bytes)}` : '—';
  const label = t('localAllocation.label', {amount});
  const detail = t('localAllocation.details') + ' · ' + t(`localAllocation.${state}`) + (value.measured_at ? ' · ' + value.measured_at : '');
  return `<span class="server-capacity local-allocation is-${state}" title="${escapeHTML(detail)}" aria-label="${escapeHTML(label + '. ' + detail)}"><svg viewBox="0 0 20 20" aria-hidden="true"><rect x="2" y="3" width="16" height="11" rx="2"/><path d="M6 18h8M10 14v4"/></svg><span>${escapeHTML(label)}</span>${state !== 'ready' ? '<span aria-hidden="true">◷</span>' : ''}</span>`;
}
