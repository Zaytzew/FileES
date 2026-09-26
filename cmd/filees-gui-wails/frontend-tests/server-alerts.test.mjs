import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {runInNewContext} from 'node:vm';

test('server alerts show origin and unresolved read alerts remain visible',()=>{
 const src=readFileSync(new URL('../frontend/app.js',import.meta.url),'utf8');
 const code=src.slice(src.indexOf('function unreadAnnouncements('),src.indexOf('// renderDetached shows'));
 const nodes=new Map();const get=k=>{if(!nodes.has(k))nodes.set(k,{hidden:false,classList:{toggle(){}},html:''});return nodes.get(k)};
 const context={$:get,t:k=>k,tn:k=>k,escapeHTML:v=>String(v).replaceAll('<','&lt;'),shortDateTime:v=>v,repoIcons:{publish:'!'},replaceHTMLIfChanged:(n,h)=>n.html=h,selectedAnnouncementID:''};runInNewContext(code,context);
 const snapshot={servers:[{id:'s',display_name:'Server A'}],repositories:[],notices:[{id:'active',source:'server',server_id:'s',status:'active',stale:true,acked:true,title:'<script>test',created_at:'now'},...Array.from({length:10},(_,i)=>({id:'old'+i,title:'old',acked:true}))]};
 context.renderShouts(snapshot);const html=get('#shouts').html;
 assert.match(html,/Server A/);assert.match(html,/alert\.stale/);assert.match(html,/alert\.active/);assert.match(html,/&lt;script>/);assert.ok(!html.includes('<script>'));
 snapshot.notices[0].status='resolved';context.renderShouts(snapshot);assert.match(get('#shouts').html,/alert\.resolved/);assert.equal(context.unreadAnnouncements(snapshot).length,0);
});
