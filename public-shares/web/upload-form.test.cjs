const {test} = require('node:test');
const assert = require('node:assert/strict');
const {Selection} = require('./upload-form.js');
const file = (name, size = 8) => ({name, size, lastModified: 1});

test('file and subtree withdrawal freeze before any transport', () => {
    const s = new Selection(1024);
    s.add(file('a.txt'), 'Oferta/a.txt'); s.add(file('b.txt'), 'Oferta/Rysunki/b.txt'); s.add(file('c.txt'), 'Inne/c.txt');
    s.withdraw('Oferta/Rysunki', true);
    assert.deepEqual(s.rows.map(r=>r.path), ['Oferta/a.txt','Inne/c.txt']);
    s.withdraw('Oferta',true);
    const frozen = s.confirm(); assert.equal(frozen.length,1);
    assert.equal(s.confirm().length,0);
    s.withdraw('Inne',true); s.add(file('d.txt')); assert.equal(s.rows.length,1);
});
test('oversize, empty and unsafe selections cannot be confirmed', () => {
    for (const [f,p] of [[file('a.txt',0),'a.txt'],[file('a.txt',1025),'a.txt'],[file('a.txt'),'../a.txt'],[file('a.txt'),'.svn/a.txt'],[file('a.txt'),'C:/a.txt']]) {
        const s = new Selection(1024);s.add(f,p); assert.equal(s.rows[0].state,'invalid');assert.equal(s.confirm().length,0);
        s.withdraw(s.rows[0].id,false);assert.equal(s.rows.length,0);
    }
});
test('queued reply is not final success and foreign or malformed status is ignored', () => {
    const s=new Selection(1024);s.add(file('x.txt')); const r=s.rows[0]; r.uploadID='id';
    assert.equal(s.applyReceipt(r,{upload_id:'other',state:'accepted',repo_path:'x.txt',size:8}),false);
    assert.equal(s.applyReceipt(r,{upload_id:'id',state:'accepted',size:8}),false);
    assert.equal(s.applyReceipt(r,{upload_id:'id',state:'pending'}),true);assert.equal(r.state,'pending');
    assert.equal(s.applyReceipt(r,{upload_id:'id',state:'accepted',repo_path:'x.txt',size:8}),true);assert.equal(r.state,'accepted');
    s.busy=false;s.withdraw(r.id,false);assert.equal(s.rows.length,1);
});
test('a different file at the same path requires an explicit withdrawal', () => {
    const s = new Selection(1024); const first=file('a.txt'); s.add(first); s.add(first);
    assert.equal(s.rows.length,1);
    assert.throws(()=>s.add(file('a.txt',9)),/Ta/);
    assert.equal(s.rows[0].file,first);
    s.withdraw(s.rows[0].id,false); s.add(file('a.txt',9));
    assert.equal(s.rows.length,1); assert.equal(s.rows[0].file.size,9);
});
