<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { createStreamToken } from "@/api/aegis";
import { useRenderIcon } from "@/components/ReIcon/src/hooks";
import Close from "~icons/ep/close";
import Refresh from "~icons/ep/refresh";
import ZoomIn from "~icons/ep/zoom-in";
import Camera from "~icons/ep/camera";
import DataLine from "~icons/ep/data-line";
import PauseLine from "~icons/ri/pause-line";
import PlayLine from "~icons/ri/play-line";
import VolumeMuteLine from "~icons/ri/volume-mute-line";
import VolumeUpLine from "~icons/ri/volume-up-line";
import Focus3Line from "~icons/ri/focus-3-line";
import PictureInPicture from "~icons/ri/picture-in-picture-2-line";
import FullscreenFill from "~icons/ri/fullscreen-fill";
import FullscreenExitFill from "~icons/ri/fullscreen-exit-fill";

export type PlayerState = "connecting" | "playing" | "reconnecting" | "paused" | "error";

const props = withDefaults(
  defineProps<{
    cameraId: string;
    cameraName?: string;
    workstationCode?: string;
    stream?: "main" | "sub";
    autoConnect?: boolean;
    showOverlay?: boolean;
    controls?: "full" | "minimal" | "none";
    closable?: boolean;
    fullscreenTarget?: HTMLElement | null;
  }>(),
  {
    cameraName: "",
    workstationCode: "",
    stream: "main",
    autoConnect: true,
    showOverlay: true,
    controls: "full",
    closable: false,
    fullscreenTarget: null
  }
);

const emit = defineEmits<{
  (e: "video-resize", size: { width: number; height: number }): void;
  (e: "state-change", state: PlayerState): void;
  (e: "update:stream", stream: "main" | "sub"): void;
  (e: "update:showOverlay", visible: boolean): void;
  (e: "snapshot", video: HTMLVideoElement | null): void;
  (e: "close"): void;
  (e: "toggle-solo"): void;
}>();

const { t } = useI18n();
const rootRef = ref<HTMLElement | null>(null);
const videoRef = ref<HTMLVideoElement | null>(null);

const status = ref<PlayerState>("connecting");
const lastError = ref("");
const retryCountdown = ref(0);
const isMuted = ref(true);
const isPaused = ref(false);
const isFullscreen = ref(false);
const showStats = ref(false);
const zoom = ref(1);
const pan = reactive({ x: 0, y: 0 });
const dragging = ref(false);
let dragOrigin = { x: 0, y: 0, panX: 0, panY: 0 };

const telemetry = reactive({
  width: 0,
  height: 0,
  fps: 0,
  bitrateKbps: 0,
  rttMs: 0
});

const pipSupported = computed(
  () => typeof document !== "undefined" && Boolean((document as any).pictureInPictureEnabled)
);

let pc: RTCPeerConnection | null = null;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
let countdownTimer: ReturnType<typeof setInterval> | null = null;
let statsTimer: ReturnType<typeof setInterval> | null = null;
let prevBytes = 0;
let prevStatsTs = 0;
let backoffMs = 1000;
let unmounted = false;

const videoStyle = computed(() => {
  if (zoom.value <= 1) return {};
  return {
    transform: `translate3d(${pan.x}px, ${pan.y}px, 0) scale(${zoom.value})`,
    cursor: dragging.value ? "grabbing" : "grab"
  };
});

function setStatus(next: PlayerState) {
  status.value = next;
  emit("state-change", next);
}

function stopTimers() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  if (countdownTimer) {
    clearInterval(countdownTimer);
    countdownTimer = null;
  }
  if (statsTimer) {
    clearInterval(statsTimer);
    statsTimer = null;
  }
}

function cleanupPeer() {
  stopTimers();
  prevBytes = 0;
  prevStatsTs = 0;
  retryCountdown.value = 0;
  if (pc) {
    pc.oniceconnectionstatechange = null;
    pc.ontrack = null;
    pc.close();
    pc = null;
  }
  if (videoRef.value) {
    videoRef.value.srcObject = null;
  }
}

function startStatsPolling(peer: RTCPeerConnection) {
  if (statsTimer) clearInterval(statsTimer);
  statsTimer = setInterval(async () => {
    if (!pc || pc !== peer || unmounted) return;
    try {
      const report = await peer.getStats();
      report.forEach((stat: any) => {
        if (stat.type === "inbound-rtp" && stat.kind === "video") {
          if (typeof stat.framesPerSecond === "number") {
            telemetry.fps = Math.round(stat.framesPerSecond);
          }
          if (typeof stat.frameWidth === "number" && stat.frameWidth > 0) {
            telemetry.width = stat.frameWidth;
            telemetry.height = stat.frameHeight || telemetry.height;
          }
          if (typeof stat.bytesReceived === "number" && typeof stat.timestamp === "number") {
            if (prevStatsTs > 0 && stat.timestamp > prevStatsTs) {
              const dtSec = (stat.timestamp - prevStatsTs) / 1000;
              const deltaBytes = Math.max(0, stat.bytesReceived - prevBytes);
              telemetry.bitrateKbps = Math.round((deltaBytes * 8) / 1000 / dtSec);
            }
            prevBytes = stat.bytesReceived;
            prevStatsTs = stat.timestamp;
          }
        }
        if (
          stat.type === "candidate-pair" &&
          (stat.state === "succeeded" || stat.nominated) &&
          typeof stat.currentRoundTripTime === "number"
        ) {
          telemetry.rttMs = Math.round(stat.currentRoundTripTime * 1000);
        }
      });
    } catch {
      // Ignore transient getStats failures
    }
  }, 2000);
}

function scheduleReconnect() {
  if (unmounted) return;
  if (status.value !== "error") {
    setStatus("reconnecting");
  }
  const delay = backoffMs;
  backoffMs = Math.min(backoffMs * 2, 15000);
  retryCountdown.value = Math.max(1, Math.ceil(delay / 1000));
  if (countdownTimer) clearInterval(countdownTimer);
  countdownTimer = setInterval(() => {
    if (retryCountdown.value > 1) retryCountdown.value -= 1;
  }, 1000);
  reconnectTimer = setTimeout(() => {
    void connect();
  }, delay);
}

async function connect() {
  cleanupPeer();
  if (!props.cameraId || unmounted) return;

  isPaused.value = false;
  lastError.value = "";
  setStatus("connecting");

  try {
    const tokenResp = await createStreamToken(props.cameraId, props.stream);
    const whepUrl = tokenResp?.data?.whep_url;
    if (!whepUrl) {
      throw new Error("missing whep_url");
    }

    const peer = new RTCPeerConnection({ iceServers: [] });
    pc = peer;
    peer.addTransceiver("video", { direction: "recvonly" });

    peer.ontrack = evt => {
      if (videoRef.value && evt.streams[0]) {
        videoRef.value.srcObject = evt.streams[0];
        videoRef.value.muted = isMuted.value;
        void videoRef.value.play().catch(() => {});
      }
    };

    peer.oniceconnectionstatechange = () => {
      if (peer.iceConnectionState === "connected" || peer.iceConnectionState === "completed") {
        backoffMs = 1000;
        lastError.value = "";
        setStatus(isPaused.value ? "paused" : "playing");
        startStatsPolling(peer);
      } else if (
        peer.iceConnectionState === "failed" ||
        peer.iceConnectionState === "disconnected"
      ) {
        lastError.value = `ICE ${peer.iceConnectionState}`;
        scheduleReconnect();
      }
    };

    const offer = await peer.createOffer();
    await peer.setLocalDescription(offer);

    const resp = await fetch(whepUrl, {
      method: "POST",
      headers: { "Content-Type": "application/sdp" },
      body: offer.sdp
    });
    if (!resp.ok) {
      throw new Error(`WHEP HTTP ${resp.status}`);
    }
    const answerSdp = await resp.text();
    await peer.setRemoteDescription({ type: "answer", sdp: answerSdp });
  } catch (err: any) {
    lastError.value = err?.message || "Stream handshake failed";
    setStatus("error");
    scheduleReconnect();
  }
}

function manualReconnect() {
  backoffMs = 1000;
  void connect();
}

function togglePause() {
  const v = videoRef.value;
  if (!v) return;
  if (isPaused.value) {
    isPaused.value = false;
    void v.play().catch(() => {});
    if (status.value === "paused") setStatus("playing");
  } else {
    v.pause();
    isPaused.value = true;
    if (status.value === "playing") setStatus("paused");
  }
}

function toggleMute() {
  isMuted.value = !isMuted.value;
  if (videoRef.value) {
    videoRef.value.muted = isMuted.value;
  }
}

function toggleStream() {
  emit("update:stream", props.stream === "main" ? "sub" : "main");
}

function cycleZoom() {
  const levels = [1, 1.5, 2, 3];
  const idx = levels.indexOf(zoom.value);
  const next = levels[(idx + 1) % levels.length];
  zoom.value = next;
  if (next === 1) {
    pan.x = 0;
    pan.y = 0;
  }
}

function resetZoom() {
  zoom.value = 1;
  pan.x = 0;
  pan.y = 0;
}

function onWheel(evt: WheelEvent) {
  if (props.controls !== "full") return;
  if (!evt.ctrlKey && zoom.value === 1) return;
  evt.preventDefault();
  const delta = evt.deltaY < 0 ? 0.25 : -0.25;
  const next = Math.min(4, Math.max(1, Number((zoom.value + delta).toFixed(2))));
  zoom.value = next;
  if (next === 1) {
    pan.x = 0;
    pan.y = 0;
  }
}

function onPointerDown(evt: PointerEvent) {
  if (props.controls !== "full" || zoom.value <= 1) return;
  dragging.value = true;
  dragOrigin = { x: evt.clientX, y: evt.clientY, panX: pan.x, panY: pan.y };
  (evt.currentTarget as HTMLElement)?.setPointerCapture?.(evt.pointerId);
}

function onPointerMove(evt: PointerEvent) {
  if (!dragging.value) return;
  const maxOffset = 220 * (zoom.value - 1);
  pan.x = Math.max(-maxOffset, Math.min(maxOffset, dragOrigin.panX + (evt.clientX - dragOrigin.x)));
  pan.y = Math.max(-maxOffset, Math.min(maxOffset, dragOrigin.panY + (evt.clientY - dragOrigin.y)));
}

function onPointerUp() {
  dragging.value = false;
}

async function togglePiP() {
  const v = videoRef.value as any;
  if (!v || !pipSupported.value) return;
  try {
    if ((document as any).pictureInPictureElement) {
      await (document as any).exitPictureInPicture();
    } else if (v.requestPictureInPicture) {
      await v.requestPictureInPicture();
    }
  } catch {
    // Ignore browser PiP refusal when video has no active frames
  }
}

function toggleFullscreen() {
  const target = props.fullscreenTarget || rootRef.value;
  if (!target) return;
  if (document.fullscreenElement) {
    void document.exitFullscreen();
  } else {
    void target.requestFullscreen?.();
  }
}

function handleFullscreenChange() {
  const target = props.fullscreenTarget || rootRef.value;
  isFullscreen.value = Boolean(document.fullscreenElement && document.fullscreenElement === target);
}

function onLoadedMetadata() {
  if (videoRef.value) {
    telemetry.width = videoRef.value.videoWidth;
    telemetry.height = videoRef.value.videoHeight;
    emit("video-resize", {
      width: videoRef.value.videoWidth,
      height: videoRef.value.videoHeight
    });
  }
}

function onDoubleClick() {
  if (props.controls === "full") {
    emit("toggle-solo");
  }
}

watch(
  () => [props.cameraId, props.stream],
  () => {
    backoffMs = 1000;
    resetZoom();
    if (props.autoConnect) {
      void connect();
    }
  }
);

onMounted(() => {
  document.addEventListener("fullscreenchange", handleFullscreenChange);
  if (props.autoConnect && props.cameraId) {
    void connect();
  }
});

onBeforeUnmount(() => {
  unmounted = true;
  document.removeEventListener("fullscreenchange", handleFullscreenChange);
  cleanupPeer();
});

defineExpose({
  connect: manualReconnect,
  togglePause,
  resetZoom,
  videoRef
});
</script>

<template>
  <div
    ref="rootRef"
    class="aegis-whep-player"
    @wheel="onWheel"
    @pointerdown="onPointerDown"
    @pointermove="onPointerMove"
    @pointerup="onPointerUp"
    @pointercancel="onPointerUp"
    @dblclick="onDoubleClick"
  >
    <video
      ref="videoRef"
      class="aegis-whep-video"
      :style="videoStyle"
      autoplay
      :muted="isMuted"
      playsinline
      @loadedmetadata="onLoadedMetadata"
      @resize="onLoadedMetadata"
    />

    <!-- Optional overlay slot inside player stage (kept below HUD controls) -->
    <slot name="overlay" />

    <!-- Top OSD Bar: Camera Identity + Status Dot + Quick Stream Pill -->
    <div v-if="controls !== 'none'" class="aegis-whep-top">
      <div class="aegis-whep-identity">
        <span
          class="aegis-whep-dot"
          :class="{
            'is-live': status === 'playing',
            'is-paused': status === 'paused',
            'is-warn': status === 'connecting' || status === 'reconnecting',
            'is-err': status === 'error'
          }"
          aria-hidden="true"
        />
        <span class="aegis-whep-name" :title="cameraName || cameraId">
          {{ cameraName || cameraId }}
        </span>
        <span v-if="workstationCode" class="aegis-whep-code">
          {{ workstationCode }}
        </span>
        <span v-if="status === 'playing'" class="aegis-whep-live-tag">
          {{ t("aegis.player.live") }}
        </span>
        <span v-else-if="status === 'paused'" class="aegis-whep-paused-tag">
          {{ t("aegis.player.paused") }}
        </span>
        <span v-if="zoom > 1" class="aegis-whep-zoom-tag">
          {{ zoom.toFixed(1) }}×
        </span>
      </div>

      <div class="aegis-whep-top-actions">
        <el-button
          v-if="closable"
          size="small"
          circle
          class="aegis-hud-btn"
          :icon="useRenderIcon(Close)"
          :title="t('aegis.player.closeSlot')"
          data-test="player-close"
          @click.stop="emit('close')"
        />
      </div>
    </div>

    <!-- Live Stream Telemetry HUD (WebRTC getStats) -->
    <div
      v-if="showStats && controls !== 'none'"
      class="aegis-whep-stats aegis-tabular"
      data-test="player-stats-hud"
    >
      <div><span>{{ t("aegis.player.resolution") }}</span><strong>{{ telemetry.width ? `${telemetry.width}×${telemetry.height}` : "—" }}</strong></div>
      <div><span>{{ t("aegis.player.fps") }}</span><strong>{{ telemetry.fps || "—" }}</strong></div>
      <div><span>{{ t("aegis.player.bitrate") }}</span><strong>{{ telemetry.bitrateKbps ? `${telemetry.bitrateKbps} kbps` : "—" }}</strong></div>
      <div><span>{{ t("aegis.player.latency") }}</span><strong>{{ telemetry.rttMs ? `${telemetry.rttMs} ms` : "—" }}</strong></div>
    </div>

    <!-- Non-Playing / Diagnostic Overlay -->
    <div
      v-if="status === 'connecting' || status === 'reconnecting' || status === 'error'"
      class="aegis-whep-state-mask"
      role="status"
      aria-live="polite"
    >
      <div class="aegis-whep-state-card">
        <div v-if="status !== 'error'" class="aegis-whep-spinner" aria-hidden="true" />
        <svg v-else aria-hidden="true" viewBox="0 0 24 24" class="w-6 h-6 text-red-400">
          <path fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" d="M12 9v4m0 4h.01M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z" />
        </svg>
        <div class="text-xs font-medium text-white">
          {{
            status === "connecting"
              ? t("aegis.player.connecting")
              : status === "reconnecting"
                ? `${t("aegis.player.reconnecting")} (${retryCountdown}s)`
                : t("aegis.player.errorTitle")
          }}
        </div>
        <div v-if="lastError" class="text-[11px] text-slate-300 max-w-[220px] truncate" :title="lastError">
          {{ lastError }}
        </div>
        <el-button
          v-if="status === 'error' || status === 'reconnecting'"
          type="primary"
          size="small"
          :icon="useRenderIcon(Refresh)"
          data-test="player-reconnect"
          @click.stop="manualReconnect"
        >
          {{ t("aegis.player.retryNow") }}
        </el-button>
      </div>
    </div>

    <!-- Bottom Interactive Control Bar -->
    <div
      v-if="controls !== 'none'"
      class="aegis-whep-controls"
      @click.stop
      @dblclick.stop
    >
      <div class="aegis-whep-controls__group">
        <!-- Pause / Resume -->
        <el-button
          size="small"
          circle
          class="aegis-hud-btn"
          :icon="useRenderIcon(isPaused ? PlayLine : PauseLine)"
          :title="isPaused ? t('aegis.player.resume') : t('aegis.player.pause')"
          data-test="player-pause"
          @click="togglePause"
        />

        <!-- Mute / Unmute -->
        <el-button
          v-if="controls === 'full'"
          size="small"
          circle
          class="aegis-hud-btn"
          :icon="useRenderIcon(isMuted ? VolumeMuteLine : VolumeUpLine)"
          :title="isMuted ? t('aegis.player.unmute') : t('aegis.player.mute')"
          data-test="player-mute"
          @click="toggleMute"
        />

        <!-- Manual Reconnect -->
        <el-button
          size="small"
          circle
          class="aegis-hud-btn"
          :icon="useRenderIcon(Refresh)"
          :title="t('aegis.player.retryNow')"
          @click="manualReconnect"
        />

        <!-- Stream Quality Switch (HD / SD) -->
        <el-button
          v-if="controls === 'full'"
          size="small"
          round
          class="aegis-hud-btn aegis-hud-pill"
          :title="stream === 'main' ? t('aegis.player.mainStream') : t('aegis.player.subStream')"
          data-test="player-stream-toggle"
          @click="toggleStream"
        >
          {{ stream === "main" ? "HD" : "SD" }}
        </el-button>
      </div>

      <div class="aegis-whep-controls__group">
        <!-- Digital Zoom -->
        <el-button
          v-if="controls === 'full'"
          size="small"
          circle
          class="aegis-hud-btn aegis-hud-secondary"
          :class="{ 'is-active': zoom > 1 }"
          :icon="useRenderIcon(ZoomIn)"
          :title="t('aegis.player.zoomIn')"
          data-test="player-zoom"
          @click="cycleZoom"
        />

        <el-button
          v-if="controls === 'full' && zoom > 1"
          size="small"
          round
          class="aegis-hud-btn aegis-hud-pill"
          :title="t('aegis.player.zoomReset')"
          @click="resetZoom"
        >
          1×
        </el-button>

        <!-- AI Overlay Toggle -->
        <el-button
          v-if="controls === 'full'"
          size="small"
          circle
          class="aegis-hud-btn"
          :class="{ 'is-active': showOverlay }"
          :icon="useRenderIcon(Focus3Line)"
          :title="t('aegis.player.overlayToggle')"
          data-test="player-overlay-toggle"
          @click="emit('update:showOverlay', !showOverlay)"
        />

        <!-- Snapshot -->
        <el-button
          v-if="controls === 'full'"
          size="small"
          circle
          class="aegis-hud-btn"
          :icon="useRenderIcon(Camera)"
          :title="t('aegis.player.snapshot')"
          data-test="player-snapshot"
          @click="emit('snapshot', videoRef)"
        />

        <!-- Stream Stats HUD Toggle -->
        <el-button
          size="small"
          circle
          class="aegis-hud-btn aegis-hud-secondary"
          :class="{ 'is-active': showStats }"
          :icon="useRenderIcon(DataLine)"
          :title="t('aegis.player.statsHud')"
          data-test="player-stats-toggle"
          @click="showStats = !showStats"
        />

        <!-- Picture-in-Picture -->
        <el-button
          v-if="controls === 'full' && pipSupported"
          size="small"
          circle
          class="aegis-hud-btn aegis-hud-secondary"
          :icon="useRenderIcon(PictureInPicture)"
          :title="t('aegis.player.pip')"
          @click="togglePiP"
        />

        <!-- Fullscreen -->
        <el-button
          v-if="controls === 'full'"
          size="small"
          circle
          class="aegis-hud-btn"
          :icon="useRenderIcon(isFullscreen ? FullscreenExitFill : FullscreenFill)"
          :title="isFullscreen ? t('aegis.player.exitFullscreen') : t('aegis.player.fullscreen')"
          data-test="player-fullscreen"
          @click="toggleFullscreen"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.aegis-whep-player {
  container: player / inline-size;
  position: relative;
  inline-size: 100%;
  block-size: 100%;
  background: #090d16;
  overflow: clip;
  user-select: none;
}

.aegis-whep-video {
  display: block;
  inline-size: 100%;
  block-size: 100%;
  object-fit: contain;
  transition: transform 140ms ease-out;
  transform-origin: center center;
}

.aegis-whep-top {
  position: absolute;
  inset-block-start: 0;
  inset-inline: 0;
  z-index: 20;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 6px 8px;
  background: linear-gradient(180deg, rgb(2 6 23 / 78%) 0%, rgb(2 6 23 / 0%) 100%);
  color: #f8fafc;
  pointer-events: none;
}

.aegis-whep-identity {
  display: flex;
  align-items: center;
  gap: 6px;
  min-inline-size: 0;
}

.aegis-whep-dot {
  inline-size: 7px;
  block-size: 7px;
  border-radius: 9999px;
  flex-shrink: 0;
  background: #94a3b8;
}

.aegis-whep-dot.is-live {
  background: #22c55e;
  box-shadow: 0 0 0 3px rgb(34 197 94 / 22%);
}

.aegis-whep-dot.is-paused {
  background: #38bdf8;
}

.aegis-whep-dot.is-warn {
  background: #f59e0b;
}

.aegis-whep-dot.is-err {
  background: #ef4444;
}

.aegis-whep-name {
  font-size: 12px;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.aegis-whep-code {
  font-size: 10.5px;
  padding: 1px 5px;
  border-radius: 4px;
  background: rgb(255 255 255 / 14%);
  color: #e2e8f0;
  flex-shrink: 0;
}

.aegis-whep-live-tag,
.aegis-whep-paused-tag,
.aegis-whep-zoom-tag {
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.03em;
  padding: 1px 5px;
  border-radius: 4px;
  flex-shrink: 0;
}

.aegis-whep-live-tag {
  background: rgb(22 163 74 / 82%);
  color: #fff;
}

.aegis-whep-paused-tag {
  background: rgb(2 132 199 / 85%);
  color: #fff;
}

.aegis-whep-zoom-tag {
  background: rgb(217 119 6 / 88%);
  color: #fff;
  font-variant-numeric: tabular-nums;
}

.aegis-whep-top-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  pointer-events: auto;
  opacity: 0;
  transition: opacity 160ms ease;
}

.aegis-whep-player:hover .aegis-whep-top-actions,
.aegis-whep-player:focus-within .aegis-whep-top-actions {
  opacity: 1;
}

.aegis-whep-stats {
  position: absolute;
  inset-block-start: 34px;
  inset-inline-start: 8px;
  z-index: 22;
  display: grid;
  grid-template-columns: repeat(2, auto);
  gap: 4px 12px;
  padding: 6px 10px;
  border-radius: 6px;
  background: rgb(15 23 42 / 84%);
  border: 1px solid rgb(255 255 255 / 14%);
  color: #f8fafc;
  font-size: 11px;
  pointer-events: none;
}

.aegis-whep-stats div {
  display: flex;
  gap: 6px;
  align-items: baseline;
}

.aegis-whep-stats span {
  color: #94a3b8;
}

.aegis-whep-state-mask {
  position: absolute;
  inset: 0;
  z-index: 18;
  display: grid;
  place-items: center;
  background: rgb(9 13 22 / 72%);
  padding: 12px;
}

.aegis-whep-state-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  text-align: center;
}

.aegis-whep-spinner {
  inline-size: 22px;
  block-size: 22px;
  border-radius: 9999px;
  border: 2px solid rgb(255 255 255 / 22%);
  border-top-color: #38bdf8;
  animation: aegis-spin 0.8s linear infinite;
}

@keyframes aegis-spin {
  to {
    transform: rotate(360deg);
  }
}

.aegis-whep-controls {
  position: absolute;
  inset-block-end: 0;
  inset-inline: 0;
  z-index: 25;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  padding: 16px 8px 6px;
  background: linear-gradient(0deg, rgb(2 6 23 / 86%) 0%, rgb(2 6 23 / 0%) 100%);
  opacity: 0;
  transition: opacity 160ms ease;
}

.aegis-whep-player:hover .aegis-whep-controls,
.aegis-whep-player:focus-within .aegis-whep-controls {
  opacity: 1;
}

.aegis-whep-controls__group {
  display: flex;
  align-items: center;
  gap: 4px;
}

/* Element Plus buttons themed for dark video HUD (keeps pure-admin el-button semantics) */
.aegis-hud-btn.el-button {
  --el-button-text-color: #f1f5f9;
  --el-button-bg-color: rgb(15 23 42 / 55%);
  --el-button-border-color: transparent;
  --el-button-hover-text-color: #fff;
  --el-button-hover-bg-color: rgb(255 255 255 / 22%);
  --el-button-hover-border-color: transparent;
  --el-button-active-text-color: #fff;
  --el-button-active-bg-color: rgb(255 255 255 / 30%);
  --el-button-active-border-color: transparent;

  margin-left: 0;
}

.aegis-hud-btn.el-button.is-active {
  --el-button-bg-color: var(--el-color-primary);
  --el-button-hover-bg-color: var(--el-color-primary-light-3);
  --el-button-active-bg-color: var(--el-color-primary-dark-2);
}

.aegis-hud-pill.el-button {
  padding-inline: 8px;
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

/* Compact mode for 9-grid / 16-grid narrow tiles */
@container player (inline-size < 320px) {
  .aegis-whep-code,
  .aegis-whep-live-tag,
  .aegis-hud-secondary {
    display: none;
  }

  .aegis-whep-controls {
    padding: 12px 5px 4px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .aegis-whep-video,
  .aegis-whep-controls,
  .aegis-whep-top-actions {
    transition: none;
  }
}
</style>
