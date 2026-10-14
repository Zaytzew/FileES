import {test} from 'node:test';
import assert from 'node:assert/strict';
import {reconcileProgressLine} from '../frontend/import-progress.js';
import {languages, translate} from '../frontend/i18n.js';
test('metadata progress is explicit, translated and not a publication receipt',()=>{
 const catalogues=Object.fromEntries(languages.map(l=>[l.code,l.messages]));
 for(const l of languages){
  const t=(k,a)=>translate(catalogues,l.code,k,a);
  assert.equal(reconcileProgressLine({},t),'');
  assert.ok(reconcileProgressLine({reconcile_progress:{phase:'planning'}},t));
  const line=reconcileProgressLine({reconcile_progress:{phase:'adopting',done:16,total:40}},t);
  assert.ok(line.includes('16') && line.includes('40'));
  assert.doesNotMatch(line,/\[progress|\{done\}|\{total\}/);
  assert.equal(reconcileProgressLine({reconcile_progress:{phase:'adopting',done:41,total:40}},t),'');
 }
});
