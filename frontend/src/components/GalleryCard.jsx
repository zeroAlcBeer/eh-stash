import React from 'react';
import { Star, Heart, MessageCircle, ExternalLink, FileText, Globe, User, Calendar } from 'lucide-react';
import TagBadge from './TagBadge';

const isAndroid = /Android/i.test(navigator.userAgent);

const getExUrl = (gid, token) => {
  const https = `https://exhentai.org/g/${gid}/${token}/`;
  if (isAndroid) {
    const fallback = encodeURIComponent(https);
    return `intent://exhentai.org/g/${gid}/${token}/#Intent;scheme=https;S.browser_fallback_url=${fallback};end`;
  }
  return https;
};

const CATEGORY_STYLES = {
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

const FALLBACK_IMAGE = `data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='200' height='280' viewBox='0 0 200 280'><rect width='200' height='280' fill='%2327272a'/><text x='100' y='145' font-family='sans-serif' font-size='13' fill='%2352525b' text-anchor='middle'>No Cover</text></svg>`;

const NS_ORDER = ['artist', 'group', 'parody', 'character', 'female', 'male', 'language', 'misc'];

// ─── Grid Card ────────────────────────────────────────────────────────────────

function GridCard({ gallery }) {
  const { gid, token, title, category, rating, fav_count, thumb } = gallery;
  const exUrl = getExUrl(gid, token);
  const catStyle = CATEGORY_STYLES[category] || CATEGORY_STYLES['Misc'];

  return (
    <a
      href={exUrl}
      target={isAndroid ? '_self' : '_blank'}
      rel="noopener noreferrer"
      className="group block rounded-lg overflow-hidden bg-zinc-900 ring-1 ring-white/5 hover:ring-amber-400/60 transition-all duration-150"
      title={title}
    >
      {/* Cover */}
      <div className="relative" style={{ paddingTop: '140%' }}>
        <img
          src={thumb ? `/v1/thumbs/${gid}` : FALLBACK_IMAGE}
          alt={title}
          onError={(e) => { e.target.onerror = null; e.target.src = FALLBACK_IMAGE; }}
          className="absolute inset-0 w-full h-full object-contain bg-zinc-950"
          loading="lazy"
        />
        {/* Category badge */}
        <span className={`absolute top-1.5 right-1.5 px-1.5 py-0.5 rounded text-[10px] font-bold text-white ${catStyle}`}>
          {category}
        </span>
      </div>

      {/* Meta */}
      <div className="p-1.5 space-y-1">
        <p className="text-[11px] leading-tight text-gray-300 line-clamp-2 font-medium">{title}</p>
        <div className="flex items-center gap-2 text-[10px] text-gray-500">
          <span className="flex items-center gap-0.5 text-amber-400">
            <Star size={9} className="fill-amber-400" />{rating?.toFixed(1) ?? '-'}
          </span>
          <span className="flex items-center gap-0.5 text-rose-400">
            <Heart size={9} className="fill-rose-400" />{fav_count ?? '-'}
          </span>
        </div>
      </div>
    </a>
  );
}

// ─── List Row ─────────────────────────────────────────────────────────────────

function ListRow({ gallery, onTagSearch }) {
  const { gid, token, title, category, rating, fav_count, comment_count,
    thumb, posted_at, uploader, pages, language, tags } = gallery;
  const exUrl = getExUrl(gid, token);
  const catStyle = CATEGORY_STYLES[category] || CATEGORY_STYLES['Misc'];

  const date = posted_at
    ? new Date(posted_at).toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' })
    : null;

  const tagMap = tags || {};
  const nsKeys = [
    ...NS_ORDER.filter((ns) => tagMap[ns]?.length),
    ...Object.keys(tagMap).filter((ns) => !NS_ORDER.includes(ns) && tagMap[ns]?.length),
  ];

  return (
    <div className="flex gap-3 rounded-lg bg-zinc-900 ring-1 ring-white/5 hover:ring-white/10 transition-all p-2.5">
      {/* Thumbnail */}
      <a
        href={exUrl}
        target={isAndroid ? '_self' : '_blank'}
        rel="noopener noreferrer"
        className="shrink-0"
      >
        <img
          src={thumb ? `/v1/thumbs/${gid}` : FALLBACK_IMAGE}
          alt={title}
          onError={(e) => { e.target.onerror = null; e.target.src = FALLBACK_IMAGE; }}
          className="w-[90px] h-[126px] sm:w-[140px] sm:h-[196px] object-contain bg-zinc-950 rounded"
          loading="lazy"
        />
      </a>

      {/* Content */}
      <div className="flex-1 min-w-0 flex flex-col gap-1.5">
        {/* Title + open link */}
        <div className="flex items-start justify-between gap-2">
          <a
            href={exUrl}
            target={isAndroid ? '_self' : '_blank'}
            rel="noopener noreferrer"
            className="text-sm font-medium text-gray-200 hover:text-white line-clamp-2 leading-snug"
          >
            {title}
          </a>
          <a
            href={exUrl}
            target={isAndroid ? '_self' : '_blank'}
            rel="noopener noreferrer"
            className="shrink-0 text-gray-500 hover:text-white transition-colors mt-0.5"
            title="在 ExHentai 打开"
          >
            <ExternalLink size={14} />
          </a>
        </div>

        {/* Meta row */}
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500">
          <span className={`px-1.5 py-0.5 rounded text-[10px] font-bold text-white ${catStyle}`}>
            {category}
          </span>
          <span className="flex items-center gap-0.5 text-amber-400">
            <Star size={10} className="fill-amber-400" />{rating?.toFixed(2) ?? '-'}
          </span>
          <span className="flex items-center gap-0.5 text-rose-400">
            <Heart size={10} className="fill-rose-400" />{fav_count ?? '-'}
          </span>
          {comment_count > 0 && (
            <span className="flex items-center gap-0.5">
              <MessageCircle size={10} />{comment_count}
            </span>
          )}
          {pages && (
            <span className="flex items-center gap-0.5">
              <FileText size={10} />{pages}p
            </span>
          )}
          {language && (
            <span className="flex items-center gap-0.5">
              <Globe size={10} />{language}
            </span>
          )}
          {uploader && (
            <span className="flex items-center gap-0.5">
              <User size={10} />{uploader}
            </span>
          )}
          {date && (
            <span className="flex items-center gap-0.5">
              <Calendar size={10} />{date}
            </span>
          )}
        </div>

        {/* Tags */}
        {nsKeys.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {nsKeys.map((ns) =>
              (tagMap[ns] || []).map((v) => (
                <TagBadge
                  key={`${ns}:${v}`}
                  namespace={ns}
                  value={v}
                  onClick={() => onTagSearch?.(`${ns}:${v}`)}
                />
              ))
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// ─── Export ───────────────────────────────────────────────────────────────────

const GalleryCard = ({ gallery, viewMode = 'grid', onTagSearch }) => {
  if (viewMode === 'list') {
    return <ListRow gallery={gallery} onTagSearch={onTagSearch} />;
  }
  return <GridCard gallery={gallery} />;
};

export default GalleryCard;
