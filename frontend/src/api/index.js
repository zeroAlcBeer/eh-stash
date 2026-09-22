async function getJson(path, params) {
  const url = new URL(window.location.origin + path);
  if (params) {
    for (const [k, v] of Object.entries(params)) {
      if (v === undefined || v === null || v === '') continue;
      if (Array.isArray(v)) {
        for (const item of v) if (item) url.searchParams.append(k, item);
      } else {
        url.searchParams.append(k, v);
      }
    }
  }
  const res = await fetch(url.toString());
  if (!res.ok) {
    const body = await res.text().catch(() => '');
    throw new Error(`${res.status} ${res.statusText}${body ? `: ${body}` : ''}`);
  }
  return res.json();
}

export const fetchGalleries = async (params) => {
  const { tags, ...rest } = params || {};
  return getJson('/v1/galleries', { ...rest, tag: tags });
};

export const fetchGalleryGroup = async (groupId) =>
  getJson(`/v1/galleries/group/${groupId}`);

export const fetchGallery = async (gid) =>
  getJson(`/v1/galleries/${gid}`);

export const fetchGalleryComments = async (gid, limit = 200) =>
  getJson(`/v1/galleries/${gid}/comments`, { limit });
