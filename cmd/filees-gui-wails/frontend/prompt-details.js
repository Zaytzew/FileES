import { t } from "./i18n.js";

// GUI-only labels. Values are opaque data, never keys or nested templates.
export function promptDetailText(key, args, text = t) {
  if (key === "details.intent") {
    const paths = JSON.parse(args.paths || "[]");
    const rows = paths.map(item => text(item.Operation === "add" ? "details.intent.add" : "details.intent.delete", {path: item.Path, size: item.Size}));
    return args.name + "\n\n" + text("details.intent.warning") + "\n\n" + rows.join("\n") + "\n\n" + text("details.intent.next");
  }
  if (key === "details.server") {
    return text("details.server.body", {
      ...args,
      address: args.address || text("details.unknown"),
      client: args.client || text("details.unknown"),
      role: text(args.role === "ro" ? "details.readOnly" : "details.full"),
      creation: text(args.creation === "true" ? "details.allowed" : "details.denied"),
    });
  }
  if (key === "details.update" || key === "details.updateApply") {
    let body = text("details.update.missing");
    if (args.missing !== "true") {
      body = text("details.update.versions", args);
      if (args.release) body += "\n" + text("details.update.release", args);
      body += "\n\n" + (args.changes ? text("details.update.changes") + "\n" + args.changes : text("details.update.noChanges"));
      if (args.restart === "true") body += "\n\n" + text("details.update.restart");
    }
    if (key === "details.updateApply") body += "\n\n" + text("details.update.question");
    return body;
  }
  return "";
}
