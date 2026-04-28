const isAndroid = /Android/i.test(navigator.userAgent);

export function getExUrl(gid, token) {
  const https = `https://exhentai.org/g/${gid}/${token}/`;
  if (isAndroid) {
    const fallback = encodeURIComponent(https);
    return `intent://exhentai.org/g/${gid}/${token}/#Intent;scheme=https;S.browser_fallback_url=${fallback};end`;
  }
  return https;
}

export const LINK_TARGET = isAndroid ? '_self' : '_blank';

export const CATEGORY_STYLES = {
  'Manga': 'bg-orange-500/80',
  'Doujinshi': 'bg-rose-600/80',
  'Cosplay': 'bg-purple-600/80',
  'Asian Porn': 'bg-pink-600/80',
  'Non-H': 'bg-blue-600/80',
  'Western': 'bg-emerald-600/80',
  'Image Set': 'bg-indigo-600/80',
  'Game CG': 'bg-teal-600/80',
  'Artist CG': 'bg-yellow-500/80',
  'Misc': 'bg-zinc-600/80',
};

export const FALLBACK_IMAGE = `data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='200' height='280' viewBox='0 0 200 280'><rect width='200' height='280' fill='%2327272a'/><text x='100' y='145' font-family='sans-serif' font-size='13' fill='%2352525b' text-anchor='middle'>No Cover</text></svg>`;
