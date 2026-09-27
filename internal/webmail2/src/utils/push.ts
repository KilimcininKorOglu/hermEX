import api from "./api"

// urlBase64ToUint8Array converts a base64url VAPID key to the byte array that
// PushManager.subscribe expects as applicationServerKey.
function urlBase64ToUint8Array(base64String: string): Uint8Array<ArrayBuffer> {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4)
  const base64 = (base64String + padding).replace(/-/g, "+").replace(/_/g, "/")
  const raw = atob(base64)
  // Build on a concrete ArrayBuffer so the result satisfies applicationServerKey.
  const buffer = new ArrayBuffer(raw.length)
  const output = new Uint8Array(buffer)
  for (let i = 0; i < raw.length; i++) {
    output[i] = raw.charCodeAt(i)
  }
  return output
}

export function pushSupported(): boolean {
  return "serviceWorker" in navigator && "PushManager" in window && "Notification" in window
}

// PushError is a push setup failure the user can act on; key names its message
// in the locale catalogue.
export class PushError extends Error {
  readonly key: string

  constructor(key: string) {
    super(key)
    this.name = "PushError"
    this.key = key
  }
}

// enablePushNotifications registers the service worker, requests permission and
// subscribes to web push. Throws a PushError when push is unsupported, denied,
// or not configured on the server.
export async function enablePushNotifications(): Promise<void> {
  if (!pushSupported()) {
    throw new PushError("settings.push.notSupported")
  }

  const permission = await Notification.requestPermission()
  if (permission !== "granted") {
    throw new PushError("settings.push.permissionDenied")
  }

  const registration = await navigator.serviceWorker.register("/sw.js")
  await navigator.serviceWorker.ready

  let key: string | undefined
  try {
    const res = await api.getVapidPublicKey()
    key = res.key
  } catch {
    throw new PushError("settings.push.notConfigured")
  }
  if (!key) {
    throw new PushError("settings.push.notConfigured")
  }

  const subscription = await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: urlBase64ToUint8Array(key),
  })

  const json = subscription.toJSON()
  await api.subscribePush({
    endpoint: json.endpoint ?? "",
    keys: {
      p256dh: json.keys?.p256dh ?? "",
      auth: json.keys?.auth ?? "",
    },
  })
}

// disablePushNotifications removes the current push subscription, if any.
export async function disablePushNotifications(): Promise<void> {
  if (!("serviceWorker" in navigator)) return
  const registration = await navigator.serviceWorker.getRegistration()
  const subscription = await registration?.pushManager.getSubscription()
  if (subscription) {
    try {
      await api.unsubscribePush(subscription.endpoint)
    } catch {
      // best effort: still unsubscribe locally
    }
    await subscription.unsubscribe()
  }
}
