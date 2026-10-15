// Runs on accounts.google.com, but only acts in tabs the background script
// marked as a gcloud login flow. Never touches password or 2FA fields.
const BUTTON_LABELS = ["continue", "allow", "next"];
const clicked = new WeakSet();
let lastClickAt = 0;

const isVisible = (el) => {
  const r = el.getBoundingClientRect();
  return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== "hidden";
};

function clickOnce(el) {
  if (!el || clicked.has(el) || Date.now() - lastClickAt < 1000) return false;
  clicked.add(el);
  lastClickAt = Date.now();
  el.click();
  return true;
}

function pickAccount() {
  // Account chooser: each signed-in account row carries its email.
  const rows = [...document.querySelectorAll("[data-identifier], [data-email]")].filter(isVisible);
  const emails = new Set(rows.map((r) => r.getAttribute("data-identifier") || r.getAttribute("data-email")));
  // Only auto-pick when there's exactly one account, so it never guesses.
  return emails.size === 1 && clickOnce(rows[0]);
}

function tickSelectAll() {
  const box = [...document.querySelectorAll('input[type="checkbox"]')].find(
    (b) => !b.checked && /select all/i.test(b.getAttribute("aria-label") || b.closest("label")?.innerText || "")
  );
  return box ? clickOnce(box) : false;
}

function pressButton() {
  if (document.querySelector('input[type="password"]:not([aria-hidden="true"])')) return false;
  const btn = [...document.querySelectorAll('button, [role="button"]')].find(
    (b) => isVisible(b) && !b.disabled && BUTTON_LABELS.includes(b.innerText.trim().toLowerCase())
  );
  return btn ? clickOnce(btn) : false;
}

function step() {
  pickAccount() || tickSelectAll() || pressButton();
}

chrome.runtime.sendMessage({ type: "isGcloudTab" }, (isGcloud) => {
  if (chrome.runtime.lastError || !isGcloud) return;
  step();
  new MutationObserver(step).observe(document.body, { childList: true, subtree: true });
  setInterval(step, 1000);
});
