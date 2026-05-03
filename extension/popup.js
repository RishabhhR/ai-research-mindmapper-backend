const API_BASE       = "https://mindmapper-api-mu.vercel.app";
const APP_URL        = "https://research-mindmapper-vite.vercel.app";
const EXT_PUBLIC_KEY = "o9GA9qbkxiDcO3P53LW9ukOClTWglw8919ttAifxJV0";

const sendBtn  = document.getElementById("send-btn");
const statusEl = document.getElementById("status");
const titleEl  = document.getElementById("video-title");
const resultEl = document.getElementById("result-link");

let videoInfo = null;

function setStatus(msg, type = "") {
  statusEl.textContent = msg;
  statusEl.className   = type;
}

/** Return a stable install UUID, creating one on first run. */
async function getInstallId() {
  return new Promise(resolve => {
    chrome.storage.local.get("installId", ({ installId }) => {
      if (installId) return resolve(installId);
      const id = crypto.randomUUID();
      chrome.storage.local.set({ installId: id }, () => resolve(id));
    });
  });
}

/** Build the auth token: ext_<uuid>_<public_key> */
async function buildToken() {
  const id = await getInstallId();
  return `ext_${id}_${EXT_PUBLIC_KEY}`;
}

// ── On popup open: ask content script for video info ─────────────────────────
chrome.tabs.query({ active: true, currentWindow: true }, ([tab]) => {
  if (!tab?.url?.includes("youtube.com/watch")) {
    setStatus("Open a YouTube video to get started.");
    return;
  }

  chrome.tabs.sendMessage(tab.id, { action: "getVideoInfo" }, (info) => {
    if (chrome.runtime.lastError || !info) {
      setStatus("Could not read page — refresh YouTube and try again.", "error");
      return;
    }

    videoInfo = info;

    if (info.error) {
      setStatus(info.error, "error");
      return;
    }

    titleEl.textContent      = info.title;
    titleEl.style.display    = "block";
    setStatus(`${info.trackCount} caption track(s) found.`);
    sendBtn.disabled = false;
  });
});

// ── Send button ───────────────────────────────────────────────────────────────
sendBtn.addEventListener("click", async () => {
  if (!videoInfo?.baseUrl) {
    setStatus("No caption URL — refresh and try again.", "error");
    return;
  }

  sendBtn.disabled    = true;
  resultEl.style.display = "none";
  setStatus("Fetching transcript from YouTube…");

  chrome.tabs.query({ active: true, currentWindow: true }, ([tab]) => {
    chrome.tabs.sendMessage(
      tab.id,
      { action: "fetchTranscript", baseUrl: videoInfo.baseUrl },
      async (res) => {
        if (chrome.runtime.lastError || !res) {
          setStatus("Page communication failed — refresh YouTube.", "error");
          sendBtn.disabled = false;
          return;
        }
        if (res.error) {
          setStatus(res.error, "error");
          sendBtn.disabled = false;
          return;
        }

        setStatus("Uploading to Mindmapper…");

        try {
          const token    = await buildToken();
          const safe     = videoInfo.title.replace(/[^\w\s-]/g, "").trim().slice(0, 80);
          const filename = `${safe}.txt`;

          const resp = await fetch(`${API_BASE}/api/sources`, {
            method: "POST",
            headers: {
              "Content-Type":  "text/plain",
              "X-Filename":    filename,
              "Authorization": `Bearer ${token}`,
            },
            body: res.text,
          });

          if (!resp.ok) {
            const err = await resp.json().catch(() => ({}));
            throw new Error(err.detail || `Server error ${resp.status}`);
          }

          const data      = await resp.json();
          const sessionId = data.session_id || data.id;
          const chunks    = data.chunks_count ?? "?";

          setStatus(`Done — ${chunks} chunks uploaded.`, "success");
          resultEl.innerHTML =
            `<a href="${APP_URL}" target="_blank">Open Mindmapper →</a>` +
            `<br><small style="color:#555;font-size:10px">session: ${sessionId}</small>`;
          resultEl.style.display = "block";
        } catch (e) {
          setStatus(`Upload failed: ${e.message}`, "error");
          sendBtn.disabled = false;
        }
      }
    );
  });
});
