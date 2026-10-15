// Only gcloud's own OAuth clients are auto-approved. Any other app's consent
// screen is left alone, so this can't be used to silently grant access.
const GCLOUD_CLIENT_IDS = new Set([
  "32555940559.apps.googleusercontent.com", // gcloud auth login
  "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com", // gcloud auth application-default login
]);

const LOCAL_HOSTS = new Set(["localhost", "127.0.0.1"]);
const tabKey = (tabId) => `gcloud-tab-${tabId}`;

function isGcloudAuthStart(url) {
  if (url.hostname !== "accounts.google.com") return false;
  if (!/^\/o\/oauth2\/(v2\/)?auth/.test(url.pathname)) return false;
  const clientId = url.searchParams.get("client_id");
  const redirect = url.searchParams.get("redirect_uri") || "";
  let redirectHost = "";
  try { redirectHost = new URL(redirect).hostname; } catch { return false; }
  return GCLOUD_CLIENT_IDS.has(clientId) && LOCAL_HOSTS.has(redirectHost);
}

function isFlowFinished(url) {
  // gcloud's local server receives ?code=... then redirects to auth_success.
  if (LOCAL_HOSTS.has(url.hostname) && (url.searchParams.has("code") || url.searchParams.has("error"))) return true;
  return url.hostname === "cloud.google.com" && url.pathname.startsWith("/sdk/auth_success");
}

chrome.webNavigation.onBeforeNavigate.addListener(async ({ tabId, frameId, url }) => {
  if (frameId !== 0) return;
  const u = new URL(url);
  if (isGcloudAuthStart(u)) {
    await chrome.storage.session.set({ [tabKey(tabId)]: true });
  }
});

chrome.webNavigation.onCommitted.addListener(async ({ tabId, frameId, url }) => {
  if (frameId !== 0) return;
  const key = tabKey(tabId);
  const { [key]: tracked } = await chrome.storage.session.get(key);
  if (!tracked || !isFlowFinished(new URL(url))) return;
  await chrome.storage.session.remove(key);
  // Small delay so gcloud's local server finishes handling the redirect.
  setTimeout(() => chrome.tabs.remove(tabId).catch(() => {}), 1500);
});

chrome.tabs.onRemoved.addListener((tabId) => chrome.storage.session.remove(tabKey(tabId)));

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg?.type !== "isGcloudTab" || !sender.tab) return;
  const key = tabKey(sender.tab.id);
  chrome.storage.session.get(key).then((r) => sendResponse(Boolean(r[key])));
  return true;
});
