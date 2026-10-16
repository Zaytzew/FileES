import test from "node:test";
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import {runInNewContext} from "node:vm";
import {languages} from "../frontend/i18n.js";
import {reconcileProgressLine} from "../frontend/import-progress.js";
import {queuePreparing, queuedChangesLine} from "../frontend/queue-state.js";

test("known copies never look remote during startup or server outage",()=>{
 const source=readFileSync(new URL("../frontend/app.js",import.meta.url),"utf8");
 const start=source.indexOf("function renderRepo("),end=source.indexOf("\nconst expandedIdleGroups",start);
 const ctx={queuePreparing,queuedChangesLine,reconcileProgressLine,t:key=>key,escapeHTML:String,bytes:String,localizedStates:new Set(["initializing","offline","active"]),repoAction:()=>"",repoIcons:{},repoProblemLine:()=>"",repoSection:()=>"active",readRepoView:()=>({}),drawerPicker:()=>"",renderUnportable:()=>"",dateTime:String};
 runInNewContext(source.slice(start,end),ctx);
 const row={id:"repo",display_name:"Project",local_path:"/local/project",local_copy_presence:"present",local_copy_kind:"virtual",can_open:true};
 for(const state of ["initializing","unattached","offline"]){
  const html=ctx.renderRepo({...row,display_state:state,connectivity:"offline"});
  assert.match(html,/\/local\/project/);assert.match(html,/copy.kind.virtual/);assert.doesNotMatch(html,/repo.remote|repo-state-overlay"/);
  assert.match(html,/is-virtual/);assert.match(html,/repo-cloud-overlay/);
  assert.doesNotMatch(html,/<small[^>]*>[^<]*copy.kind.virtual/);
  assert.match(html,state==="offline"?/copy.waitingConnection/:/copy.refreshing/);
 }
 const unavailable=ctx.renderRepo({...row,display_state:"initializing",local_copy_presence:"unavailable",can_open:false});
 assert.match(unavailable,/copy.localUnavailable/);assert.doesNotMatch(unavailable,/repo.remote/);
 const remote=ctx.renderRepo({id:"remote",display_state:"unattached"});assert.match(remote,/repo.remote/);
 assert.doesNotMatch(remote,/repo-cloud-overlay|is-virtual/);
 const sparse=ctx.renderRepo({...row,local_copy_kind:'sparse',display_state:'active'});
 assert.doesNotMatch(sparse,/repo-cloud-overlay|is-virtual/);
 const busy=ctx.renderRepo({...row,display_state:'busy',connectivity:'online',attached:true});
 assert.match(busy,/repo-cloud-overlay/);assert.match(busy,/repo-state-overlay is-busy/);
 const error=ctx.renderRepo({...row,display_state:'attention',connectivity:'online'});
 assert.match(error,/repo-cloud-overlay/);assert.match(error,/repo-state-overlay is-attention/);
 const waiting=ctx.renderRepo({...row,display_state:"initializing",local_copy_waiting_connection:true});
 assert.match(waiting,/copy.waitingConnection/);assert.doesNotMatch(waiting,/is-busy/);
 const css=readFileSync(new URL("../frontend/app.css",import.meta.url),"utf8");
 assert.match(css,/\.repo-open\.is-local\s*\{[^}]*filter:none/);
});

test("local observation copy is translated without changing availability into hydration",()=>{
 for(const language of languages)for(const key of ["copy.kind.working_copy","copy.kind.sparse","copy.kind.virtual","copy.checking","copy.localUnavailable","copy.waitingConnection","copy.refreshing"])
  assert.equal(typeof language.messages[key],"string",`${language.code} ${key}`);
});
