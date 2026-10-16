import test from "node:test";
import assert from "node:assert/strict";
import {queuePreparing, queuedChangesLine, queueSummary} from "../frontend/queue-state.js";
import {languages} from "../frontend/i18n.js";
const t = key => key;
const ready = {attached:true,display_state:"active",cycle:{phase:"waiting",cycle_id:0}};

test("startup does not declare an empty queue before local runtime is ready",()=>{
  for(const repo of [
    {attached:false,local_copy_presence:"present",display_state:"unattached"},
    {...ready,cycle:{}}, {...ready,display_state:"initializing"},
    {local_copy_presence:"checking"},
  ]) {
    assert.equal(queuePreparing(repo),true);
    assert.deepEqual(queueSummary({connected:true,repositories:[repo]},t,String),{value:"…",note:"queue.loading"});
  }
  assert.equal(queuePreparing(ready),false); // cycle zero with scheduled runtime is ready
  assert.deepEqual(queueSummary({connected:true,repositories:[ready]},t,String),{value:0,note:"summary.emptyQueue"});
  assert.equal(queueSummary({connected:true,repositories:[ready,{local_copy_presence:"present",pending_files:1,pending_bytes:10}]},t,String).value,"1+");
});

test("remote, missing, shelf and offline folders never spin indefinitely",()=>{
  for(const repo of [{display_state:"unattached"}, {...ready,local_copy_presence:"unavailable"}, {...ready,purpose:"upload_shelf"}, {...ready,display_state:"offline"}]) assert.equal(queuePreparing(repo),false);
  const snapshot={connected:false,repositories:[{...ready,pending_files:1,pending_bytes:10}]};
  assert.match(queueSummary(snapshot,t,String).note,/summary.lastKnown/);
  assert.match(queueSummary({...snapshot,connected:true,stale:true},t,String).note,/summary.lastKnown/);
});

test("blocked pending work is distinguished from ordinary waiting",()=>{
  for(const [display_state,key] of [["attention","attention"],["offline","offline"],["paused","paused"]]) {
    const repo={...ready,display_state,pending_files:1,pending_bytes:10809858};
    assert.equal(queuedChangesLine(repo,t,String),`1 · 10809858 · queue.${key}`);
    if(key==="attention") assert.match(queueSummary({connected:true,repositories:[repo]},t,String).note,/queue.attention/);
  }
  assert.equal(queuedChangesLine({...ready,pending_files:1,pending_bytes:10},t,String),"1 · 10");
  for(const language of languages) for(const key of ["loading","attention","offline","paused"]) assert.equal(typeof language.messages[`queue.${key}`],"string");
});
