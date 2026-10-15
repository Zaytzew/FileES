import test from "node:test";
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import {runInNewContext} from "node:vm";

test("public share resend is capability-gated and escapes opaque IDs",()=>{
  const source=readFileSync(new URL("../frontend/repository.js",import.meta.url),"utf8");
  const begin=source.indexOf("function shareCard("),end=source.indexOf("\n}",begin)+2;
  assert.ok(begin>=0);
  const card=runInNewContext(`${source.slice(begin,end)}\nshareCard`,{
    escapeHTML:v=>String(v??"").replaceAll("<","&lt;").replaceAll('"',"&quot;"),
    labelHTML:k=>k,t:k=>k,currentSnapshot:null,publicShareURL:()=>""
  });
  for(const allowed of [false,true]){
    const html=card({channel_id:'opaque"<',can_resend:allowed,address:"a/b",recipients:"a@example.test"});
    assert.equal(html.includes('data-share-action="resend"'),allowed);
    assert.ok(!html.includes('channel-id="opaque"<'));
  }
});
