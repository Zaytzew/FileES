import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync,writeFileSync,chmodSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {spawnSync} from 'node:child_process';

test('staging checks APK metadata, revision, package and debug status',()=>{
  const dir=mkdtempSync(join(tmpdir(),'filees-apk-check-'));
  try {
    const aapt=join(dir,'aapt2'),apk=join(dir,'candidate with space.apk');
    writeFileSync(aapt,'#!/bin/sh\ncat "$3"\n');chmodSync(aapt,0o755);
    const run=(metadata,extra=[])=>{
      writeFileSync(apk,metadata);
      return spawnSync('sh',[resolve('tools/check-android-apk.sh'),apk,...extra],{
        env:{...process.env,AAPT2:aapt,EXPECTED_ANDROID_VERSION:'0.1.18+r1700',EXPECTED_ANDROID_CODE:'1700'},encoding:'utf8'
      });
    };
    const good="package: name='net.filees.mobile' versionCode='1700' versionName='0.1.18+r1700'\n";
    assert.equal(run(good).status,0);
    for(const bad of [good.replace('1700\' versionName','1699\' versionName'),good.replace('+r1700','+r1699'),good.replace('net.filees.mobile','other.app'),good+'application-debuggable\n']) {
      assert.notEqual(run(bad).status,0,bad);
    }
    assert.equal(run(good+'application-debuggable\n',['--allow-debug']).status,0);
  } finally { rmSync(dir,{recursive:true,force:true}); }
});
