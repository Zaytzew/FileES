// @ts-check
// Wails binding for the Wehikuł czasu window (TimeMachineService).
//
// Kept by hand, like pairingservice.js and promptservice.js: a regenerated
// bindings tree renames the hand-kept modules other windows import. The IDs
// come from `wails3 generate bindings` for filees/cmd/filees-gui-wails and are
// asserted against the build context by timemachine_service_test.go. Results
// arrive as plain objects in the daemon's JSON shape.
import { Call as $Call } from "/wails/runtime.js";

export function Repositories() {
    return $Call.ByID(3993252539);
}

export function Context() {
    return $Call.ByID(3963988158);
}

export function Resolve(serverID, repoID, momentUTC, boundary) {
    return $Call.ByID(3005509185, serverID, repoID, momentUTC, boundary);
}

export function Commits(snapshotID, from, to, cursor) {
    return $Call.ByID(2437090663, snapshotID, from, to, cursor);
}

export function Changes(snapshotID, revision, cursor) {
    return $Call.ByID(285647072, snapshotID, revision, cursor);
}

export function List(snapshotID, path, cursor) {
    return $Call.ByID(1949481919, snapshotID, path, cursor);
}

export function Density(snapshotID, bucketHours, utcOffsetMinutes, cursor) {
    return $Call.ByID(3998683337, snapshotID, bucketHours, utcOffsetMinutes, cursor);
}

export function ChooseDestination() {
    return $Call.ByID(2002927558);
}

export function Fetch(payload) {
    return $Call.ByID(1963484241, payload);
}

export function Operation(operationID) {
    return $Call.ByID(1519860772, operationID);
}

export function Confirm(operationID) {
    return $Call.ByID(3305799153, operationID);
}

export function Cancel(operationID) {
    return $Call.ByID(3346073713, operationID);
}

export function Close() {
    return $Call.ByID(1754646125);
}
