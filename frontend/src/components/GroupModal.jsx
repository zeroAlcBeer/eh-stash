import React, { useEffect, useState } from 'react';
import { X, Star, Heart, FileText, Globe, Calendar, ExternalLink } from 'lucide-react';
import { fetchGalleryGroup } from '../api';
import { getExUrl, LINK_TARGET, CATEGORY_STYLES, FALLBACK_IMAGE } from '../shared/gallery';
import { useFocusTrap } from '../hooks/useFocusTrap';

export default function GroupModal({ groupId, onClose }) {
  const [galleries, setGalleries] = useState([]);
  const [loading, setLoading] = useState(true);
  const dialogRef = useFocusTrap(Boolean(groupId), onClose);

  useEffect(() => {
    if (!groupId) return;
    setLoading(true);
    fetchGalleryGroup(groupId)
      .then(setGalleries)
      .catch(() => setGalleries([]))
      .finally(() => setLoading(false));
  }, [groupId]);

  if (!groupId) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 bg-black/60" />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={loading ? '加载中' : `${galleries.length} 个版本`}
        className="relative bg-zinc-900 rounded-lg ring-1 ring-white/10 w-full max-w-2xl max-h-[80vh] overflow-y-auto m-4"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="sticky top-0 bg-zinc-900 border-b border-white/10 px-4 py-3 flex items-center justify-between">
          <span className="text-sm font-medium text-gray-200">
            {loading ? '加载中...' : `${galleries.length} 个版本`}
          </span>
          <button
            onClick={onClose}
            className="p-2 -mr-1 text-gray-500 hover:text-white rounded-lg hover:bg-white/10 transition-colors"
            aria-label="关闭"
          >
            <X size={18} />
          </button>
        </div>

        {/* List */}
        <div className="p-3 flex flex-col gap-2">
          {galleries.map((g) => {
            const exUrl = getExUrl(g.gid, g.token);
            const catStyle = CATEGORY_STYLES[g.category] || CATEGORY_STYLES['Misc'];
            const date = g.posted_at
              ? new Date(g.posted_at).toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' })
              : null;
            const displayTitle = g.title_jpn || g.title;

            return (
              <a
                key={g.gid}
                href={exUrl}
                target={LINK_TARGET}
                rel="noopener noreferrer"
                className="flex gap-3 rounded-lg bg-zinc-800 ring-1 ring-white/5 hover:ring-amber-400/60 transition-all p-2.5"
              >
                {/* Thumbnail */}
                <img
                  src={g.thumb ? `/v1/thumbs/${g.gid}` : FALLBACK_IMAGE}
                  alt={displayTitle}
                  width={60}
                  height={84}
                  className="w-[60px] h-[84px] object-contain bg-zinc-950 rounded shrink-0"
                  loading="lazy"
                />
                {/* Info */}
                <div className="flex-1 min-w-0 flex flex-col gap-1">
                  <p className="text-sm text-gray-200 line-clamp-2 leading-snug font-medium">
                    {g.is_favorited && <Heart size={12} className="inline fill-rose-400 text-rose-400 mr-1 -mt-0.5" />}
                    {displayTitle}
                  </p>
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500">
                    <span className={`px-1.5 py-0.5 rounded text-xs font-bold text-white ${catStyle}`}>
                      {g.category}
                    </span>
                    <span className="flex items-center gap-0.5 text-amber-400">
                      <Star size={10} className="fill-amber-400" />{g.rating?.toFixed(2) ?? '-'}
                    </span>
                    <span className="flex items-center gap-0.5 text-rose-400">
                      <Heart size={10} className="fill-rose-400" />{g.fav_count ?? '-'}
                    </span>
                    {g.pages && (
                      <span className="flex items-center gap-0.5">
                        <FileText size={10} />{g.pages}p
                      </span>
                    )}
                    {g.language && (
                      <span className="flex items-center gap-0.5">
                        <Globe size={10} />{g.language}
                      </span>
                    )}
                    {date && (
                      <span className="flex items-center gap-0.5">
                        <Calendar size={10} />{date}
                      </span>
                    )}
                    <ExternalLink size={10} className="ml-auto text-gray-600" />
                  </div>
                </div>
              </a>
            );
          })}
        </div>
      </div>
    </div>
  );
}
