import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

test("Polish shelf description uses the agreed file transfer and antivirus wording", () => {
  const locale = readFileSync(new URL("../frontend/locales/pl.js", import.meta.url), "utf8");
  const actions = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  const html = readFileSync(new URL("../frontend/repository.html", import.meta.url), "utf8");
  for (const source of [locale, actions]) {
    assert.ok(source.includes("przesyłają pliki przy pomocy przeglądarki"));
    assert.ok(source.includes("Pliki z negatywną rekomendacją antywirusową"));
    assert.ok(!source.includes("kładą plik przeglądarką"));
    assert.doesNotMatch(source, /odrzut\S*\s+(?:AV|antywir)/iu);
  }
  assert.ok(html.includes("Pliki z negatywną rekomendacją antywirusową"));
});

test("shelf resend button follows server-authorized projection", () => {
  const source = readFileSync(new URL("../frontend/repository.js", import.meta.url), "utf8");
  const begin = source.indexOf("function uploadCard("), end = source.indexOf("\n}", begin) + 2;
  const card = runInNewContext(`${source.slice(begin,end)}\nuploadCard`, {escapeHTML: value => String(value ?? "").replaceAll("<","&lt;").replaceAll('"',"&quot;"), labelHTML: key => key});
  for (const allowed of [false,true]) {
    const html=card({channel_id:'opaque"<', can_resend:allowed, address:"a/b", recipients:"a@example.test"});
    assert.equal(html.includes('data-upload-action="resend"'),allowed);
    assert.ok(!html.includes('channel-id="opaque"<'));
  }
});

test("multiline invitation prompt retains newlines and edits during translation", async () => {
  const nodes=new Map(), listeners=new Map();
  const makeNode=tag => {
    const element={tagName:tag.toUpperCase(), value:"", disabled:false, addEventListener(){}, replaceChildren(){}, focus(){}, select(){}, removeAttribute(){}, replaceWith(next){nodes.set("#prompt-value",next);}};
    // HTMLTextAreaElement.type is a getter-only property. Browser ES modules
    // are strict: assigning it aborts render before default text and labels.
    if (element.tagName === "TEXTAREA") Object.defineProperty(element,"type",{get:()=>"textarea"});
    return element;
  };
  const node=id => {if(!nodes.has(id)) nodes.set(id,makeNode("input")); return nodes.get(id);};
  let sent;
  const source=readFileSync(new URL("../frontend/prompt.js",import.meta.url),"utf8").replace(/^import .*;\r?\n/gm,"").replace(/try \{ render\(await PromptService\.Snapshot\(\)\); \} catch[^\n]*/,"");
  const api=runInNewContext(`"use strict";\n${source}\n({render,resolve,refreshPromptLabels})`,{
    initializeTheme(){},initializeLanguage(){},t:key=>key,
    document:{querySelector:node,addEventListener(){},createElement:makeNode},
    window:{addEventListener:(key,fn)=>listeners.set(key,fn),setTimeout(){}},
    Events:{On(){}},Window:{},console:{error(){}},
    PromptService:{Resolve:async choice=>{sent=choice;return {accepted:true};},Snapshot:async()=>({revision:1})}
  });
  api.render({revision:1,mode:"text",multiline:true,max_length:2000,default:"Saved\nmessage"});
  const input=node("#prompt-value");
  assert.equal(input.tagName,"TEXTAREA"); assert.equal(input.maxLength,2000); assert.equal(input.value,"Saved\nmessage");
  input.value="Edited\nmessage"; input.selectionStart=4;
  listeners.get("filees:language-changed")();
  assert.equal(input.value,"Edited\nmessage"); assert.equal(input.selectionStart,4);
  await api.resolve(true); assert.equal(sent.value,"Edited\nmessage");
  api.render({revision:2,mode:"text",secret:true,default:"secret"});
  assert.equal(node("#prompt-value").tagName,"INPUT"); assert.equal(node("#prompt-value").type,"password");
});
