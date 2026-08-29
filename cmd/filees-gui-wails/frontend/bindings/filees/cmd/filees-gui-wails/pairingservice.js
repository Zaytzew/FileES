// @ts-check
// Wails binding for the deliberately narrow native mobile-pairing bridge.
import { Call as $Call } from "/wails/runtime.js";

export function Snapshot() {
    return $Call.ByID(288208043);
}

export function Close(request) {
    return $Call.ByID(358808385, request);
}

export function Cancel() {
    return $Call.ByID(1746464029);
}
