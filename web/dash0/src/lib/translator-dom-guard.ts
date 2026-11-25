/**
 * Keeps React alive when a browser translator rewrites the page.
 *
 * Chrome's built-in translation (Google Translate) replaces the text nodes
 * React owns with `<font>` wrappers. React still holds the old nodes, so its
 * next commit calls `removeChild` / `insertBefore` with a node that is no
 * longer a child of the parent it remembers, and the DOM throws
 * `NotFoundError`: every page then falls into the route error boundary.
 * `<html lang>` (i18n.ts) stops the offer for the languages the app ships,
 * not for the others (ja, pt…).
 *
 * The guard makes those two calls a no-op, with a warning, when the node is
 * not where React expects it (the patch discussed in facebook/react#11538).
 * The translated text may then lag behind an update, but the page keeps
 * working, and the visitor keeps their translation.
 */

const GUARD_FLAG = "__solidpingTranslatorDomGuard";

type GuardedPrototype = typeof Node.prototype & { [GUARD_FLAG]?: boolean };

export function installTranslatorDomGuard(proto: typeof Node.prototype = Node.prototype): void {
  const guarded = proto as GuardedPrototype;
  if (guarded[GUARD_FLAG]) return;
  guarded[GUARD_FLAG] = true;

  const originalRemoveChild = proto.removeChild;
  proto.removeChild = function removeChild<T extends Node>(this: Node, child: T): T {
    if (child.parentNode !== this) {
      console.warn("Skipped removeChild of a node moved by a translator", child);
      return child;
    }
    return originalRemoveChild.call(this, child) as T;
  };

  const originalInsertBefore = proto.insertBefore;
  proto.insertBefore = function insertBefore<T extends Node>(
    this: Node,
    newNode: T,
    referenceNode: Node | null,
  ): T {
    if (referenceNode && referenceNode.parentNode !== this) {
      console.warn("Skipped insertBefore a node moved by a translator", referenceNode);
      return newNode;
    }
    return originalInsertBefore.call(this, newNode, referenceNode) as T;
  };
}
