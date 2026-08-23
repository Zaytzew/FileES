// @ts-check
// Wails named binding kept deliberately small: this service is both the
// platform Prompter implementation and the browser-facing dialog bridge.
import { Call as $Call } from "/wails/runtime.js";

export function Snapshot() {
    return $Call.ByName("filees/cmd/filees-gui-wails.PromptService.Snapshot");
}

export function Resolve(choice) {
    return $Call.ByName("filees/cmd/filees-gui-wails.PromptService.Resolve", choice);
}

export function Cancel() {
    return $Call.ByName("filees/cmd/filees-gui-wails.PromptService.Cancel");
}
