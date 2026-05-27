// SolidPing service worker — push-only (no caching).

self.addEventListener('push', (event) => {
    const data = event.data?.json() ?? {};
    event.waitUntil(
        self.registration.showNotification(data.title ?? 'SolidPing alert', {
            body:  data.body  ?? '',
            icon:  '/dash0/icon-192.png',
            data:  { url: data.url ?? '/dash0/' },
            tag:   data.url ?? 'solidping',
            renotify: true,
        })
    );
});

self.addEventListener('notificationclick', (event) => {
    event.notification.close();
    event.waitUntil(
        clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
            for (const c of list) {
                if (c.url === event.notification.data.url) return c.focus();
            }
            return clients.openWindow(event.notification.data.url);
        })
    );
});
