// @ts-check
// Wails named binding kept deliberately small: this service is both the
// platform Prompter implementation and the browser-facing dialog bridge.
import { Call as $Call } from "/wails/runtime.js";

export function Snapshot() {
    return $Call.ByID(2105785425);
}

export function Resolve(choice) {
    return $Call.ByID(2590249023, choice);
}

export function Cancel() {
    return $Call.ByID(1442486127);
}
