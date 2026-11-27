import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import RoiEditor from "../RoiEditor.vue";

describe("RoiEditor", () => {
  it("does not append a duplicate vertex when finishing a vertex drag", async () => {
    const wrapper = mount(RoiEditor, {
      props: {
        modelValue: [
          { x: 0.2, y: 0.2 },
          { x: 0.8, y: 0.2 },
          { x: 0.5, y: 0.8 }
        ],
        containerWidth: 800,
        containerHeight: 450,
        videoWidth: 1920,
        videoHeight: 1080,
        editable: true
      }
    });

    const svg = wrapper.find("svg");
    const circles = wrapper.findAll("circle");
    expect(circles).toHaveLength(3);

    // Simulate dragging vertex 0: mousedown -> mousemove -> mouseup -> click on svg
    await circles[0].trigger("mousedown");
    await svg.trigger("mousemove", { clientX: 200, clientY: 120 });
    await svg.trigger("mouseup");
    await svg.trigger("click", { clientX: 200, clientY: 120 });

    const emitted = wrapper.emitted("update:modelValue") ?? [];
    expect(emitted.length).toBe(1);
    expect((emitted[0][0] as Array<{ x: number; y: number }>).length).toBe(3);
  });
});
