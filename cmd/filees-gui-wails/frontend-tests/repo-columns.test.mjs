import test from "node:test";
import assert from "node:assert/strict";
import {repoToolsWidth} from "../frontend/repository-dom.js";

test("a row in a closed group still reserves its buttons",()=>{
 // A WebView that gives rows in a closed <details> no box measures them as 0.
 assert.equal(repoToolsWidth([0,0,0,0]),4*34+3*5);
 assert.equal(repoToolsWidth([38,34,0]),38+34+34+2*5);
 assert.equal(repoToolsWidth([]),0);
});
