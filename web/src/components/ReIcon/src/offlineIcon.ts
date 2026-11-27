// 这里存放本地图标，在 src/layout/index.vue 文件中加载，避免在首启动加载
import { getSvgInfo } from "@pureadmin/utils";
import { addIcon } from "@iconify/vue/dist/offline";

// Element Plus Icons (bundled offline via unplugin-icons, zero external CDN)
import EpHomeFilled from "~icons/ep/home-filled?raw";
import EpDataAnalysis from "~icons/ep/data-analysis?raw";
import EpMonitor from "~icons/ep/monitor?raw";
import EpVideoCamera from "~icons/ep/video-camera?raw";
import EpBell from "~icons/ep/bell?raw";
import EpCamera from "~icons/ep/camera?raw";
import EpCalendar from "~icons/ep/calendar?raw";
import EpCpu from "~icons/ep/cpu?raw";
import EpFolderOpened from "~icons/ep/folder-opened?raw";
import EpConnection from "~icons/ep/connection?raw";
import EpUser from "~icons/ep/user?raw";
import EpDocument from "~icons/ep/document?raw";
import EpSetting from "~icons/ep/setting?raw";
import EpHistogram from "~icons/ep/histogram?raw";
import EpOdometer from "~icons/ep/odometer?raw";
import EpOperation from "~icons/ep/operation?raw";
import EpLock from "~icons/ep/lock?raw";

// Remix Icons
import RiSearchLine from "~icons/ri/search-line?raw";
import RiInformationLine from "~icons/ri/information-line?raw";

const icons = [
  ["ep/home-filled", EpHomeFilled],
  ["ep/data-analysis", EpDataAnalysis],
  ["ep/monitor", EpMonitor],
  ["ep/video-camera", EpVideoCamera],
  ["ep/bell", EpBell],
  ["ep/camera", EpCamera],
  ["ep/calendar", EpCalendar],
  ["ep/cpu", EpCpu],
  ["ep/folder-opened", EpFolderOpened],
  ["ep/connection", EpConnection],
  ["ep/user", EpUser],
  ["ep/document", EpDocument],
  ["ep/setting", EpSetting],
  ["ep/histogram", EpHistogram],
  ["ep/odometer", EpOdometer],
  ["ep/operation", EpOperation],
  ["ep/lock", EpLock],
  ["ri/search-line", RiSearchLine],
  ["ri/information-line", RiInformationLine]
];

icons.forEach(([name, icon]) => {
  addIcon(name as string, getSvgInfo(icon as string));
});
