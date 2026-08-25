// SolidPing service worker — push-only (no caching).

// Activate an updated worker immediately instead of waiting for every existing
// tab to close. Safe here because this worker is push-only — it controls no
// fetches and caches nothing, so there is no version-skew risk.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()));

self.addEventListener('push', (event) => {
    const data = event.data?.json() ?? {};
    // Resolve a guaranteed-non-empty URL with `||` (not `??`): incident pushes
    // can arrive with an empty url, and Chrome rejects showNotification with a
    // TypeError ("Notifications which set the renotify flag must specify a
    // non-empty tag") when renotify is true and tag is empty. `??` only falls
    // back on null/undefined, so an empty string would slip through. Safari
    // doesn't enforce the rule, which is why this only broke on Chrome.
    const url = data.url || '/dash0/';
    event.waitUntil(
        self.registration.showNotification(data.title || 'SolidPing alert', {
            body:  data.body  || '',
            icon:  '/dash0/assets/favicon-192.png',
            data:  { url },
            tag:   url,
            renotify: true,
        })
    );
});

self.addEventListener('notificationclick', (event) => {
    event.notification.close();
    // Push URLs are relative in-app paths; client URLs are absolute. Resolve
    // against our origin so the "focus an already-open tab" match can ever hit.
    const target = new URL(event.notification.data.url, self.location.origin).href;
    event.waitUntil(
        clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
            for (const c of list) {
                if (c.url === target) return c.focus();
            }
            return clients.openWindow(target);
        })
    );
});
