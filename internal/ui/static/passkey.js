// harb passkey (WebAuthn) shim — minimal, no deps. Loaded from base.html
// only when passkey support is enabled. Wires two buttons:
//
//   #passkey-login-btn      (login page)    → navigator.credentials.get
//   #passkey-register-btn   (settings page) → navigator.credentials.create
//
// All URLs are built relative to the /ui/ root via data-ui-base, exactly
// like keys.js, so the app keeps working under a path prefix (e.g. the
// Tailscale Funnel /rss mount).
(function () {
  "use strict";

  const BASE = document.documentElement.dataset.uiBase || "./";
  const url = (seg) => new URL(seg, new URL(BASE, window.location.href)).href;

  // base64url (no padding) <-> ArrayBuffer.
  function b64urlToBuf(s) {
    s = s.replace(/-/g, "+").replace(/_/g, "/");
    while (s.length % 4) s += "=";
    const bin = atob(s);
    const buf = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
    return buf.buffer;
  }
  function bufToB64url(buf) {
    const bytes = new Uint8Array(buf);
    let bin = "";
    for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function showError(msg) {
    const el = document.getElementById("passkey-error");
    if (el) {
      el.textContent = msg;
      el.hidden = false;
    }
  }

  function supported() {
    return window.PublicKeyCredential && navigator.credentials;
  }

  // Decode the server's creation/request options (binary fields are
  // base64url strings) into the ArrayBuffer-typed shape the API needs.
  function decodeCreationOptions(o) {
    o.challenge = b64urlToBuf(o.challenge);
    o.user.id = b64urlToBuf(o.user.id);
    if (Array.isArray(o.excludeCredentials)) {
      o.excludeCredentials.forEach((c) => (c.id = b64urlToBuf(c.id)));
    }
    return o;
  }
  function decodeRequestOptions(o) {
    o.challenge = b64urlToBuf(o.challenge);
    if (Array.isArray(o.allowCredentials)) {
      o.allowCredentials.forEach((c) => (c.id = b64urlToBuf(c.id)));
    }
    return o;
  }

  async function postJSON(seg, body) {
    const res = await fetch(url(seg), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: body ? JSON.stringify(body) : undefined,
    });
    return res;
  }

  // ---- registration (settings page) --------------------------------
  async function register() {
    if (!supported()) {
      showError("this browser does not support passkeys");
      return;
    }
    try {
      const beginRes = await postJSON("webauthn/register/begin");
      if (!beginRes.ok) throw new Error("could not start registration");
      const opts = await beginRes.json();
      const pk = decodeCreationOptions(opts.publicKey);
      const cred = await navigator.credentials.create({ publicKey: pk });
      const label =
        window.prompt("Name this passkey (e.g. 'MacBook Touch ID')", "passkey") ||
        "passkey";
      const payload = {
        rawId: bufToB64url(cred.rawId),
        type: cred.type,
        response: {
          clientDataJSON: bufToB64url(cred.response.clientDataJSON),
          attestationObject: bufToB64url(cred.response.attestationObject),
        },
      };
      const finRes = await postJSON(
        "webauthn/register/finish?label=" + encodeURIComponent(label),
        payload
      );
      if (!finRes.ok) throw new Error("registration failed");
      window.location.reload();
    } catch (e) {
      showError(e && e.message ? e.message : "registration failed");
    }
  }

  // ---- login (login page) ------------------------------------------
  async function login() {
    if (!supported()) {
      showError("this browser does not support passkeys");
      return;
    }
    try {
      const beginRes = await postJSON("webauthn/login/begin");
      if (!beginRes.ok) throw new Error("could not start login");
      const opts = await beginRes.json();
      const pk = decodeRequestOptions(opts.publicKey);
      const assertion = await navigator.credentials.get({ publicKey: pk });
      const payload = {
        rawId: bufToB64url(assertion.rawId),
        type: assertion.type,
        response: {
          clientDataJSON: bufToB64url(assertion.response.clientDataJSON),
          authenticatorData: bufToB64url(assertion.response.authenticatorData),
          signature: bufToB64url(assertion.response.signature),
        },
      };
      const finRes = await postJSON("webauthn/login/finish", payload);
      if (!finRes.ok) throw new Error("authentication failed");
      window.location.href = BASE; // into the app
    } catch (e) {
      showError(e && e.message ? e.message : "authentication failed");
    }
  }

  document.addEventListener("DOMContentLoaded", function () {
    const reg = document.getElementById("passkey-register-btn");
    if (reg) reg.addEventListener("click", register);
    const log = document.getElementById("passkey-login-btn");
    if (log) log.addEventListener("click", login);
  });
})();
