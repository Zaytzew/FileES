// @ts-check
// Wails binding for the "Browse on the server" window (HeadBrowserService).
//
// Kept by hand like timemachineservice.js: the IDs come from the Wails build
// context for filees/cmd/filees-gui-wails and are asserted by
// headbrowser_service_test.go. Results arrive as plain objects in the daemon's
// JSON shape.
import { Call as $Call } from "/wails/runtime.js";

export function Open(serverID, repoID) {
    return $Call.ByID(1302558521, serverID, repoID);
}

export function Context() {
    return $Call.ByID(1296624396);
}

export function Repository() {
    return $Call.ByID(635738405);
}

export function List(path) {
    return $Call.ByID(1147532241, path);
}

export function Preview(path) {
    return $Call.ByID(3881288903, path);
}

export function OpenLocal(path) {
    return $Call.ByID(1320231244, path);
}

export function Materialize(path) {
    return $Call.ByID(2128678882, path);
}

export function Fill() {
    return $Call.ByID(2384190360);
}

export function Operation(operationID) {
    return $Call.ByID(667235286, operationID);
}

export function Close() {
    return $Call.ByID(2252102579);
}
