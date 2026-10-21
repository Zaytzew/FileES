import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {runInNewContext} from 'node:vm';
const read=name=>readFileSync(new URL('../frontend/'+name,import.meta.url),'utf8');

test('tray and panel open the same journal without a snapshot or network request',()=>{
 const source=read('app.js');
 const start=source.indexOf('function openJournal()');
 const end=source.indexOf('document.addEventListener("keydown"',start);
 assert.ok(start>=0&&end>start);
 const nodes=new Map(),listeners=new Map(),events=new Map();
 let focused=0;
 const node=id=>{
  if(!nodes.has(id)) nodes.set(id,{hidden:true,focus(){focused++},
   addEventListener(type,fn){listeners.set(`${id}:${type}`,fn)},
  });
  return nodes.get(id);
 };
 runInNewContext(source.slice(start,end),{$:node,Events:{On:(name,fn)=>events.set(name,fn)}});
 const overlay=node('#journal-overlay');
 events.get('filees:open-journal')();
 assert.equal(overlay.hidden,false); assert.equal(focused,1);
 listeners.get('#close-journal:click')(); assert.equal(overlay.hidden,true);
 listeners.get('#open-journal:click')(); assert.equal(overlay.hidden,false);
 events.get('filees:open-journal')(); assert.equal(focused,3);
 listeners.get('#journal-overlay:click')({target:overlay,currentTarget:overlay});
 assert.equal(overlay.hidden,true);
 assert.equal(events.get('filees:open-journal'),listeners.get('#open-journal:click'));
});

test('collapse actions reach the fixed update dock and sidebar exactly once and survive refresh',()=>{
 const source=read('app.js');
 const toggle=source.indexOf('const toggle = event.target.closest("[data-toggle-card]")');
 const start=source.lastIndexOf('$(',toggle),end=source.indexOf('document.querySelectorAll("[data-toggle-card]")',toggle);
 assert.ok(start>=0&&end>start);
 const nodes=new Map(),listeners=new Map();
 const node=id=>{
  if(!nodes.has(id)) nodes.set(id,{hidden:false,dataset:{},attributes:{},
   addEventListener(type,fn){listeners.set(id,fn)},
   getAttribute(name){return this.attributes[name]},
   setAttribute(name,value){this.attributes[name]=value},
  });
  return nodes.get(id);
 };
 const context={$:node,t:key=>key,updateDownloadLine:()=>'',bytes:String,
  document:{getElementById:id=>node('#'+id)},
 };
 runInNewContext(source.slice(start,end),context);
 const click=(id,ancestry)=>{
  const button=node(id);
  let calls=0;
  for(const ancestor of ancestry) if(listeners.has(ancestor)) {
   calls++;listeners.get(ancestor)({target:{closest:selector=>selector==='[data-toggle-card]'?button:null}});
  }
  assert.equal(calls,1,'click must reach one shared handler');
 };
 const update=node('update-toggle');update.dataset.toggleCard='update-body';update.attributes['aria-expanded']='true';
 click('update-toggle',['#update-card','.dashboard-header','.shell']);
 assert.equal(node('#update-body').hidden,true);
 assert.equal(update.attributes['aria-expanded'],'false');
 assert.equal(update.attributes['aria-label'],'expand.update');
 const renderStart=source.indexOf('function renderUpdate('),renderEnd=source.indexOf('\n}',renderStart)+2;
 runInNewContext(source.slice(renderStart,renderEnd),context);
 context.renderUpdate({update:{state:'available',available_version:'0.2.0'}});
 assert.equal(node('#update-card').hidden,false);
 assert.equal(node('#update-body').hidden,true,'projection refresh must not reopen the body');
 click('update-toggle',['#update-card','.dashboard-header','.shell']);
 assert.equal(node('#update-body').hidden,false);
 assert.equal(update.attributes['aria-label'],'collapse.update');
 const activity=node('activity-toggle');activity.dataset.toggleCard='activity-body';activity.attributes['aria-expanded']='true';
 click('activity-toggle',['.side-column','.shell']);
 assert.equal(node('#activity-body').hidden,true);
 assert.equal(activity.attributes['aria-label'],'expand.activity');
});
test('dashboard owns stationary header, notification dock and toast row',()=>{
 const html=read('index.html');
 const dock=html.indexOf('id="notification-dock"'),cards=html.indexOf('id="dashboard-content"');
 for(const id of ['update-card','sync-pause-status','memory-safety','announcement-banner','intent-alerts','lock-release-banner','action-status']) {
  const position=html.indexOf(`id="${id}"`);
  assert.ok(position>dock&&position<cards,id);
  assert.equal(html.split(`id="${id}"`).length,2,'unique '+id);
 }
 assert.ok(html.indexOf('id="toasts"')<html.indexOf('</main>'));
 assert.match(read('app.css'),/grid-template-rows:auto minmax\(0,1fr\) auto/);
 assert.match(read('app.css'),/\.dashboard-content \{[^}]*min-height:0[^}]*overflow-y:auto/);
 assert.match(read('app.css'),/\.dashboard-toasts:empty \{ display:none/);
});

test('journal precedes public shares in actual DOM order and update is outside the scrolling cards',()=>{
 const html=read('index.html');
 const side=html.indexOf('class="side-column"');
 assert.ok(html.indexOf('id="activity-card"')>side);
 assert.ok(html.indexOf('id="activity-card"')<html.indexOf('id="public-shares-card"'));
 assert.equal(html.split('id="activity-card"').length,2);
 assert.ok(html.indexOf('id="update-card"')<html.indexOf('id="dashboard-content"'));
 assert.equal(html.includes('id="update-store-actions"'),false);
});
test('background progress is placed in the same stationary notification dock',()=>{
 const js=read('progress-overlay.js');
 assert.match(js,/getElementById\("notification-dock"\)/);
 assert.match(js,/if \(dock\) dock.appendChild\(strip\)/);
 assert.doesNotMatch(js,/hero.nextSibling/);
});

test('all six statistics remain unique in the stationary header before cards',()=>{
 const html=read('index.html'), header=html.indexOf('class="dashboard-header"');
 const dock=html.indexOf('id="notification-dock"'), cards=html.indexOf('id="dashboard-content"');
 for(const id of ['metric-servers','metric-repos','metric-pending','metric-reservations','metric-public-shares','metric-attention']) {
  const position=html.indexOf(`id="${id}"`);
  assert.ok(position>header&&position<dock&&position<cards,id);
  assert.equal(html.split(`id="${id}"`).length,2,'unique '+id);
 }
 assert.match(read('app.css'),/\.dashboard-header \.metrics article \{[^}]*min-height:76px/);
 assert.match(read('app.css'),/@media \(max-height:600px\)/);
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

test('header background spans both themed window edges and statistics have breathing room',()=>{
 const css=read('app.css');
 assert.match(css,/\.dashboard-header::before \{[^}]*width:100vw[^}]*background:var\(--background\)[^}]*pointer-events:none/);
 assert.match(css,/\.dashboard-header \.metrics \{[^}]*margin:8px 0 24px/);
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
