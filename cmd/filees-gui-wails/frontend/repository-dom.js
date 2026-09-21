// Reconcile the repository panel without recreating its controls on every
// snapshot. Keys are presentation identities, never authority or filesystem paths.
function key(node) {
  if (node.nodeType !== 1) return null;
  if (node.matches('.server-panel[data-server-id]')) {
    return JSON.stringify(['server', node.dataset.serverId, node.dataset.guiScope || '']);
  }
  // A conditional group/action must not lend its DOM to the next group or
  // action when a neighbour disappears. In particular, keep the same SVG.
  if (node.matches('.realm-group')) return JSON.stringify(['group', node.className]);
  if (node.matches('.repo-tools [data-action]')) return JSON.stringify(['action', node.dataset.action]);
  for (const attribute of ['data-drawer-id', 'data-repo-id', 'data-idle-key', 'id']) {
    if (node.hasAttribute(attribute)) return JSON.stringify([attribute, node.getAttribute(attribute)]);
  }
  return null;
}

function sameKind(left, right) {
  return left.nodeType === right.nodeType && left.nodeName === right.nodeName;
}

function patch(node, next) {
  if (node.nodeType !== 1) {
    if (node.nodeValue !== next.nodeValue) node.nodeValue = next.nodeValue;
    return;
  }
  // Column widths are measured by the window fitter, not the HTML renderer.
  const widths = node.matches('.server-folders')
    ? ['--repo-title-min', '--repo-actions-column'].map(name => [name, node.style.getPropertyValue(name)]) : [];
  for (const [name, value] of widths) if (value) next.style.setProperty(name, value);
  for (const attribute of [...node.attributes]) {
    if (!next.hasAttribute(attribute.name)) node.removeAttribute(attribute.name);
  }
  for (const attribute of next.attributes) {
    if (node.getAttribute(attribute.name) !== attribute.value) node.setAttribute(attribute.name, attribute.value);
  }
  children(node, next);
  // Changing an unchanged value resets selection in some WebViews. Preserve
  // the live input and caret; still apply an explicit draft clear after submit.
  if (['INPUT', 'SELECT', 'TEXTAREA'].includes(node.nodeName) && node.value !== next.value) {
    node.value = next.value;
  }
}

function children(parent, next) {
  const keyed = new Map([...parent.childNodes].map(node => [key(node), node]).filter(([id]) => id !== null));
  let cursor = parent.firstChild;
  for (const wanted of [...next.childNodes]) {
    const id = key(wanted);
    let node = id !== null ? keyed.get(id) : cursor && key(cursor) === null && sameKind(cursor, wanted) ? cursor : null;
    if (node && !sameKind(node, wanted)) node = null;
    if (!node) {
      node = wanted.cloneNode(true);
      parent.insertBefore(node, cursor);
    } else {
      if (node !== cursor) {
        // moveBefore preserves focus and native control state where supported.
        if (parent.moveBefore && node.isConnected === parent.isConnected) parent.moveBefore(node, cursor);
        else parent.insertBefore(node, cursor);
      }
      patch(node, wanted);
    }
    cursor = node.nextSibling;
  }
  while (cursor) {
    const obsolete = cursor;
    cursor = cursor.nextSibling;
    obsolete.remove();
  }
}

export function reconcileRepositoryHTML(root, html) {
  const template = root.ownerDocument.createElement('template');
  template.innerHTML = html;
  children(root, template.content);
}
