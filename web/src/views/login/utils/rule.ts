import { reactive } from "vue";
import type { FormRules } from "element-plus";
import { $t, transformI18n } from "@/plugins/i18n";

/** 密码正则（用于参考校验，不阻断已有任意长度密码登录） */
export const REGEXP_PWD =
  /^(?![0-9]+$)(?![a-z]+$)(?![A-Z]+$)(?!([^(0-9a-zA-Z)]|[()])+$)(?!^.*[\u4E00-\u9FA5].*$)([^(0-9a-zA-Z)]|[()]|[a-z]|[A-Z]|[0-9]){8,18}$/;

/** 登录校验：仅校验账号与密码非空，具体凭据校验交由后端认证接口返回明确错误 */
const loginRules = reactive<FormRules>({
  password: [
    {
      validator: (_rule, value, callback) => {
        if (!value || !String(value).trim()) {
          callback(new Error(transformI18n($t("login.purePassWordReg"))));
        } else {
          callback();
        }
      },
      trigger: "blur"
    }
  ]
});

export { loginRules };
