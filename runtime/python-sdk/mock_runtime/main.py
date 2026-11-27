"""Mock Python Runtime simulating anomaly, count_threshold, and absence scenarios."""

from __future__ import annotations

import logging
import os
import struct
import time

from aegis_runtime_sdk import BaseAlgorithm, Detection, FrameOutput, TaskContext, serve

# Minimal valid 1x1 JPEG bytes for mock abnormal snapshots
_MOCK_JPEG = bytes([
    0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01,
    0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xDB, 0x00, 0x43,
    0x00, 0x08, 0x06, 0x06, 0x07, 0x06, 0x05, 0x08, 0x07, 0x07, 0x07, 0x09,
    0x09, 0x08, 0x0A, 0x0C, 0x14, 0x0D, 0x0C, 0x0B, 0x0B, 0x0C, 0x19, 0x12,
    0x13, 0x0F, 0x14, 0x1D, 0x1A, 0x1F, 0x1E, 0x1D, 0x1A, 0x1C, 0x1C, 0x20,
    0x24, 0x2E, 0x27, 0x20, 0x22, 0x2C, 0x23, 0x1C, 0x1C, 0x28, 0x37, 0x29,
    0x2C, 0x30, 0x31, 0x34, 0x34, 0x34, 0x1F, 0x27, 0x39, 0x3D, 0x38, 0x32,
    0x3C, 0x2E, 0x33, 0x34, 0x32, 0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x01,
    0x00, 0x01, 0x01, 0x01, 0x11, 0x00, 0xFF, 0xC4, 0x00, 0x14, 0x00, 0x01,
    0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
    0x00, 0x00, 0x00, 0x09, 0xFF, 0xC4, 0x00, 0x14, 0x10, 0x01, 0x00, 0x00,
    0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
    0x00, 0x00, 0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00,
    0xD2, 0xCF, 0x20, 0xFF, 0xD9,
])


class MockAlgorithm(BaseAlgorithm):
    """Configurable mock detector driven by task algo_params['mock_mode'] or AEGIS_MOCK_MODE."""

    def __init__(self) -> None:
        self._task_steps: dict[str, int] = {}

    def load(self) -> None:
        logging.info("MockAlgorithm loaded")

    def on_task_start(self, task_ctx: TaskContext) -> None:
        self._task_steps[task_ctx.task_id] = 0

    def on_task_stop(self, task_ctx: TaskContext) -> None:
        self._task_steps.pop(task_ctx.task_id, None)

    def infer_task(self, task_ctx: TaskContext) -> FrameOutput:
        step = self._task_steps.get(task_ctx.task_id, 0) + 1
        self._task_steps[task_ctx.task_id] = step
        mode = str(task_ctx.algo_params.get("mock_mode") or os.getenv("AEGIS_MOCK_MODE", "anomaly"))

        if mode == "absence":
            # Emit empty frames -> RuntimeServer sends periodic is_heartbeat=True frames.
            return FrameOutput(detections=[], frame_width=1920, frame_height=1080)

        if mode == "sop":
            script = str(task_ctx.algo_params.get("sop_script") or os.getenv("AEGIS_SOP_SCRIPT", "compliant"))
            phase = step % 6
            if script == "out_of_order":
                # Jump straight to step 2 (scan_barcode) skipping required step 1 (wear_esd_strap)
                return FrameOutput(
                    detections=[
                        Detection(bbox=(0.3, 0.25, 0.6, 0.75), label="scan_barcode", confidence=0.95, track_id="op-1", is_abnormal=False)
                    ],
                    frame_width=1920,
                    frame_height=1080,
                )
            if script == "timeout":
                # Perform step 1 on first two frames, then emit empty frames to trigger step_timeout
                if step <= 2:
                    return FrameOutput(
                        detections=[
                            Detection(bbox=(0.2, 0.25, 0.45, 0.75), label="wear_esd_strap", confidence=0.95, track_id="op-1", is_abnormal=False)
                        ],
                        frame_width=1920,
                        frame_height=1080,
                    )
                return FrameOutput(detections=[], frame_width=1920, frame_height=1080)
            # Default "compliant": step 1 (2 frames) -> step 2 (1 frame, matching default min_dwell_frames=1) -> idle pause (3 frames)
            if phase in (1, 2):
                label = "wear_esd_strap"
            elif phase == 3:
                label = "scan_barcode"
            else:
                return FrameOutput(detections=[], frame_width=1920, frame_height=1080)
            return FrameOutput(
                detections=[
                    Detection(bbox=(0.25, 0.25, 0.55, 0.75), label=label, confidence=0.95, track_id="op-1", is_abnormal=False)
                ],
                frame_width=1920,
                frame_height=1080,
            )

        if mode == "count":
            count = int(task_ctx.algo_params.get("mock_count", 3))
            dets = []
            for idx in range(count):
                x1 = min(0.8, 0.1 + idx * 0.18)
                dets.append(
                    Detection(
                        bbox=(x1, 0.2, x1 + 0.12, 0.75),
                        label="person",
                        confidence=0.92,
                        track_id=f"p-{idx + 1}",
                        is_abnormal=False,
                    )
                )
            return FrameOutput(detections=dets, frame_width=1920, frame_height=1080)

        # Default "anomaly" mode: emit abnormal no_helmet detection + valid JPEG snapshot.
        offset = (step % 5) * 0.02
        det = Detection(
            bbox=(0.2 + offset, 0.2, 0.45 + offset, 0.8),
            label=str(task_ctx.algo_params.get("mock_label", "no_helmet")),
            confidence=0.94,
            track_id="trk-1",
            is_abnormal=True,
            attributes={"mock_ts": str(int(time.time()))},
        )
        return FrameOutput(
            detections=[det],
            frame_width=1920,
            frame_height=1080,
            snapshot_jpeg=_MOCK_JPEG + struct.pack(">I", step)[:0],
        )


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    serve(MockAlgorithm(), runtime_key=os.getenv("AEGIS_RUNTIME_KEY", "py-mock-v1"))


if __name__ == "__main__":
    main()
