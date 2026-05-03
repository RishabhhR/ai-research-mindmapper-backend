const API_BASE = "https://mindmapper-api-mu.vercel.app";
const APP_URL  = "https://research-mindmapper-vite.vercel.app";

const sendBtn    = document.getElementById("send-btn");
const statusEl   = document.getElementById("status");
const titleEl    = document.getElementById("video-title");
const resultEl   = document.getElementById("result-link");
const keyInput   = document.getElementById("api-key-input");
const saveKeyBtn = document.getElementById("save-key-btn");

let videoInfo = null;

function setStatus(msg, type = "") {
  statusEl.textContent = msg;
  statusEl.className = type;
}

// ── Load saved API key ───────────────────────────────────────────────────────
chrome.storage.local.get("apiKey", ({ apiKey }) => {
  if (apiKey) keyInput.value = apiKey;
});

saveKeyBtn.addEventListener("click", () => {
  const key = keyInput.value.trim();
  if (!key) return setStatus("Paste your API key first.", "error");
  chrome.storage.local.set({ apiKey: key }, () => {
    setStatus("Key saved.", "success");
    if (videoInfo && !videoInfo.error) sendBtn.disabled = false;
  });
});

// ── On open: ask content script for video info ───────────────────────────────
chrome.tabs.query({ active: true, currentWindow: true }, ([tab]) => {
  if (!tab?.url?.includes("youtube.com/watch")) {
    setStatus("Open a YouTube video first.");
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

    titleEl.textContent = info.title;
    titleEl.style.display = "block";
    setStatus(`${info.trackCount} caption track(s) found (${info.lang}).`);

    chrome.storage.local.get("apiKey", ({ apiKey }) => {
      if (apiKey) sendBtn.disabled = false;
      else setStatus("Save your API key below to enable upload.", "error");
    });
  });
});

// ── Send button ──────────────────────────────────────────────────────────────
sendBtn.addEventListener("click", async () => {
  const { apiKey } = await new Promise(r => chrome.storage.local.get("apiKey", r));
  if (!apiKey) return setStatus("No API key — save it below.", "error");
  if (!videoInfo?.baseUrl) return setStatus("No caption URL — refresh and try again.", "error");

  sendBtn.disabled = true;
  resultEl.style.display = "none";
  setStatus("Fetching transcript from YouTube…");

  // Ask content script to fetch the caption URL (runs on youtube.com, no CORS issue)
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
          const safe = videoInfo.title.replace(/[^\w\s-]/g, "").trim().slice(0, 80);
          const filename = `${safe}.txt`;

          const resp = await fetch(`${API_BASE}/api/sources`, {
            method: "POST",
            headers: {
              "Content-Type": "text/plain",
              "X-Filename": filename,
              "Authorization": `Bearer ${apiKey}`,
            },
            body: res.text,
          });

          if (!resp.ok) {
            const err = await resp.json().catch(() => ({}));
            throw new Error(err.detail || `Server error ${resp.status}`);
          }

          const data = await resp.json();
          const sessionId = data.session_id || data.id;
          const chunks = data.chunks_count ?? "?";

          setStatus(`Done — ${chunks} chunks uploaded.`, "success");
          resultEl.innerHTML =
            `<a href="${APP_URL}" target="_blank">Open Mindmapper →</a>` +
            `<br><small style="color:#666;font-size:10px">session: ${sessionId}</small>`;
          resultEl.style.display = "block";
        } catch (e) {
          setStatus(`Upload failed: ${e.message}`, "error");
          sendBtn.disabled = false;
        }
      }
    );
  });
});
