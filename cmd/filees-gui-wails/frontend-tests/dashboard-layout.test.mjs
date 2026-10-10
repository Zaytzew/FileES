import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {runInNewContext} from 'node:vm';
const read=name=>readFileSync(new URL('../frontend/'+name,import.meta.url),'utf8');
test('dashboard owns stationary header, notification dock and toast row',()=>{
 const html=read('index.html');
 const dock=html.indexOf('id="notification-dock"'),cards=html.indexOf('id="dashboard-content"');
 for(const id of ['sync-pause-status','memory-safety','announcement-banner','intent-alerts','lock-release-banner','action-status']) {
  const position=html.indexOf(`id="${id}"`);
  assert.ok(position>dock&&position<cards,id);
  assert.equal(html.split(`id="${id}"`).length,2,'unique '+id);
 }
 assert.ok(html.indexOf('id="toasts"')<html.indexOf('</main>'));
 assert.match(read('app.css'),/grid-template-rows:auto minmax\(0,1fr\) auto/);
 assert.match(read('app.css'),/\.dashboard-content \{[^}]*min-height:0[^}]*overflow-y:auto/);
 assert.match(read('app.css'),/\.dashboard-toasts:empty \{ display:none/);
});
test('background progress is placed in the same stationary notification dock',()=>{
 const js=read('progress-overlay.js');
 assert.match(js,/getElementById\("notification-dock"\)/);
 assert.match(js,/if \(dock\) dock.appendChild\(strip\)/);
 assert.doesNotMatch(js,/hero.nextSibling/);
});
test('actual background progress creates only one strip in the stationary dock',()=>{
 const source=read('progress-overlay.js').replace(/^import .*;\r?\n/gm,'').replace('export function initializeProgressOverlay','function initializeProgressOverlay');
 const children=[];
 const sandbox={document:{
  getElementById:id=>id==='notification-dock'?{appendChild:node=>children.push(node)}:null,
  createElement:()=>({addEventListener(){}}),
  body:{prepend:()=>assert.fail('fell back outside the dock')},
 }};
 runInNewContext(source+'\nglobalThis.createStrip=ensureStrip;',sandbox);
 assert.equal(sandbox.createStrip(),sandbox.createStrip());
 assert.equal(children.length,1);
 assert.equal(children[0].className,'progress-parking');
});
test('actual toast renderer keeps the latest result visible without changing expiry',()=>{
 const source=read('app.js');
 const start=source.indexOf('function showToast('),end=source.indexOf('function deletedCopyRepo(',start);
 const children=[],timers=[];
 const root={scrollTop:0,get scrollHeight(){return children.length*100},appendChild:node=>children.push(node)};
 const sandbox={$:id=>{assert.equal(id,'#toasts');return root},
  document:{createElement:()=>({classList:{add(){}},remove(){}})},
  escapeHTML:text=>String(text).replaceAll('<','&lt;'),
  window:{setTimeout:(fn,delay)=>timers.push(delay)},
 };
 runInNewContext(source.slice(start,end)+'\nglobalThis.notify=showToast;',sandbox);
 sandbox.notify({title:'done',message:'first'});
 sandbox.notify({level:'critical',title:'<literal>',message:'second'});
 assert.equal(children.length,2);
 assert.equal(root.scrollTop,root.scrollHeight);
 assert.deepEqual(timers,[4800,8000]);
 assert.ok(children[1].innerHTML.includes('&lt;literal>'));
});
