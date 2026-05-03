/** Reads ytInitialPlayerResponse from the page and returns video + caption info. */
function getVideoInfo() {
  try {
    const pr = window.ytInitialPlayerResponse;
    if (!pr) return { error: "Player not ready — refresh the page and try again." };

    const tracks = pr?.captions?.playerCaptionsTracklistRenderer?.captionTracks;
    if (!tracks?.length) return { error: "This video has no captions available." };

    const preferred =
      tracks.find((t) => t.languageCode === "en" && t.kind === "asr") ||
      tracks.find((t) => t.languageCode === "en") ||
      tracks.find((t) => t.languageCode?.startsWith("en")) ||
      tracks[0];

    if (!preferred?.baseUrl) return { error: "No usable caption track found." };

    const title =
      document.querySelector("h1.ytd-watch-metadata yt-formatted-string")?.textContent?.trim() ||
      document.querySelector("h1.ytd-video-primary-info-renderer yt-formatted-string")?.textContent?.trim() ||
      document.title.replace(/ - YouTube$/, "").trim();

    return {
      baseUrl: preferred.baseUrl,
      lang: preferred.languageCode,
      title,
      videoId: new URLSearchParams(location.search).get("v"),
      trackCount: tracks.length,
    };
  } catch (e) {
    return { error: e.message };
  }
}

/** Fetches caption JSON3 from YouTube and returns plain text. */
async function fetchTranscriptText(baseUrl) {
  const resp = await fetch(`${baseUrl}&fmt=json3`);
  if (!resp.ok) throw new Error(`Caption fetch failed: HTTP ${resp.status}`);

  const data = await resp.json();
  if (!data?.events?.length) throw new Error("Caption data was empty.");

  const text = data.events
    .filter((e) => e.segs)
    .flatMap((e) => e.segs)
    .map((s) => s.utf8 || "")
    .join("")
    .replace(/\n/g, " ")
    .replace(/\s+/g, " ")
    .trim();

  if (text.length < 100) throw new Error("Transcript too short — video may be music-only.");
  return text;
}

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg.action === "getVideoInfo") {
    sendResponse(getVideoInfo());
  } else if (msg.action === "fetchTranscript") {
    fetchTranscriptText(msg.baseUrl)
      .then((text) => sendResponse({ text }))
      .catch((err) => sendResponse({ error: err.message }));
    return true; // keep channel open for async response
  }
});
