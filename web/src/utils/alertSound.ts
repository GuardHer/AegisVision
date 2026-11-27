// Synthesizes a short offline two-tone duty room alert chime using Web Audio API (zero external assets).
export function playDutyAlertChime(): void {
  if (typeof window === "undefined") return;
  const AudioCtx =
    window.AudioContext ||
    (window as unknown as { webkitAudioContext?: typeof AudioContext })
      .webkitAudioContext;
  if (!AudioCtx) return;

  try {
    const ctx = new AudioCtx();
    const now = ctx.currentTime;
    const tones = [
      { freq: 880, start: 0, dur: 0.14 },
      { freq: 1174.66, start: 0.16, dur: 0.22 }
    ];

    for (const tone of tones) {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.type = "sine";
      osc.frequency.setValueAtTime(tone.freq, now + tone.start);
      gain.gain.setValueAtTime(0.001, now + tone.start);
      gain.gain.exponentialRampToValueAtTime(0.18, now + tone.start + 0.02);
      gain.gain.exponentialRampToValueAtTime(
        0.001,
        now + tone.start + tone.dur
      );
      osc.connect(gain);
      gain.connect(ctx.destination);
      osc.start(now + tone.start);
      osc.stop(now + tone.start + tone.dur + 0.02);
    }

    setTimeout(() => {
      void ctx.close().catch(() => {});
    }, 600);
  } catch {
    // Browser autoplay policy may block audio before first user gesture.
  }
}
