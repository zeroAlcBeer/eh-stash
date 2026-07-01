import { IS_PUBLIC } from '../shared/mode';
import { getAllowCosplay } from '../shared/settings';

const BASE = (import.meta.env.VITE_API_BASE_URL || '').replace(/\/$/, '');

async function getJson(path, params) {
  const url = new URL((BASE || window.location.origin) + path);
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

// In public mode, allow_cosplay gates the Cosplay category at the worker.
// In self-hosted mode, the category param is passed directly by the filter UI.
function cosplayParam() {
  return IS_PUBLIC && getAllowCosplay() ? 1 : undefined;
}

export const fetchGalleries = async (params) => {
  const { tags, ...rest } = params || {};
  return getJson('/v1/galleries', { ...rest, tag: tags, allow_cosplay: cosplayParam() });
};

export const fetchStats = async () => getJson('/v1/stats');

export const fetchGalleryGroup = async (groupId) =>
  getJson(`/v1/galleries/group/${groupId}`, {
    allow_cosplay: cosplayParam(),
  });

export const fetchGallery = async (gid) =>
  getJson(`/v1/galleries/${gid}`, {
    allow_cosplay: cosplayParam(),
  });
