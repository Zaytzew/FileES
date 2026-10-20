import assert from "node:assert/strict";
import {test} from "node:test";
import {languages, translate} from "../frontend/i18n.js";
import {conversionProgressLine} from "../frontend/conversion-progress.js";
import {reconcileProgressLine} from "../frontend/import-progress.js";
import {problemCardHTML} from "../frontend/problem-card.js";

const catalogues = Object.fromEntries(languages.map(x=>[x.code,x.messages]));
test("conversion stages and measured counts render in every language", () => {
  for (const {code} of languages) {
    const t = (key,args)=>translate(catalogues,code,key,args);
    for (const phase of ["waiting","inspecting","retaining","retained","creating","preserving","verifying","cleanup","rolling_back","finalizing"]) {
      const repo = {conversion:{operation_id:"id",phase,counters_known:true,done:3,total:9}};
      const line = conversionProgressLine(repo,t);
      assert.ok(line); assert.doesNotMatch(line, /\[conversion|%/);
      assert.equal(reconcileProgressLine(repo,t),line);
      if (["preserving","cleanup"].includes(phase)) assert.match(line,/3.*9/);
    }
    const repo = {conversion:{operation_id:"id",phase:"cleanup",error:"raw <path>", recovery_path:"D:/old", counters_known:false,done:3,total:9}};
    assert.match(conversionProgressLine(repo,t), /raw <path>.*D:\/old/);
    assert.doesNotMatch(conversionProgressLine(repo,t),/3.*9/);
    for (const phase of ["complete","canceled","rolled_back"]) assert.equal(conversionProgressLine({conversion:{operation_id:"id",phase}},t),"");
  }
});
test("unknown or invalid counters never become invented progress",()=>{
  const t=(key,args)=>translate(catalogues,"en",key,args);
  for (const [done,total] of [[-1,9],[10,9],[3,NaN],[3.5,9]]) {
    const line=conversionProgressLine({conversion:{operation_id:"id",phase:"cleanup",counters_known:true,done,total}},t);
    assert.equal(line,t("conversion.phase.cleanup"));
  }
});

test("control passes show measured discovery without a guessed total in all languages",()=>{
  for (const {code} of languages) {
    const t=(key,args)=>translate(catalogues,code,key,args);
    for (const check of ["source","custody","placeholders","local"]) {
      const p={operation_id:"id",phase:"verifying",check,done:17,total:0,counters_known:false};
      const line=conversionProgressLine({conversion:p},t);
      assert.match(line,/17/); assert.doesNotMatch(line, /\[conversion|%/);
      assert.ok(line.includes(t(`conversion.check.${check}`)));
      assert.ok(!line.includes(t("conversion.count",{done:17,total:0})));
      p.total=23; p.counters_known=true;
      assert.ok(conversionProgressLine({conversion:p},t).includes(t("conversion.count",{done:17,total:23})));
    }
    for (const check of [undefined,"future-check","<script>"]) {
      assert.equal(conversionProgressLine({conversion:{operation_id:"id",phase:"inspecting",check,done:17,counters_known:false}},t),t("conversion.phase.inspecting"));
    }
    const p={operation_id:"id",phase:"retaining",check:"source",done:-1,total:0,counters_known:false};
    assert.doesNotMatch(conversionProgressLine({conversion:p},t),/-1/);
  }
});
test("conversion problem shows literal cause and recovery without injecting markup",()=>{
  const t=(key,args)=>translate(catalogues,"en",key,args);
  const escape=x=>String(x).replaceAll("&","&amp;").replaceAll("<","&lt;");
  const html=problemCardHTML({kind:"conversion", reason:"raw <script>{value}", path:"D:/old<copy>"},t,escape);
  assert.match(html,/raw &lt;script>\{value\}/);
  assert.match(html,/D:\/old&lt;copy>/);
  assert.doesNotMatch(html,/<script>/);
});
