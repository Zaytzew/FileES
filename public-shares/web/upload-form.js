(function (root, factory) {
    "use strict";
    var api = factory();
    if (typeof module === "object" && module.exports) module.exports = api;
    if (root.document) api.start(root.document, root);
}(typeof globalThis !== "undefined" ? globalThis : this, function () {
    "use strict";
    var maxFiles = 2000;
    var labels = {
        selected: "Gotowe do wys\u0142ania", invalid: "Sprawd\u017a wyb\u00f3r", sending: "Wysy\u0142anie",
        pending: "Oczekuje na przyj\u0119cie", accepted: "Przyj\u0119te na p\u00f3\u0142k\u0119",
        rejected: "Odrzucone przez kontrol\u0119 bezpiecze\u0144stwa", attention: "Przyj\u0119cie wymaga uwagi",
        uncertain: "Nie potwierdzono wysy\u0142ki. Nie wysy\u0142aj ponownie bez sprawdzenia.",
        refused: "Nie wys\u0142ano. Kana\u0142 nie przyj\u0105\u0142 pliku."
    };
    function problem(file, path, maxBytes) {
        if (!file || !Number.isSafeInteger(file.size) || file.size <= 0) return "Plik jest pusty lub niedost\u0119pny.";
        if (file.size > maxBytes) return "Plik przekracza limit tej przesy\u0142ki.";
        if (!path || path.length > 4096 || /[\\:\x00-\x1f\x7f-\x9f\u200b-\u200f\u202a-\u202e\u2060-\u206f]/.test(path)) return "Nieprawid\u0142owa \u015bcie\u017cka.";
        var parts = path.split("/");
        if (parts.length > 32 || parts[parts.length - 1] !== file.name || parts.some(function (p) { return !p || p.indexOf("..") >= 0; })) return "Nieprawid\u0142owa \u015bcie\u017cka.";
        if (parts.slice(0, -1).some(function (p) { return p[0] === "." || p.length > 512; })) return "Niedozwolona nazwa folderu.";
        if (!/^[^.].*\.[a-zA-Z0-9]{1,12}$/.test(file.name)) return "Nazwa pliku musi mie\u0107 rozszerzenie.";
        return "";
    }
    function Selection(maxBytes) { this.maxBytes = maxBytes; this.rows = []; this.nextID = 0; this.busy = false; }
    Selection.prototype.add = function (file, path) {
        if (this.busy) return;
        path = path || file.webkitRelativePath || file.name;
        if (this.rows.some(function (r) { return r.path === path && r.file === file; })) return;
        if (this.rows.some(function (r) { return r.path === path; })) throw new Error("Ta \u015bcie\u017cka jest ju\u017c na li\u015bcie. Wycofaj plik przed wybraniem innego.");
        if (this.rows.length >= maxFiles) throw new Error("Za du\u017co plik\u00f3w w jednej li\u015bcie (limit 2000).");
        var reason = problem(file, path, this.maxBytes);
        this.rows.push({ id: ++this.nextID, file: file, path: path, state: reason ? "invalid" : "selected", reason: reason, uploadID: "", repoPath: "" });
    };
    Selection.prototype.withdraw = function (key, subtree) {
        if (this.busy) return;
        this.rows = this.rows.filter(function (r) {
            var selected = r.state === "selected" || r.state === "invalid";
            return !(selected && (subtree ? r.path.indexOf(key + "/") === 0 : r.id === key));
        });
    };
    Selection.prototype.confirm = function () {
        if (this.busy || this.rows.some(function (r) { return r.state === "invalid"; })) return [];
        var selected = this.rows.filter(function (r) { return r.state === "selected"; });
        if (selected.length) this.busy = true;
        return selected;
    };
    Selection.prototype.applyReceipt = function (row, result) {
        if (!result || result.upload_id !== row.uploadID) return false;
        if (["pending", "accepted", "rejected", "attention"].indexOf(result.state) < 0) return false;
        if (result.state === "accepted" && (!result.repo_path || result.size !== row.file.size)) return false;
        row.state = result.state; row.repoPath = result.repo_path || ""; row.reason = result.reason || "";
        return true;
    };
    function start(doc, win) {
        var form = doc.getElementById("upload-form"), input = doc.getElementById("upload-file"), drop = doc.getElementById("upload-drop");
        if (!form || !input || !drop || !win.FormData || !win.XMLHttpRequest || !win.fetch) return;
        var model = new Selection(Number(form.dataset.maxBytes)), list = doc.getElementById("upload-list"), review = doc.getElementById("upload-review");
        var summary = doc.getElementById("upload-summary"), notice = doc.getElementById("upload-notice"), confirm = doc.getElementById("upload-confirm");
        var folder = doc.getElementById("upload-folder"), folderLabel = doc.getElementById("upload-folder-label"), opened = Object.create(null);
        var preview = doc.getElementById("upload-preview"), previewBody = doc.getElementById("upload-preview-body"), previewName = doc.getElementById("upload-preview-name");
        var previewURL = "", previewEpoch = 0, pollTimer = null, pollCursor = 0, scanning = false;
        var target = new win.URL(win.location.href); target.searchParams.delete("upload_status");
        input.multiple = true; input.required = false; confirm.disabled = true;
        if ("webkitdirectory" in folder) folderLabel.hidden = false;
        function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text) e.textContent = text; return e; }
        function action(text, fn) { var b = el("button", "upload-action", text); b.type = "button"; b.addEventListener("click", fn); return b; }
        function canWithdraw(row) { return row.state === "selected" || row.state === "invalid"; }
        function size(bytes) { return bytes < 1024 ? bytes + " B" : (bytes / 1048576).toLocaleString("pl", { maximumFractionDigits: 2 }) + " MB"; }
        function closePreview() {
            previewEpoch++;
            if (previewURL) { win.URL.revokeObjectURL(previewURL); previewURL = ""; }
            previewBody.replaceChildren();
            if (preview.open) preview.close();
        }
        async function showPreview(row) {
            closePreview(); var epoch = previewEpoch;
            previewName.textContent = row.path;
            previewBody.append(el("p", "hint", size(row.file.size)));
            if (/\.(png|jpe?g|gif|webp|bmp)$/i.test(row.file.name)) {
                previewURL = win.URL.createObjectURL(row.file);
                var image = el("img", "upload-preview-image"); image.src = previewURL; image.alt = row.path; previewBody.append(image);
            } else if (/\.(txt|csv|log|md|json|xml)$/i.test(row.file.name)) {
                var text = await row.file.slice(0, 131072).text();
                if (epoch !== previewEpoch) return;
                previewBody.append(el("pre", "upload-preview-text", text));
                if (row.file.size > 131072) previewBody.append(el("p", "hint", "Pokazano pocz\u0105tek pliku."));
            } else previewBody.append(el("p", "hint", "Ten format sprawd\u017a w swoim programie przed wys\u0142aniem."));
            if (epoch === previewEpoch) preview.showModal();
        }
        doc.getElementById("upload-preview-close").addEventListener("click", closePreview);
        preview.addEventListener("cancel", function (e) { e.preventDefault(); closePreview(); });
        function reason(row) {
            if (row.state === "invalid") return row.reason;
            var messages = { collision: "Plik o tej nazwie ju\u017c istnieje na p\u00f3\u0142ce.", scanner: "Kontrola bezpiecze\u0144stwa jest chwilowo niedost\u0119pna.", storage: "Serwer nie ma miejsca na przyj\u0119cie.", confirmation: "Zapis nie jest potwierdzony. Nie wysy\u0142aj ponownie.", operation: "Przyj\u0119cie nie zosta\u0142o potwierdzone." };
            return row.state === "attention" ? messages[row.reason] || messages.operation : "";
        }
        function render() {
            var active = doc.activeElement, focusID = active && active.dataset && active.dataset.focus;
            list.replaceChildren(); review.hidden = !model.rows.length;
            var count = model.rows.length, bytes = model.rows.reduce(function (sum, r) { return sum + r.file.size; }, 0);
            var accepted = model.rows.filter(function (r) { return r.state === "accepted"; }).length;
            var waiting = model.rows.filter(function (r) { return r.state === "pending" || r.state === "attention"; }).length;
            summary.textContent = "Pliki: " + count + " \u00b7 " + size(bytes) + " \u00b7 przyj\u0119te: " + accepted + (waiting ? " \u00b7 oczekuj\u0105ce: " + waiting : "");
            var tree = { folders: Object.create(null), files: [] };
            model.rows.forEach(function (row) {
                var parts = row.path.split("/"), node = tree, path = "";
                parts.slice(0, -1).forEach(function (p) { path += (path ? "/" : "") + p; if (!node.folders[p]) node.folders[p] = { path: path, folders: Object.create(null), files: [] }; node = node.folders[p]; });
                node.files.push(row);
            });
            function appendTree(node, parent) {
                Object.keys(node.folders).sort().forEach(function (name) {
                    var branch = node.folders[name], details = el("details", "upload-directory"), heading = el("summary", "", name);
                    details.open = !!opened[branch.path]; details.addEventListener("toggle", function () { opened[branch.path] = details.open; });
                    if (!model.busy && model.rows.some(function (r) { return canWithdraw(r) && r.path.indexOf(branch.path + "/") === 0; })) {
                        var remove = action("Wycofaj folder", function (e) { e.preventDefault(); model.withdraw(branch.path, true); render(); });
                        heading.append(remove);
                    }
                    details.append(heading); var children = el("div", "upload-branch"); appendTree(branch, children); details.append(children); parent.append(details);
                });
                node.files.forEach(function (row) {
                    var item = el("div", "upload-row"); item.dataset.state = row.state;
                    var info = el("div", "upload-row-info"), name = action(row.path.split("/").pop(), function () { showPreview(row).catch(function () { notice.textContent = "Nie uda\u0142o si\u0119 otworzy\u0107 podgl\u0105du."; }); });
                    name.className += " upload-name"; name.dataset.focus = "preview-" + row.id; info.append(name);
                    info.append(el("span", "upload-size", size(row.file.size)));
                    var state = el("span", "upload-state", labels[row.state] + (row.progress ? " \u00b7 " + row.progress + "%" : "")); info.append(state);
                    if (reason(row)) info.append(el("span", "upload-reason", reason(row)));
                    if (row.repoPath) info.append(el("span", "upload-destination", "Na p\u00f3\u0142ce: " + row.repoPath));
                    item.append(info);
                    if (!model.busy && canWithdraw(row)) { var remove = action("Wycofaj", function () { model.withdraw(row.id, false); render(); }); remove.dataset.focus = "withdraw-" + row.id; item.append(remove); }
                    parent.append(item);
                });
            }
            appendTree(tree, list);
            if (focusID) { var found = Array.from(list.querySelectorAll("button")).find(function (b) { return b.dataset.focus === focusID; }); if (found) found.focus(); }
            confirm.disabled = model.busy || scanning || !model.rows.some(function (r) { return r.state === "selected"; }) || model.rows.some(function (r) { return r.state === "invalid"; });
            input.disabled = folder.disabled = model.busy || scanning;
            form.dataset.busy = model.busy ? "1" : "0"; form.setAttribute("aria-busy", model.busy ? "true" : "false");
        }
        function addFiles(files) {
            if (model.busy || scanning) return;
            notice.textContent = "";
            try { Array.from(files).forEach(function (file) { model.add(file); }); }
            catch (error) { notice.textContent = error.message; }
            input.value = folder.value = ""; render();
        }
        input.addEventListener("change", function () { addFiles(input.files); });
        folder.addEventListener("change", function () { addFiles(folder.files); });
        ["dragenter", "dragover"].forEach(function (event) { drop.addEventListener(event, function (e) { e.preventDefault(); drop.dataset.over = "1"; }); });
        drop.addEventListener("dragleave", function () { delete drop.dataset.over; });
        async function readEntry(entry, parent) {
            var path = parent + entry.name;
            if (entry.isFile) { var file = await new Promise(function (resolve, reject) { entry.file(resolve, reject); }); model.add(file, path); }
            else if (entry.isDirectory) {
                var reader = entry.createReader(), batch;
                do { batch = await new Promise(function (resolve, reject) { reader.readEntries(resolve, reject); }); for (var i = 0; i < batch.length; i++) await readEntry(batch[i], path + "/"); } while (batch.length);
            }
        }
        drop.addEventListener("drop", async function (e) {
            e.preventDefault(); delete drop.dataset.over;
            if (model.busy || scanning) return;
            var entries = Array.from(e.dataTransfer.items || []).filter(function (item) { return item.kind === "file"; }).map(function (item) { return item.webkitGetAsEntry ? item.webkitGetAsEntry() : null; });
            if (!entries.length || entries.some(function (entry) { return !entry; })) { addFiles(e.dataTransfer.files); return; }
            scanning = true; notice.textContent = "Odczytywanie listy plik\u00f3w\u2026"; render();
            try { for (var i = 0; i < entries.length; i++) await readEntry(entries[i], ""); notice.textContent = ""; }
            catch (error) { notice.textContent = error.message || "Nie uda\u0142o si\u0119 odczyta\u0107 folderu."; }
            scanning = false; render();
        });
        function send(row) {
            return new Promise(function (resolve) {
                row.state = "sending"; render();
                var body = new win.FormData(); body.append("relative_path", row.path); body.append("file", row.file, row.file.name);
                var xhr = new win.XMLHttpRequest(); xhr.open("POST", target.href); xhr.setRequestHeader("Accept", "application/json");
                xhr.upload.addEventListener("progress", function (e) { if (e.lengthComputable) { row.progress = Math.floor(e.loaded * 100 / e.total); render(); } });
                var finished = false;
                function done(state) { if (finished) return; finished = true; row.state = state; row.progress = 0; render(); resolve(); }
                xhr.onload = function () {
                    if (xhr.status === 202) {
                        try {
                            var response = JSON.parse(xhr.responseText);
                            if (/^[0-9a-f-]{36}$/.test(response.upload_id) && response.state === "pending" && response.size === row.file.size && /^[0-9a-f]{64}$/.test(response.sha256)) {
                                row.uploadID = response.upload_id; row.checksum = response.sha256; done("pending"); return;
                            }
                        } catch (ignore) {}
                        done("uncertain");
                    } else if (xhr.status >= 400 && xhr.status < 500 || xhr.status === 503) done("refused");
                    else done("uncertain");
                };
                xhr.onerror = xhr.onabort = xhr.ontimeout = function () { done("uncertain"); };
                try { xhr.send(body); } catch (ignore) { done("uncertain"); }
            });
        }
        async function poll() {
            pollTimer = null;
            var pending = model.rows.filter(function (r) { return r.uploadID && (r.state === "pending" || r.state === "attention"); });
            if (!pending.length) return;
            if (!doc.hidden) {
                for (var i = 0; i < Math.min(30, pending.length); i++) {
                    var row = pending[(pollCursor + i) % pending.length], url = new win.URL(target.href); url.searchParams.set("upload_status", row.uploadID);
                    var abort = new win.AbortController(), timer = win.setTimeout(function () { abort.abort(); }, 15000);
                    try {
                        var response = await win.fetch(url.href, { credentials: "same-origin", cache: "no-store", signal: abort.signal, headers: { Accept: "application/json" } });
                        if (!response.ok) { notice.textContent = "Nie mo\u017cna teraz potwierdzi\u0107 przyj\u0119cia. Zachowaj t\u0119 kart\u0119."; continue; }
                        var result = await response.json();
                        if (result.state === "accepted" && result.sha256 !== row.checksum) continue;
                        if (model.applyReceipt(row, result)) render();
                    } catch (ignore) { notice.textContent = "Czekamy na potwierdzenie serwera. Nie wysy\u0142aj tych plik\u00f3w ponownie."; }
                    finally { win.clearTimeout(timer); }
                }
                pollCursor += 30;
            }
            if (model.rows.every(function (r) { return r.state === "accepted"; })) notice.textContent = "Wszystkie pliki zosta\u0142y przyj\u0119te na p\u00f3\u0142k\u0119.";
            schedulePoll();
        }
        function schedulePoll() {
            if (!pollTimer && model.rows.some(function (r) { return r.uploadID && (r.state === "pending" || r.state === "attention"); })) pollTimer = win.setTimeout(poll, 5000);
        }
        form.addEventListener("submit", async function (e) {
            e.preventDefault(); if (scanning) return;
            var selected = model.confirm(); if (!selected.length) return;
            notice.textContent = ""; render();
            for (var i = 0; i < selected.length; i++) await send(selected[i]);
            model.busy = false; render(); notice.textContent = "Wysy\u0142ka zako\u0144czona. Sprawd\u017a wynik przy ka\u017cdym pliku."; schedulePoll();
        });
        win.addEventListener("beforeunload", function (e) { if (model.busy || model.rows.some(function (r) { return r.state === "pending" || r.state === "attention" || r.state === "uncertain"; })) { e.preventDefault(); e.returnValue = ""; } });
        return model;
    }
    return { Selection: Selection, start: start, problem: problem };
}));
