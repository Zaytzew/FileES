// Stage everything install.sh needs into dist/site-publisher, for copying to
// the web server:  node packaging/site/stage.mjs
import {mkdir,copyFile,rm,readFile,writeFile} from 'node:fs/promises';
import {execFileSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
const here=path.dirname(fileURLToPath(import.meta.url));
const root=path.resolve(here,'..','..');
const out=path.join(root,'dist','site-publisher');
await rm(out,{recursive:true,force:true});
await mkdir(out,{recursive:true});
execFileSync('go',['build','-trimpath','-o',path.join(out,'filees-site-download'),'./cmd/filees-site-download'],
  {cwd:root,stdio:'inherit',env:{...process.env,GOOS:'linux',GOARCH:'amd64',CGO_ENABLED:'0'}});
await copyFile(path.join(root,'landing','download.json'),path.join(out,'download.json'));
await copyFile(path.join(root,'landing','release-key.pub'),path.join(out,'release-key.pub'));
await copyFile(path.join(root,'landing','download','index.html'),path.join(out,'download.html'));
// Both run on Linux: a Windows checkout may have given them CRLF, which breaks
// sh, and cron ignores a last line without a newline.
for(const name of ['install.sh','filees-site-download.cron']){
  const text=(await readFile(path.join(here,name),'utf8')).replace(/\r\n/g,'\n');
  await writeFile(path.join(out,name),text.endsWith('\n')?text:text+'\n');
}
console.log(out);
