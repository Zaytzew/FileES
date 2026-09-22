// @ts-check
// Wails binding for the "Browse on the server" window (HeadBrowserService).
//
// Kept by hand like timemachineservice.js. Wails derives each ID from
// FNV-1a("main.HeadBrowserService.<Method>"): in the released binary this
// package's path is "main", not its import path - IDs taken from a test binary
// are wrong there. headbrowser_service_test.go computes them from that rule. Results arrive as plain objects in the daemon's
// JSON shape.
import { Call as $Call } from "/wails/runtime.js";

export function Open(serverID, repoID) {
    return $Call.ByID(710813287, serverID, repoID);
}

export function Context() {
    return $Call.ByID(1497377474);
}

export function Repository() {
    return $Call.ByID(3031534591);
}

export function List(path) {
    return $Call.ByID(2454661859, path);
}

export function Preview(path) {
    return $Call.ByID(104937325, path);
}

export function OpenLocal(path) {
    return $Call.ByID(979704198, path);
}

export function Materialize(path) {
    return $Call.ByID(3147328124, path);
}

export function Fill() {
    return $Call.ByID(158188730);
}

export function Operation(operationID) {
    return $Call.ByID(2266335560, operationID);
}

export function Close() {
    return $Call.ByID(2404765921);
}
