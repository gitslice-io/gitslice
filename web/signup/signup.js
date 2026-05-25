const params = new URLSearchParams(window.location.search);
const usernameInput = document.querySelector("#username");
const callbackInput = document.querySelector("#callback-url");
const stateInput = document.querySelector("#state");
const gatewayInput = document.querySelector("#gateway-url");
const form = document.querySelector("#signup-form");
const message = document.querySelector("#message");
const statusPill = document.querySelector("#status-pill");
const title = document.querySelector("#signup-title");
const button = document.querySelector("#approve-button");

const username = params.get("username") || "";
const callbackUrl = params.get("callback_url") || params.get("callbackUrl") || "";
const state = params.get("state") || "";
const gatewayUrl = params.get("gateway_url") ||
  params.get("gatewayUrl") ||
  window.localStorage.getItem("gitslice.gatewayUrl") ||
  window.location.origin;

usernameInput.value = username;
callbackInput.value = callbackUrl;
stateInput.value = state;
gatewayInput.value = gatewayUrl;

if (username) {
  title.textContent = `Approve signup for ${username}`;
}

const missing = [];
if (!username) missing.push("username");
if (!callbackUrl) missing.push("callback_url");
if (!state) missing.push("state");
if (missing.length > 0) {
  setStatus("Blocked", "error");
  showMessage(`Missing required signup parameter: ${missing.join(", ")}.`, true);
  button.disabled = true;
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const apiBase = gatewayInput.value.trim().replace(/\/+$/, "");
  if (!apiBase) {
    showMessage("API Gateway is required.", true);
    return;
  }
  window.localStorage.setItem("gitslice.gatewayUrl", apiBase);
  setStatus("Approving", "busy");
  showMessage("Approving signup...", false);
  button.disabled = true;
  try {
    const response = await fetch(`${apiBase}/gitslice.core.v1.FakeAccountService/ApproveSignup`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        username,
        callbackUrl,
        state,
      }),
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) {
      throw new Error(data.message || `Signup approval failed with HTTP ${response.status}`);
    }
    if (!data.redirectUrl) {
      throw new Error("Signup approval response did not include redirectUrl.");
    }
    setStatus("Approved", "ok");
    showMessage("Signup approved. Returning to the CLI...", false);
    window.location.assign(data.redirectUrl);
  } catch (error) {
    setStatus("Failed", "error");
    showMessage(error.message || String(error), true);
    button.disabled = false;
  }
});

function setStatus(text, tone) {
  statusPill.textContent = text;
  statusPill.dataset.tone = tone;
}

function showMessage(text, isError) {
  message.textContent = text;
  message.dataset.tone = isError ? "error" : "normal";
}
