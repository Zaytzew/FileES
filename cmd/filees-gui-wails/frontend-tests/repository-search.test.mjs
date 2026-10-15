import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {runInNewContext} from 'node:vm';
import {filterRepositories,searchText} from '../frontend/repository-search.js';
import {localAllocationMarkup} from '../frontend/local-allocation.js';

const servers=[{id:'spot',display_name:'Pracownia',address:'example.invalid'}];
const repos=[
 {id:'a',server_id:'spot',display_name:'TOWAROWA-PŁOŃSK',local_path:'D:\\01_OPRACOWANIA\\Towarowa',own_name:'Projekt'},
 {id:'b',server_id:'spot',display_name:'ARCHIWUM',local_path:'/data/archiwum'},
 {id:'c',server_id:'cloud',display_name:'Zdalny',local_path:''}
];

test('search is local, accent/case insensitive, literal and matches all words',()=>{
 assert.equal(searchText('  PŁOŃSK  '),'plonsk');
 for(const query of ['towarowa','Płońsk','plonsk pracownia','D:\\01_OPRACOWANIA','projekt example.invalid'])
  assert.deepEqual(filterRepositories(repos,servers,query),[repos[0]],query);
 assert.deepEqual(filterRepositories(repos,servers,'[.*'),[]);
 assert.deepEqual(filterRepositories(repos,servers,'towarowa zdalny'),[]);
 assert.equal(filterRepositories(repos,servers,'  '),repos);
 assert.equal(filterRepositories(repos,servers,'pracownia').length,2);
 assert.deepEqual(filterRepositories(repos,servers,'zdalny'),[repos[2]]);
 assert.equal(filterRepositories([{...repos[0],display_state:'offline'}],servers,'towarowa').length,1);
});

test('results bypass closed groups, preserve expansion state and follow fresh snapshots',()=>{
 const source=readFileSync(new URL('../frontend/app.js',import.meta.url),'utf8');
 const start=source.indexOf('function renderRepositories('),end=source.indexOf('\nfunction renderActions',start);
 const nodes=new Map();
 const ctx={repositoryQuery:'towarowa',searchText,filterRepositories,localAllocationMarkup,
  $:id=>{if(!nodes.has(id))nodes.set(id,{});return nodes.get(id);},
  t:(key,args)=>key+JSON.stringify(args||{}),tn:(key,count)=>`${key} ${count}`,escapeHTML:String,bytes:String,
  replaceHTMLIfChanged:(_node,html)=>html,seenServers:new Set(['spot','cloud']),expandedServers:new Set(),
  renderRepo:repo=>`ROW:${repo.id}`,repoOrder:()=>0,refreshDrawers:()=>{},
  unparentedShelves:()=>[],renderDrawers:()=>'<details>CLOSED DRAWER</details>',drawerToolbar:()=>'',
  demoPresentation:()=>null,serverHealthPresentation:()=>({className:'health-unavailable',label:'offline'}),
  storageCapacityMarkup:()=>'',renderRepoGroup:(_label,items)=>items.map(r=>`GROUP:${r.id}`).join('')};
 runInNewContext(source.slice(start,end),ctx);
 const snapshot={repositories:repos,servers:[...servers,{id:'cloud'}],connected:false};
 const found=ctx.renderRepositories(snapshot);
 assert.match(found,/ROW:a/);assert.doesNotMatch(found,/ROW:b|CLOSED DRAWER|data-server-id="cloud"|class="server-folders" hidden/);
 assert.equal(ctx.expandedServers.size,0);
 assert.match(nodes.get('#repository-search-count').textContent,/"count":1,"total":3/);
 const changed=ctx.renderRepositories({...snapshot,repositories:repos.slice(1)});
 assert.match(changed,/repoSearch.empty/);assert.doesNotMatch(changed,/ROW:a/);
 ctx.repositoryQuery='';
 const restored=ctx.renderRepositories(snapshot);
 assert.match(restored,/CLOSED DRAWER/);assert.match(restored,/class="server-folders" hidden/);
 assert.equal(ctx.expandedServers.size,0);
 assert.equal(nodes.get('#repository-search-clear').disabled,true);
});
