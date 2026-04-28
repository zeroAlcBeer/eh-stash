import React from 'react';
import { Star, Heart, MessageCircle, ExternalLink, FileText, Globe, User, Calendar, Layers } from 'lucide-react';
import TagBadge from './TagBadge';
import { getExUrl, LINK_TARGET, CATEGORY_STYLES, FALLBACK_IMAGE } from '../shared/gallery';

const NS_ORDER = ['artist', 'group', 'parody', 'character', 'female', 'male', 'language', 'misc'];

// ─── Grid Card ────────────────────────────────────────────────────────────────

function GridCard({ gallery, onGroupClick }) {
  const { gid, token, title, title_jpn, category, rating, fav_count, thumb, posted_at } = gallery;
  const displayTitle = title_jpn || title;
  const exUrl = getExUrl(gid, token);
  const catStyle = CATEGORY_STYLES[category] || CATEGORY_STYLES['Misc'];
  const hasGroup = gallery.group_count > 1;
  const date = posted_at
    ? new Date(posted_at).toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' })
    : null;

  const handleClick = (e) => {
    if (hasGroup) {
      e.preventDefault();
      onGroupClick?.(gallery.group_id);
    }
  };

  return (
    <a
      href={exUrl}
      target={LINK_TARGET}
      rel="noopener noreferrer"
      onClick={handleClick}
      className={`group flex flex-col rounded-lg overflow-hidden bg-zinc-900 transition-all duration-150 ${gallery.is_favorited ? 'ring-2 ring-rose-500/70 hover:ring-rose-400' : 'ring-1 ring-white/5 hover:ring-amber-400/60'}`}
      title={displayTitle}
    >
      {/* Cover */}
      <div className="relative" style={{ paddingTop: '140%' }}>
        <img
          src={thumb ? `/v1/thumbs/${gid}` : FALLBACK_IMAGE}
          alt={displayTitle}
          onError={(e) => { e.target.onerror = null; e.target.src = FALLBACK_IMAGE; }}
          className="absolute inset-0 w-full h-full object-contain bg-zinc-950"
          loading="lazy"
        />
        {/* Category + custom badges */}
        <div className="absolute top-1.5 right-1.5 flex flex-col items-end gap-0.5">
          <span className={`px-1.5 py-0.5 rounded text-xs font-bold text-white ${catStyle}`}>
            {category}
          </span>
          {!gallery.is_active && (
            <span className="px-1.5 py-0.5 rounded text-xs font-bold text-white bg-red-700/80">
              Deleted
            </span>
          )}
          {category === 'Doujinshi' && gallery.pages > 0 && gallery.pages < 10 && (
            <span className="px-1.5 py-0.5 rounded text-xs font-bold text-white bg-zinc-500/80">
              おまけ
            </span>
          )}
        </div>
        {/* Favorites badge — corner ribbon */}
        {gallery.is_favorited && (
          <>
            <div className="absolute top-0 left-0 w-0 h-0 border-t-[36px] border-r-[36px] border-t-rose-500/80 border-r-transparent" />
            <Heart size={12} className="absolute top-1 left-1 fill-white text-white drop-shadow" aria-hidden="true" />
          </>
        )}
        {/* Version count badge */}
        {hasGroup && (
          <span className="absolute bottom-1.5 left-1.5 flex items-center gap-0.5 px-1.5 py-0.5 rounded bg-amber-500/80 text-xs font-bold text-white">
            <Layers size={10} />{gallery.group_count}
          </span>
        )}
      </div>

      {/* Meta — flex-1 so it fills remaining height; justify-between pins stats to bottom */}
      <div className="p-1.5 flex flex-col flex-1 gap-1">
        <p className="text-xs leading-tight text-gray-300 line-clamp-2 font-medium flex-1">{displayTitle}</p>
        <div className="flex items-center justify-between text-xs text-gray-500">
          <div className="flex items-center gap-2">
            <span className="flex items-center gap-0.5 text-amber-400">
              <Star size={9} className="fill-amber-400" aria-hidden="true" />{rating?.toFixed(1) ?? '-'}
            </span>
            <span className="flex items-center gap-0.5 text-rose-400">
              <Heart size={9} className="fill-rose-400" aria-hidden="true" />{fav_count ?? '-'}
            </span>
          </div>
          {date && (
            <span className="flex items-center gap-0.5 text-gray-600">
              <Calendar size={9} aria-hidden="true" />{date}
            </span>
          )}
        </div>
      </div>
    </a>
  );
}

// ─── List Row ─────────────────────────────────────────────────────────────────

function ListRow({ gallery, onTagSearch, translate, onGroupClick }) {
  const { gid, token, title, title_jpn, category, rating, fav_count, comment_count,
    thumb, posted_at, uploader, pages, language, tags } = gallery;
  const displayTitle = title_jpn || title;
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
        target={LINK_TARGET}
        rel="noopener noreferrer"
        className="shrink-0"
      >
        <img
          src={thumb ? `/v1/thumbs/${gid}` : FALLBACK_IMAGE}
          alt={title}
          onError={(e) => { e.target.onerror = null; e.target.src = FALLBACK_IMAGE; }}
          width={140}
          height={196}
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
            target={LINK_TARGET}
            rel="noopener noreferrer"
            className="text-sm font-medium text-gray-200 hover:text-white line-clamp-2 leading-snug"
          >
            {gallery.is_favorited && <Heart size={12} className="inline fill-rose-400 text-rose-400 mr-1 -mt-0.5" aria-hidden="true" />}
            {displayTitle}
          </a>
          <a
            href={exUrl}
            target={LINK_TARGET}
            rel="noopener noreferrer"
            className="shrink-0 p-1.5 -mr-1.5 text-gray-500 hover:text-white transition-colors rounded-lg hover:bg-white/10"
            aria-label="在 ExHentai 打开"
          >
            <ExternalLink size={14} />
          </a>
        </div>

        {/* Meta row */}
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500">
          <span className={`px-1.5 py-0.5 rounded text-xs font-bold text-white ${catStyle}`}>
            {category}
          </span>
          <span className="flex items-center gap-0.5 text-amber-400">
            <Star size={10} className="fill-amber-400" aria-hidden="true" />{rating?.toFixed(2) ?? '-'}
          </span>
          <span className="flex items-center gap-0.5 text-rose-400">
            <Heart size={10} className="fill-rose-400" aria-hidden="true" />{fav_count ?? '-'}
          </span>
          {comment_count > 0 && (
            <span className="flex items-center gap-0.5">
              <MessageCircle size={10} aria-hidden="true" />{comment_count}
            </span>
          )}
          {pages && (
            <span className="flex items-center gap-0.5">
              <FileText size={10} aria-hidden="true" />{pages}p
            </span>
          )}
          {language && (
            <span className="flex items-center gap-0.5">
              <Globe size={10} aria-hidden="true" />{language}
            </span>
          )}
          {uploader && (
            <span className="flex items-center gap-0.5">
              <User size={10} aria-hidden="true" />{uploader}
            </span>
          )}
          {date && (
            <span className="flex items-center gap-0.5">
              <Calendar size={10} aria-hidden="true" />{date}
            </span>
          )}
          {gallery.group_count > 1 && (
            <button
              onClick={(e) => { e.stopPropagation(); onGroupClick?.(gallery.group_id); }}
              className="flex items-center gap-0.5 text-amber-400 hover:text-amber-300 cursor-pointer"
              aria-label={`查看 ${gallery.group_count} 个版本`}
            >
              <Layers size={10} />{gallery.group_count}版本
            </button>
          )}
        </div>

        {/* Tags – grouped by namespace */}
        {nsKeys.length > 0 && (
          <div className="flex flex-col gap-0.5">
            {nsKeys.map((ns) => (
              <div key={ns} className="flex items-start gap-1.5">
                {/* Namespace label */}
                <span className="shrink-0 text-xs text-gray-600 w-[3.5rem] text-right leading-[1.6rem] select-none">
                  {ns}
                </span>
                {/* Tags for this namespace */}
                <div className="flex flex-wrap gap-1">
                  {(tagMap[ns] || []).map((v) => (
                    <TagBadge
                      key={`${ns}:${v}`}
                      namespace={ns}
                      value={v}
                      translation={translate ? translate(v) : undefined}
                      showNs={false}
                      onClick={() => onTagSearch?.(`${ns}:${v}`)}
                    />
                  ))}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

// ─── Export ───────────────────────────────────────────────────────────────────

const GalleryCard = ({ gallery, viewMode = 'grid', onTagSearch, translate, onGroupClick }) => {
  if (viewMode === 'list') {
    return <ListRow gallery={gallery} onTagSearch={onTagSearch} translate={translate} onGroupClick={onGroupClick} />;
  }
  return <GridCard gallery={gallery} onGroupClick={onGroupClick} />;
};

export default GalleryCard;
