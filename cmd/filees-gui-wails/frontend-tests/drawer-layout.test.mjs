import test from "node:test";
import assert from "node:assert/strict";
import {emptyDrawers,parseDrawers,changeDrawers,groupDrawers,commitDrawerChange} from "../frontend/drawer-layout.js";
import {repoSection} from "../frontend/repo-view.js";
const create=(state,id,name=id)=>changeDrawers(state,{type:"create",id,name});
const assign=(state,repo,id)=>changeDrawers(state,{type:"assign",repo,id});

test("drawer survives attach/detach, aliases, foreign grants and attention",()=>{
 let state=create(emptyDrawers(),"one","Projekt <nowy>"); state=assign(state,"owned","one"); state=assign(state,"guest","one");
 const repos=[{id:"owned",attached:true},{id:"guest",attached:false,ownership:"guest",display_state:"attention"},{id:"loose"}];
 const first=groupDrawers(repos,state);
 assert.deepEqual(first.groups[0].repos.map(r=>r.id),["owned","guest"]);
 assert.deepEqual(groupDrawers(repos.map(r=>({...r,attached:!r.attached,display_name:"Changed"})),state).groups[0].repos.map(r=>r.id),["owned","guest"]);
 assert.deepEqual(first.loose.map(r=>r.id),["loose"]);
});
test("deleting a drawer keeps repositories and other assignments",()=>{
 let state=create(create(emptyDrawers(),"one"),"two"); state=assign(assign(state,"a","one"),"b","two");
 state=changeDrawers(state,{type:"delete",id:"one"});
 const groups=groupDrawers([{id:"a"},{id:"b"}],state);
 assert.equal(groups.loose[0].id,"a"); assert.equal(groups.groups[0].repos[0].id,"b");
 assert.equal(groups.groups.length,1);
});
test("archive classification is per drawer and a new commit wakes the same row",()=>{
 let state=assign(create(emptyDrawers(),"one"),"a","one");
 const now=Date.parse("2026-09-19T12:00:00Z"); const repo={id:"a",server_id:"s",can_fold_inactive:true,last_commit_at:"2026-08-01T12:00:00Z"};
 const prefs={inactive:14,archive:30};
 assert.equal(repoSection(groupDrawers([repo],state).groups[0].repos[0],prefs,now),"archived");
 const awake={...repo,last_commit_at:new Date(now).toISOString()};
 assert.equal(repoSection(groupDrawers([awake],state).groups[0].repos[0],prefs,now),"active");
});
test("future/malformed layout is not silently reset",()=>{
 for (const data of ['{"schema":"future"}','null','[]','{"schema":"filees.gui.drawers/v1","drawers":[],"repos":{"a":"missing"}}']) assert.throws(()=>parseDrawers(data));
 assert.throws(()=>create(emptyDrawers(),"one"," "));
});
test("CAS conflict rebases only this gesture and preserves another clone's drawer",async()=>{
 const original={scope:"realm",version:"old",data:JSON.stringify(emptyDrawers())}; let calls=0;
 const saved=await commitDrawerChange(original,{type:"create",id:"local",name:"Local"},async(expected,data)=>{
  calls++;
  if (calls===1) return {scope:"realm",version:"new",conflict:true,data:JSON.stringify(create(emptyDrawers(),"remote","Remote"))};
  assert.equal(expected,"new"); return {scope:"realm",version:"saved",data};
 });
 assert.equal(calls,2); assert.deepEqual(parseDrawers(saved.data).drawers.map(d=>d.id),["remote","local"]);
});
test("CAS cannot resurrect a concurrently deleted destination or cross realms",async()=>{
 const original={scope:"one",version:"old",data:JSON.stringify(create(emptyDrawers(),"destination"))};
 await assert.rejects(()=>commitDrawerChange(original,{type:"assign",repo:"r",id:"destination"},async()=>({scope:"one",version:"new",conflict:true,data:JSON.stringify(emptyDrawers())})),/drawers.changed/);
 await assert.rejects(()=>commitDrawerChange(original,{type:"delete",id:"destination"},async()=>({scope:"other",version:"new",data:""})),/drawers.changed/);
});

// Exercise the production renderer, including its archive gate and shelf nesting.
import {readFileSync} from "node:fs";
import {runInNewContext} from "node:vm";
import {repoOrder} from "../frontend/repo-view.js";
import {shelvesFor} from "../frontend/shelf-layout.js";
test("renderer keeps archives and shelves in their drawer, including empty drawers",()=>{
 const source=readFileSync(new URL("../frontend/app.js",import.meta.url),"utf8");
 const extract=name=>{const start=source.indexOf(`function ${name}(`);return source.slice(start,source.indexOf("\n}",start)+2);};
 let layout=create(create(create(emptyDrawers(),"one","<First>"),"two"),"empty");
 layout=assign(assign(assign(layout,"a","one"),"b","two"),"attention","one");
 const repos=[{id:"a",server_id:"s",last_commit_at:"2020-01-01",can_fold_inactive:true},{id:"b",server_id:"s",last_commit_at:"2020-01-01",can_fold_inactive:true},{id:"attention",server_id:"s"},{id:"loose",server_id:"s"}];
 const shelf={id:"shelf",server_id:"s",purpose:"upload_shelf",parent_repo_id:"a"};
 const escapeHTML=value=>String(value).replaceAll("&","&amp;").replaceAll("<","&lt;").replaceAll('"',"&quot;");
 const render=runInNewContext(`${extract("renderDrawers")}\n${extract("renderRepoGroup")}\nrenderDrawers`,{
  parseDrawers,groupDrawers,drawerState:()=>({state:{data:JSON.stringify(layout)},ready:true}),drawerEnabled:()=>true,
  escapeHTML,t:key=>key,repoOrder,readRepoView:()=>({inactive:14,archive:30}),repoSection,
  expandedIdleGroups:new Set(),currentSnapshot:{repositories:[...repos,shelf],notices:[{repo_id:"attention"}]},
  shelvesFor,renderRepo:repo=>`<row id="${repo.id}"></row>`
 });
 const html=render({id:"s",gui_scope:"r"},repos);
 const first=html.slice(html.indexOf("&lt;First>"),html.indexOf('<h4>two'));
 assert.match(first,/folders.archived/);assert.match(first,/<row id="a">/);assert.match(first,/<row id="attention">/);
 assert.match(first,/parent-shelves/);assert.match(first,/<row id="shelf">/);
 assert.doesNotMatch(first,/<row id="b">/);assert.match(html,/drawers.empty/);assert.match(html,/<row id="loose">/);
 const keys=[...html.matchAll(/data-idle-key="([^"]+)"/g)].map(m=>m[1]);assert.equal(new Set(keys).size,keys.length);
 assert.match(render({id:"s",gui_scope:"r"},[]),/drawers.empty/);
});

test('background drawer refresh keeps controls enabled and cannot overwrite a newer gesture', async () => {
 const source=readFileSync(new URL('../frontend/app.js',import.meta.url),'utf8');
 const extract=name=>{
  const start=source.indexOf(`function ${name}(`);
  return source.slice(source.lastIndexOf('\n',start)+1,source.indexOf('\n}',start)+2);
 };
 for (const rejected of [false,true]) {
  const server={id:'s',gui_scope:'realm'};
  const original={scope:'realm',version:'old',data:''};
  const entry={state:original,ready:true,busy:false,fetching:false,generation:0,next:0};
  let resolve,reject;
  const pending=new Promise((yes,no)=>{resolve=yes;reject=no;});
  const api=runInNewContext(`${extract('refreshDrawers')}\n${extract('modifyDrawers')}\n${extract('drawerToolbar')}\n({refreshDrawers,modifyDrawers,drawerToolbar})`,{
   drawerState:()=>entry,drawerServer:()=>server,drawerEnabled:()=>true,
   GUIService:{GetGUIBlob:()=>pending,SetGUIBlob:async(serverID,expected,data)=>({scope:'realm',version:'written',data})},
   commitDrawerChange,rememberDrawers:(_,entry,state)=>{entry.state=state;entry.ready=true;},
   currentSnapshot:{capabilities:['realm.gui_blob.v1']},renderRepositories:()=>{},scheduleWindowFit:()=>{},
   drawerKey:()=>'',drawerDrafts:new Map(),expandedIdleGroups:new Set(),escapeHTML:String,t:key=>key,
   showToast:()=>assert.fail('unexpected mutation failure'),
  });
  api.refreshDrawers(server);
  assert.equal(entry.fetching,true);
  assert.doesNotMatch(api.drawerToolbar(server),/disabled|drawers.loading/);
  await api.modifyDrawers('s',{type:'create',id:'local',name:'New drawer'});
  assert.equal(entry.state.version,'written');
  if(rejected)reject(Error('old request failed'));else resolve(original);
  await new Promise(r=>setTimeout(r,0));
  assert.equal(entry.ready,true);
  assert.equal(entry.fetching,false);
  assert.equal(entry.state.version,'written');
  assert.equal(parseDrawers(entry.state.data).drawers[0].id,'local');
 }
});
