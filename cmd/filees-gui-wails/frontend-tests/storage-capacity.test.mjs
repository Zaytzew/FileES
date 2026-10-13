import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {storageCapacityMarkup} from '../frontend/storage-capacity.js';
const now=Date.parse('2026-10-13T20:00:00Z');
const env={now,t:(key,args)=>key+JSON.stringify(args||{}),bytes:String,escapeHTML:value=>String(value).replaceAll('<','&lt;').replaceAll('"','&quot;')};
const sample={state:'fresh',total_bytes:1000,available_bytes:200,expires_at:new Date(now+60000).toISOString()};
test('server gauge distinguishes fresh, stale, full and unknown without inventing zero',()=>{
 assert.match(storageCapacityMarkup(sample,env),/is-fresh is-normal/);
 assert.match(storageCapacityMarkup(sample,env),/80%/);
 assert.match(storageCapacityMarkup({...sample,available_bytes:0},env),/is-critical/);
 assert.match(storageCapacityMarkup({...sample,expires_at:new Date(now-1).toISOString()},env),/is-stale/);
 assert.match(storageCapacityMarkup({...sample,state:'stale'},env),/is-stale/);
 for(const value of [null,{}, {...sample,total_bytes:0},{...sample,available_bytes:1001},{...sample,total_bytes:Infinity}]) {
  const html=storageCapacityMarkup(value,env); assert.match(html,/is-unknown/);assert.doesNotMatch(html,/>0%</);
 }
});
test('gauge remains in the title outside the collapsed server folders',()=>{
 const app=readFileSync(new URL('../frontend/app.js',import.meta.url),'utf8');
 const gauge=app.indexOf('storageCapacityMarkup(server.storage_capacity');
 assert.ok(gauge>app.indexOf('<header class="server-header"'));
 assert.ok(gauge<app.indexOf('<div id="server-folders-'));
});
