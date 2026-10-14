import test from 'node:test';
import assert from 'node:assert/strict';
import { importProgressLine } from '../frontend/import-progress.js';

test('resumed import uses its own files and bytes, not global backlog', () => {
  const repo = {local_provisioning:true, publish_files_done:330, publish_files_total:4544, publish_bytes_sent:668280014, publish_bytes_total:5838690169, pending_files:999};
  assert.deepEqual(importProgressLine(repo, (key,args)=>({key,args}), n=>`${n} B`), {
    key:'progress.published', args:{done:330,total:4544,sent:'668280014 B',size:'5838690169 B'},
  });
});

test('missing and completed import have no invented progress', () => {
  for (const repo of [{local_provisioning:true}, {local_provisioning:false,publish_files_total:10}, {local_provisioning:true,publish_files_total:-1}]) {
    assert.equal(importProgressLine(repo,()=>assert.fail('unexpected translation'),String),'');
  }
});
