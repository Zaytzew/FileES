// @ts-check
// Wails named binding kept deliberately small: this service is both the
// platform Prompter implementation and the browser-facing dialog bridge.
import { Call as $Call } from "/wails/runtime.js";

export function Snapshot() {
    return $Call.ByID(1755424007);
}

export function Resolve(choice) {
    return $Call.ByID(3660384409, choice);
}

export function Cancel() {
    return $Call.ByID(3669365241);
}
