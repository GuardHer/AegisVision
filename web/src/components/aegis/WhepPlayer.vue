<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { createStreamToken } from "@/api/aegis";

const props = withDefaults(
  defineProps<{
    cameraId: string;
    stream?: "main" | "sub";
    autoConnect?: boolean;
  }>(),
  {
    stream: "main",
    autoConnect: true
  }
);

const emit = defineEmits<{
  (e: "video-resize", size: { width: number; height: number }): void;
  (e: "state-change", state: "connecting" | "playing" | "reconnecting" | "error"): void;
}>();

const videoRef = ref<HTMLVideoElement | null>(null);
const status = ref<"connecting" | "playing" | "reconnecting" | "error">("connecting");

let pc: RTCPeerConnection | null = null;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
let backoffMs = 1000;
let unmounted = false;

function setStatus(next: "connecting" | "playing" | "reconnecting" | "error") {
  status.value = next;
  emit("state-change", next);
}

function cleanupPeer() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
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

function scheduleReconnect() {
  if (unmounted) return;
  setStatus("reconnecting");
  const delay = backoffMs;
  backoffMs = Math.min(backoffMs * 2, 15000);
  reconnectTimer = setTimeout(() => {
    void connect();
  }, delay);
}

async function connect() {
  cleanupPeer();
  if (!props.cameraId || unmounted) return;

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
        void videoRef.value.play().catch(() => {});
      }
    };

    peer.oniceconnectionstatechange = () => {
      if (peer.iceConnectionState === "connected" || peer.iceConnectionState === "completed") {
        backoffMs = 1000;
        setStatus("playing");
      } else if (
        peer.iceConnectionState === "failed" ||
        peer.iceConnectionState === "disconnected"
      ) {
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
      throw new Error(`WHEP handshake HTTP ${resp.status}`);
    }
    const answerSdp = await resp.text();
    await peer.setRemoteDescription({ type: "answer", sdp: answerSdp });
  } catch {
    setStatus("error");
    scheduleReconnect();
  }
}

function onLoadedMetadata() {
  if (videoRef.value) {
    emit("video-resize", {
      width: videoRef.value.videoWidth,
      height: videoRef.value.videoHeight
    });
  }
}

watch(
  () => [props.cameraId, props.stream],
  () => {
    backoffMs = 1000;
    if (props.autoConnect) {
      void connect();
    }
  }
);

onMounted(() => {
  if (props.autoConnect && props.cameraId) {
    void connect();
  }
});

onBeforeUnmount(() => {
  unmounted = true;
  cleanupPeer();
});
</script>

<template>
  <div class="aegis-whep-player relative w-full h-full bg-black overflow-hidden">
    <video
      ref="videoRef"
      class="w-full h-full object-contain block"
      autoplay
      muted
      playsinline
      @loadedmetadata="onLoadedMetadata"
      @resize="onLoadedMetadata"
    />
    <div
      v-if="status !== 'playing'"
      class="absolute inset-0 flex items-center justify-center bg-black/50 text-white text-xs pointer-events-none"
    >
      <span>{{ status }}</span>
    </div>
  </div>
</template>
