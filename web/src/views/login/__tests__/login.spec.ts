import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import LoginPage from "../index.vue";

const mockLoginByUsername = vi.fn();
const mockMessage = vi.fn();
const mockPush = vi.fn().mockResolvedValue(undefined);

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (k: string) => k })
}));

vi.mock("vue-router", () => ({
  useRouter: () => ({ push: mockPush })
}));

vi.mock("@/utils/message", () => ({
  message: (...args: any[]) => mockMessage(...args)
}));

vi.mock("@/plugins/i18n", () => ({
  $t: (k: string) => k,
  transformI18n: (k: string) => k
}));

vi.mock("@/layout/hooks/useLayout", () => ({
  useLayout: () => ({ initStorage: vi.fn() })
}));

vi.mock("@/layout/hooks/useNav", () => ({
  useNav: () => ({
    title: "AegisVision",
    getDropdownItemStyle: () => ({}),
    getDropdownItemClass: () => ""
  })
}));

vi.mock("@/layout/hooks/useTranslationLang", () => ({
  useTranslationLang: () => ({
    locale: "zh",
    translationCh: vi.fn(),
    translationEn: vi.fn()
  })
}));

vi.mock("@/layout/hooks/useDataThemeChange", () => ({
  useDataThemeChange: () => ({
    dataTheme: false,
    overallStyle: { value: "light" },
    dataThemeChange: vi.fn()
  })
}));

vi.mock("@/router/utils", () => ({
  initRouter: vi.fn().mockResolvedValue(undefined),
  getTopMenu: () => ({ path: "/dashboard/index" })
}));

vi.mock("@/store/modules/user", () => ({
  useUserStoreHook: () => ({
    loginByUsername: (...args: any[]) => mockLoginByUsername(...args)
  })
}));

vi.mock("@/assets/svg/day.svg?component", () => ({
  default: { template: "<span />" }
}));
vi.mock("@/assets/svg/dark.svg?component", () => ({
  default: { template: "<span />" }
}));
vi.mock("@/assets/svg/globalization.svg?component", () => ({
  default: { template: "<span />" }
}));
vi.mock("../utils/static", () => ({
  bg: "",
  avatar: { template: "<div />" },
  illustration: { template: "<div />" }
}));

const mountLogin = () =>
  mount(LoginPage, {
    global: {
      stubs: {
        Motion: { template: "<div><slot /></div>" },
        avatar: { template: "<div />" },
        dayIcon: { template: "<span />" },
        darkIcon: { template: "<span />" },
        globalization: { template: "<span />" },
        IconifyIconOffline: { template: "<span />" },
        "el-switch": { template: "<input type='checkbox' />" },
        "el-dropdown": { template: "<div><slot /><slot name='dropdown' /></div>" },
        "el-dropdown-menu": { template: "<div><slot /></div>" },
        "el-dropdown-item": { template: "<div><slot /></div>" },
        "el-form": {
          template: "<form><slot /></form>",
          methods: {
            validate: () => Promise.resolve(true)
          }
        },
        "el-form-item": { template: "<div><slot /></div>" },
        "el-input": {
          props: ["modelValue"],
          emits: ["update:modelValue"],
          template:
            "<input :value='modelValue' @input=\"$emit('update:modelValue', $event.target.value)\" />"
        },
        "el-alert": {
          props: ["title", "type"],
          emits: ["close"],
          template: "<div class='mock-alert' :data-type='type'>{{ title }}</div>"
        },
        "el-button": {
          template: "<button v-bind='$attrs'><slot /></button>"
        }
      }
    }
  });

describe("LoginPage error feedback", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("displays inline el-alert and error toast when backend returns 401 invalid credentials", async () => {
    mockLoginByUsername.mockRejectedValueOnce({
      message: "Request failed with status code 401",
      response: {
        status: 401,
        data: { code: 40100, message: "invalid username or password" }
      }
    });

    const wrapper = mountLogin();
    await wrapper.get('[data-test="login-submit-btn"]').trigger("click");
    await flushPromises();

    const alert = wrapper.get('[data-test="login-error-alert"]');
    expect(alert.text()).toBe("login.invalidCredentials");
    expect(mockMessage).toHaveBeenCalledWith(
      "login.invalidCredentials",
      expect.objectContaining({ type: "error", showClose: true })
    );
    expect((wrapper.vm as any).loading).toBe(false);
  });

  it("displays account disabled error on 403 and clears alert when editing credentials", async () => {
    mockLoginByUsername.mockRejectedValueOnce({
      message: "Request failed with status code 403",
      response: {
        status: 403,
        data: { code: 40300, message: "user account is disabled" }
      }
    });

    const wrapper = mountLogin();
    await wrapper.get('[data-test="login-submit-btn"]').trigger("click");
    await flushPromises();

    expect(wrapper.get('[data-test="login-error-alert"]').text()).toBe(
      "login.accountDisabled"
    );

    // Editing password clears the inline error alert
    (wrapper.vm as any).ruleForm.password = "newpassword";
    await flushPromises();
    expect(wrapper.find('[data-test="login-error-alert"]').exists()).toBe(false);
  });
});
