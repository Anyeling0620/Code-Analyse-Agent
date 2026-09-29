// 浏览器标识：后端把「IP + 这个标识」哈希成游客身份，用来按身份限制 plus 用量。
//
// 只采集设备/浏览器特征，不涉及个人内容；它的作用是让同一浏览器多次进入共用一份额度，
// 而不是把对方当成一个"账号"。因此刻意不落 localStorage——清理存储不会重置配额，
// 换浏览器或换设备才会。指纹本身不是安全手段，也挡不住刻意伪造。

// FNV-1a：把特征串压成一个短标识，这里只需要稳定，不需要密码学强度。
function fnv1a(input: string, seed: number): string {
  let hash = seed;
  for (let i = 0; i < input.length; i += 1) {
    hash ^= input.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash.toString(16).padStart(8, '0');
}

function collectFeatures(): string {
  const { navigator: nav, screen: screenInfo } = window;
  return [
    nav.userAgent,
    nav.language,
    nav.languages?.join(',') ?? '',
    nav.platform ?? '',
    nav.hardwareConcurrency ?? 0,
    screenInfo?.width ?? 0,
    screenInfo?.height ?? 0,
    screenInfo?.colorDepth ?? 0,
    window.devicePixelRatio ?? 1,
    new Date().getTimezoneOffset(),
  ].join('|');
}

// getDeviceFingerprint 返回 16 位十六进制标识；浏览器环境异常取不到特征时返回空串，
// 由后端退化为只按 IP 识别，不让游客卡在登录这一步。
export function getDeviceFingerprint(): string {
  try {
    const features = collectFeatures();
    return fnv1a(features, 0x811c9dc5) + fnv1a(features, 0x9e3779b9);
  } catch {
    return '';
  }
}
