import { http } from "@/utils/http";
import { formatToken } from "@/utils/auth";
import { rolePermissions } from "@/utils/permissions";

export type UserResult = {
  success: boolean;
  data: {
    avatar: string;
    username: string;
    nickname: string;
    roles: Array<string>;
    permissions: Array<string>;
    accessToken: string;
    refreshToken: string;
    expires: Date;
  };
};

export type RefreshTokenResult = {
  success: boolean;
  data: {
    accessToken: string;
    refreshToken: string;
    expires: Date;
  };
};

type BackendAuthEnvelope = {
  code: number;
  message: string;
  data?: {
    access_token: string;
    refresh_token: string;
    expires_at: string;
    user: {
      id: string;
      username: string;
      display_name: string;
      role: string;
    };
  };
};

/** 登录对接 aegis-api POST /api/v1/auth/login */
export const getLogin = async (data?: object): Promise<UserResult> => {
  const res = await http.request<BackendAuthEnvelope>(
    "post",
    "/api/v1/auth/login",
    { data }
  );
  if (res?.code === 0 && res.data) {
    const role = res.data.user.role;
    return {
      success: true,
      data: {
        avatar: "",
        username: res.data.user.username,
        nickname: res.data.user.display_name || res.data.user.username,
        roles: [role],
        permissions: rolePermissions(role),
        accessToken: res.data.access_token,
        refreshToken: res.data.refresh_token,
        expires: new Date(res.data.expires_at)
      }
    };
  }
  return {
    success: false,
    data: {
      avatar: "",
      username: "",
      nickname: "",
      roles: [],
      permissions: [],
      accessToken: "",
      refreshToken: "",
      expires: new Date(0)
    }
  };
};

/** 刷新 token 对接 aegis-api POST /api/v1/auth/refresh */
export const refreshTokenApi = async (
  data?: object
): Promise<RefreshTokenResult> => {
  const res = await http.request<BackendAuthEnvelope>(
    "post",
    "/api/v1/auth/refresh",
    { data }
  );
  if (res?.code === 0 && res.data) {
    return {
      success: true,
      data: {
        accessToken: res.data.access_token,
        refreshToken: res.data.refresh_token,
        expires: new Date(res.data.expires_at)
      }
    };
  }
  return {
    success: false,
    data: {
      accessToken: "",
      refreshToken: "",
      expires: new Date(0)
    }
  };
};

/** 登出对接 aegis-api POST /api/v1/auth/logout */
export const logoutApi = (refreshToken?: string, accessToken?: string) => {
  return http.request("post", "/api/v1/auth/logout", {
    data: refreshToken ? { refresh_token: refreshToken } : {},
    headers: accessToken ? { Authorization: formatToken(accessToken) } : undefined
  });
};
