import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {localAllocationMarkup} from '../frontend/local-allocation.js';
const helpers={t:(key,args={})=>key+JSON.stringify(args),bytes:n=>`${n} B`,escapeHTML:s=>String(s).replaceAll('<','&lt;')};
test('local allocation distinguishes waiting, known zero, stale and partial without a fake zero',()=>{
 assert.equal(localAllocationMarkup(null,helpers),'');
 assert.match(localAllocationMarkup({state:'waiting',known:false},helpers),/—/);
 assert.match(localAllocationMarkup({state:'ready',known:true,bytes:0},helpers),/≈ 0 B/);
 assert.match(localAllocationMarkup({state:'partial',known:true,bytes:10},helpers),/is-partial/);
 assert.match(localAllocationMarkup({state:'stale',known:true,bytes:10},helpers),/is-stale/);
 assert.match(localAllocationMarkup({state:'ready',known:true,bytes:-1},helpers),/—/);
});
test('local allocation stays next to server storage outside collapsed content',()=>{
 const app=readFileSync(new URL('../frontend/app.js',import.meta.url),'utf8');
 assert.match(app,/storageCapacityMarkup\(server.storage_capacity[^\n]+\n\s*\$\{localAllocationMarkup\(server.local_allocation/);
});
test('local allocation keeps a normal border and contrast while details preserve uncertainty',()=>{
 const css=readFileSync(new URL('../frontend/app.css',import.meta.url),'utf8');
 assert.match(css,/\.local-allocation,\.local-allocation\.is-stale\s*\{\s*border-style:solid;\s*opacity:1;/);
 assert.doesNotMatch(css,/\.local-allocation[^{}]*\{[^}]*border-style:dashed/);
 for(const state of ['partial','stale','waiting']) {
  const markup=localAllocationMarkup({state,known:true,bytes:10},helpers);
  assert.match(markup,new RegExp(`title="[^"]*localAllocation\\.${state}`));
 }
});
