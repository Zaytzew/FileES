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
