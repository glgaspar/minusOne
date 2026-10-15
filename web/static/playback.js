// Shared by the queue player (player.js) and the practice page (practice.js):
// the video, the mixer UI and its saved settings.
//
// Video: a muted YouTube embed. The audio is the clock; the video follows it
// and is re-seeked when it drifts.

const SYNC_TOLERANCE_S = 0.3; // video drift allowed before re-seeking
const SYNC_COOLDOWN_MS = 2000; // time a re-seek gets to settle before the next one

const $ = (id) => document.getElementById(id);

function formatTime(seconds) {
  const s = Math.floor(seconds);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

// ---------------------------------------------------------------- video

let yt = null;
let ytReady = false;
let videoAvailable = true;
let lastVideoSeek = 0;
let cuedVideoId = null; // cued before the API was ready

window.onYouTubeIframeAPIReady = () => {
  yt = new YT.Player("yt-player", {
    width: "100%",
    height: "100%",
    playerVars: {
      controls: 0,
      disablekb: 1,
      fs: 0,
      iv_load_policy: 3,
      playsinline: 1,
      rel: 0,
      mute: 1,
      origin: location.origin,
    },
    events: {
      onReady: () => {
        ytReady = true;
        yt.mute();
        if (cuedVideoId) yt.cueVideoById(cuedVideoId);
      },
      // 101/150: the owner doesn't allow embedding; 100: removed. The audio
      // doesn't depend on the video, so show the thumbnail and carry on.
      onError: () => {
        videoAvailable = false;
        showCover(true);
      },
    },
  });
};

function loadYouTubeAPI() {
  const script = document.createElement("script");
  script.src = "https://www.youtube.com/iframe_api";
  document.head.append(script);
}

function cueVideo(videoId) {
  videoAvailable = true;
  cuedVideoId = videoId;
  $("cover").src = `https://i.ytimg.com/vi/${videoId}/hqdefault.jpg`;
  showCover(false);
  if (ytReady) yt.cueVideoById(videoId);
}

function showCover(show) {
  $("cover").hidden = !show;
}

function syncVideo(audioTime, playing) {
  if (!ytReady || !videoAvailable) return;
  const state = yt.getPlayerState();
  if (!playing) {
    if (state === YT.PlayerState.PLAYING) yt.pauseVideo();
    return;
  }
  // past the video's end (or the video is shorter): let it rest
  if (state === YT.PlayerState.ENDED || audioTime >= yt.getDuration() - 0.5) return;
  if (state !== YT.PlayerState.PLAYING && state !== YT.PlayerState.BUFFERING) yt.playVideo();

  const now = performance.now();
  if (now - lastVideoSeek > SYNC_COOLDOWN_MS && Math.abs(yt.getCurrentTime() - audioTime) > SYNC_TOLERANCE_S) {
    yt.seekTo(audioTime, true);
    lastVideoSeek = now;
  }
}

// ---------------------------------------------------------------- mixer prefs

// The original's preference depends on whether the song has split tracks:
// alone it's the only sound, next to the stems it would double them.
function prefKey(track, hasStems) {
  if (track.type !== "original") return track.type;
  return hasStems ? "original:with-tracks" : "original:alone";
}

// store is the localStorage key: the queue player and the practice page keep
// their own settings, so practicing with the vocals muted doesn't mute the TV.
function loadPrefs(store) {
  try {
    return JSON.parse(localStorage.getItem(store)) || {};
  } catch {
    return {};
  }
}

function savePref(store, key, pref) {
  try {
    const prefs = loadPrefs(store);
    prefs[key] = pref;
    localStorage.setItem(store, JSON.stringify(prefs));
  } catch {}
}

// buildMixer shows a row per track with its volume and mute. mixer is anything
// with tracks ({type, label, muted}) and setGain(track, value, immediate).
function buildMixer(song, mixer, store) {
  const box = $("mixer");
  box.replaceChildren();
  const prefs = loadPrefs(store);
  const hasStems = song.tracks.some((t) => t.type !== "original");

  for (const track of mixer.tracks) {
    const key = prefKey(track, hasStems);
    const pref = { volume: 1, muted: track.muted, ...prefs[key] };

    const row = $("track-template").content.firstElementChild.cloneNode(true);
    const muteButton = row.querySelector(".track-mute");
    const volume = row.querySelector(".track-volume");
    row.querySelector(".track-label").textContent = track.label;
    volume.value = pref.volume;
    volume.setAttribute("aria-label", `${track.label} volume`);

    const apply = (immediate) => {
      mixer.setGain(track, pref.muted ? 0 : pref.volume, immediate);
      muteButton.setAttribute("aria-pressed", String(pref.muted));
      muteButton.textContent = pref.muted ? "Unmute" : "Mute";
      row.classList.toggle("is-muted", pref.muted);
    };
    muteButton.addEventListener("click", () => {
      pref.muted = !pref.muted;
      apply();
      savePref(store, key, pref);
    });
    volume.addEventListener("input", () => {
      pref.volume = Number(volume.value);
      apply();
      savePref(store, key, pref);
    });

    apply(true);
    box.append(row);
  }
  box.hidden = false;
}
