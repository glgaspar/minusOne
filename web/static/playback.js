// Shared by the queue player (player.js) and the practice page (practice.js):
// the video, the mixer UI and its saved settings.
//
// Video: a muted YouTube embed. The audio is the clock; the video follows it
// and is re-seeked when it drifts.

const SYNC_TOLERANCE_S = 0.3; // video drift allowed before re-seeking
const SYNC_COOLDOWN_MS = 2000; // time a re-seek gets to settle before the next one

const $ = (id) => document.getElementById(id);

// formatTime shows m:ss, or m:ss.t with tenths.
function formatTime(seconds, tenths = false) {
  const s = Math.floor(seconds);
  const text = `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
  return tenths ? `${text}.${Math.floor((seconds - s) * 10)}` : text;
}

// ---------------------------------------------------------------- video

let yt = null;
let ytReady = false;
let videoAvailable = true;
let lastVideoSeek = 0;
let cuedVideoId = null; // cued before the API was ready
let videoRate = 1;

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

// syncVideo keeps the video at the audio's time. rate is the audio's speed:
// the video plays at the closest one YouTube offers, and re-seeks cover the rest.
function syncVideo(audioTime, playing, rate = 1) {
  if (!ytReady || !videoAvailable) return;
  if (rate !== videoRate) {
    const rates = yt.getAvailablePlaybackRates?.() ?? [1];
    const closest = rates.reduce((a, b) => (Math.abs(b - rate) < Math.abs(a - rate) ? b : a), 1);
    yt.setPlaybackRate(closest);
    videoRate = rate;
  }
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

// backingOf says which way of playing the band a track is part of: the
// original, the instrumental, or "stems" for the instruments. Playing two ways
// at once doubles the band (web/player.go picks which one starts unmuted).
function backingOf(type) {
  return ["drums", "bass", "guitar", "piano", "other"].includes(type) ? "stems" : type;
}

// A preference depends on what else the song has: the original is the only
// sound in a song that isn't split, and doubles the band in one that is. So
// it's saved per track type and per set of backings the song has.
function prefKey(track, shape) {
  return `${track.type}:${shape}`;
}

function songShape(song) {
  const ways = song.tracks.map((t) => backingOf(t.type)).filter((b) => b !== "vocals");
  return [...new Set(ways)].sort().join("+");
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

// buildMixer shows a row per track with its volume and mute, and with
// opts.solo a solo button: while any track is soloed, only soloed tracks play.
// mixer is anything with tracks ({type, label, muted}) and
// setGain(track, value, immediate). opts.onChange runs after every change.
//
// It returns controls for presets: setMuted(type, muted) for several tracks,
// then commit() to apply and save them.
function buildMixer(song, mixer, store, opts = {}) {
  const box = $("mixer");
  box.replaceChildren();
  const prefs = loadPrefs(store);
  const shape = songShape(song);
  const rows = [];
  const anySolo = () => rows.some((r) => r.soloed);
  const applyAll = (immediate) => {
    for (const r of rows) r.apply(immediate);
    opts.onChange?.();
  };

  for (const track of mixer.tracks) {
    const key = prefKey(track, shape);
    const pref = { volume: 1, muted: track.muted, ...prefs[key] };

    const el = $("track-template").content.firstElementChild.cloneNode(true);
    const muteButton = el.querySelector(".track-mute");
    const soloButton = el.querySelector(".track-solo");
    const volume = el.querySelector(".track-volume");
    el.querySelector(".track-label").textContent = track.label;
    soloButton.hidden = !opts.solo;
    volume.value = pref.volume;
    volume.setAttribute("aria-label", `${track.label} volume`);

    const row = { type: track.type, pref, soloed: false };
    row.apply = (immediate) => {
      const silent = pref.muted || (anySolo() && !row.soloed);
      mixer.setGain(track, silent ? 0 : pref.volume, immediate);
      muteButton.setAttribute("aria-pressed", String(pref.muted));
      muteButton.textContent = pref.muted ? "Unmute" : "Mute";
      soloButton.setAttribute("aria-pressed", String(row.soloed));
      el.classList.toggle("is-muted", silent);
    };
    row.save = () => savePref(store, key, pref);
    muteButton.addEventListener("click", () => {
      pref.muted = !pref.muted;
      applyAll();
      row.save();
    });
    soloButton.addEventListener("click", () => {
      row.soloed = !row.soloed;
      applyAll();
    });
    volume.addEventListener("input", () => {
      pref.volume = Number(volume.value);
      row.apply();
      row.save();
    });

    rows.push(row);
    box.append(el);
  }
  applyAll(true);
  box.hidden = false;

  return {
    rows,
    setMuted(type, muted) {
      for (const r of rows) if (r.type === type) r.pref.muted = muted;
    },
    clearSolo() {
      for (const r of rows) r.soloed = false;
    },
    commit() {
      for (const r of rows) r.save();
      applyAll();
    },
  };
}
