import test from "node:test";
import assert from "node:assert/strict";
import { shelvesFor, unparentedShelves } from "../frontend/shelf-layout.js";

test("shelves follow explicit parent ID, never name or another server", () => {
  const parent = {id:"parent",server_id:"one",display_name:"Renamed project"};
  const child = {id:"shelf",server_id:"one",parent_repo_id:"parent",purpose:"upload_shelf"};
  const orphan = {id:"orphan",server_id:"one",display_name:"Renamed project",purpose:"upload_shelf"};
  const other = {...child,id:"other",server_id:"two"};
  const repos = [parent,child,orphan,other];
  assert.deepEqual(shelvesFor(parent,repos),[child]);
  assert.deepEqual(unparentedShelves(repos),[orphan,other]);
  assert.deepEqual(shelvesFor(child,repos),[]);
  assert.deepEqual(unparentedShelves([{...parent,server_deleted:true},child]),[child]);
});
