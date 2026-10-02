import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

test('every server health colour pulses and reduced motion remains respected', () => {
  const css = readFileSync(new URL('../frontend/app.css', import.meta.url), 'utf8');
  assert.match(css, /\.server-mark\s*\{[^}]*animation:server-beacon/);
  for (const state of ['current', 'refreshing', 'unverified', 'unavailable']) {
    assert.match(css, new RegExp(`\\.server-mark\\.health-${state}`));
  }
  assert.match(css, /@keyframes server-beacon\s*\{[^}]*opacity:/);
  assert.match(css, /@media\s*\(prefers-reduced-motion:reduce\)/);
});
