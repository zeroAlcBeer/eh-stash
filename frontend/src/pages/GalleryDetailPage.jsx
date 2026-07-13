import React, { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useNavigate, useParams } from 'react-router-dom';
import {
  AlertCircle,
  ArrowLeft,
  CalendarDays,
  Clock3,
  Download,
  ExternalLink,
  FileText,
  Globe2,
  HardDrive,
  Heart,
  Layers3,
  Loader2,
  MessageCircle,
  Quote,
  Star,
  UserRound,
  Users,
} from 'lucide-react';
import { fetchGallery, fetchGalleryComments } from '../api';
import GroupModal from '../components/GroupModal';
import TagBadge from '../components/TagBadge';
import { CATEGORY_STYLES, FALLBACK_IMAGE, getExUrl, getThumbUrl, LINK_TARGET } from '../shared/gallery';
import { t, formatDate, formatDateTime } from '../shared/i18n';
import { IS_PUBLIC } from '../shared/mode';

const NS_ORDER = ['artist', 'group', 'parody', 'character', 'female', 'male', 'language', 'misc'];

function Metric({ icon: Icon, label, value, tone = 'text-gray-100', hint }) {
  return (
    <div className="rounded-xl border border-white/10 bg-white/[0.035] px-3.5 py-3">
      <div className="mb-2 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-[0.12em] text-gray-500">
        <Icon size={12} aria-hidden="true" />
        {label}
      </div>
      <div className={`text-lg font-semibold tabular-nums ${tone}`}>{value ?? '—'}</div>
      {hint && <div className="mt-0.5 text-xs text-gray-600">{hint}</div>}
    </div>
  );
}

function MetadataRow({ label, value, icon: Icon }) {
  if (value === null || value === undefined || value === '') return null;
  return (
    <div className="flex items-start justify-between gap-4 border-b border-white/5 py-2.5 last:border-b-0">
      <span className="flex items-center gap-2 text-sm text-gray-500">
        <Icon size={14} aria-hidden="true" />
        {label}
      </span>
      <span className="max-w-[65%] text-right text-sm text-gray-200">{value}</span>
    </div>
  );
}

function CommentCard({ comment }) {
  return (
    <article className={`rounded-xl border p-4 ${comment.is_uploader_comment
      ? 'border-sky-500/25 bg-sky-500/[0.06]'
      : 'border-white/10 bg-white/[0.025]'
    }`}>
      <div className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500">
        <span className="font-medium text-gray-200">{comment.author || t('detail.comment.anonymous')}</span>
        {comment.is_uploader_comment && (
          <span className="rounded-full bg-sky-500/15 px-2 py-0.5 text-sky-300">
            {t('detail.comment.uploader')}
          </span>
        )}
        {comment.score !== null && comment.score !== undefined && (
          <span className="flex items-center gap-1 text-emerald-400">
            <Star size={11} aria-hidden="true" />{comment.score}
          </span>
        )}
        {comment.posted_at && <span>{comment.posted_at}</span>}
      </div>
      <div className="flex items-start gap-2.5">
        <Quote size={14} className="mt-1 shrink-0 text-gray-600" aria-hidden="true" />
        <p className="whitespace-pre-wrap break-words text-sm leading-6 text-gray-300">{comment.body}</p>
      </div>
    </article>
  );
}

export default function GalleryDetailPage() {
  const { gid: rawGid } = useParams();
  const gid = Number(rawGid);
  const navigate = useNavigate();
  const [groupModalId, setGroupModalId] = useState(null);

  const galleryQuery = useQuery({
    queryKey: ['gallery', gid],
    queryFn: () => fetchGallery(gid),
    enabled: Number.isFinite(gid),
  });

  const commentsQuery = useQuery({
    queryKey: ['gallery-comments', gid],
    queryFn: () => fetchGalleryComments(gid),
    enabled: !IS_PUBLIC && Number.isFinite(gid),
  });

  const gallery = galleryQuery.data;
  const displayTitle = gallery?.title_jpn || gallery?.title || t('detail.untitled');
  const secondaryTitle = gallery?.title_jpn && gallery?.title && gallery.title_jpn !== gallery.title
    ? gallery.title
    : null;

  useEffect(() => {
    if (!gallery) return undefined;
    const previous = document.title;
    document.title = `${displayTitle} · EhStash`;
    return () => { document.title = previous; };
  }, [displayTitle, gallery]);

  const tagNamespaces = useMemo(() => {
    const tagMap = gallery?.tags || {};
    return [
      ...NS_ORDER.filter((ns) => tagMap[ns]?.length),
      ...Object.keys(tagMap).filter((ns) => !NS_ORDER.includes(ns) && tagMap[ns]?.length),
    ];
  }, [gallery?.tags]);

  const handleBack = () => {
    if (window.history.length > 1) navigate(-1);
    else navigate('/');
  };

  const handleTag = (namespace, value) => {
    const params = new URLSearchParams({ tags: `${namespace}:${value}` });
    navigate(`/?${params.toString()}`);
  };

  if (!Number.isFinite(gid) || galleryQuery.isError) {
    return (
      <div className="mx-auto max-w-xl py-24 text-center">
        <AlertCircle size={32} className="mx-auto mb-3 text-rose-400" />
        <h1 className="text-lg font-semibold text-white">{t('detail.error.title')}</h1>
        <p className="mt-1 text-sm text-gray-500">{t('detail.error.body')}</p>
        <button type="button" onClick={handleBack} className="pressable mt-5 rounded-lg bg-white/10 px-4 py-2 text-sm text-white hover:bg-white/15">
          {t('detail.back')}
        </button>
      </div>
    );
  }

  if (galleryQuery.isLoading || !gallery) {
    return (
      <div className="flex items-center justify-center py-28 text-gray-500">
        <Loader2 size={24} className="animate-spin" />
        <span className="ml-2 text-sm">{t('loading')}</span>
      </div>
    );
  }

  const exUrl = getExUrl(gallery.gid, gallery.token);
  const categoryClass = CATEGORY_STYLES[gallery.category] || CATEGORY_STYLES.Misc;
  const similarity = gallery.similarity == null ? null : `${Math.round(gallery.similarity * 100)}%`;

  return (
    <div className="mx-auto max-w-6xl pb-16">
      <div className="mb-4 flex items-center justify-between gap-3">
        <button type="button" onClick={handleBack} className="pressable -ml-2 flex items-center gap-2 rounded-lg px-2.5 py-2 text-sm text-gray-400 transition-colors hover:bg-white/5 hover:text-white">
          <ArrowLeft size={16} />{t('detail.back')}
        </button>
        <a href={exUrl} target={LINK_TARGET} rel="noopener noreferrer" className="pressable flex items-center gap-2 rounded-lg bg-blue-600 px-3.5 py-2 text-sm font-medium text-white transition-colors hover:bg-blue-500">
          {t('detail.openEx')}<ExternalLink size={14} />
        </a>
      </div>

      {gallery.is_expunged && (
        <div role="status" className="mb-4 flex items-center gap-2 rounded-xl border border-amber-500/25 bg-amber-500/10 px-4 py-3 text-sm text-amber-200">
          <AlertCircle size={16} />{t('detail.expunged')}
        </div>
      )}

      {!IS_PUBLIC && !gallery.file_size && (
        <div role="status" className="mb-4 flex items-center gap-2 rounded-xl border border-sky-500/20 bg-sky-500/[0.07] px-4 py-3 text-sm text-sky-200">
          <Clock3 size={16} />{t('detail.pending')}
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-[280px_minmax(0,1fr)]">
        <aside>
          <div className="sticky top-16 overflow-hidden rounded-2xl border border-white/10 bg-zinc-900 shadow-2xl shadow-black/20">
            <img
              src={gallery.thumb ? getThumbUrl(gallery.gid) : FALLBACK_IMAGE}
              alt={displayTitle}
              onError={(event) => { event.currentTarget.onerror = null; event.currentTarget.src = FALLBACK_IMAGE; }}
              className="aspect-[5/7] w-full bg-zinc-950 object-contain"
            />
            <div className="space-y-1 px-4 py-3">
              <MetadataRow icon={UserRound} label={t('detail.uploader')} value={gallery.uploader} />
              <MetadataRow icon={Globe2} label={t('detail.language')} value={gallery.language} />
              <MetadataRow icon={CalendarDays} label={t('detail.posted')} value={formatDate(gallery.posted_at)} />
              <MetadataRow icon={Users} label={t('detail.visibility')} value={gallery.visible} />
              <MetadataRow icon={Clock3} label={t('detail.synced')} value={formatDateTime(gallery.last_synced_at)} />
              <MetadataRow icon={FileText} label="GID" value={gallery.gid.toLocaleString()} />
            </div>
          </div>
        </aside>

        <main className="min-w-0">
          <div className="mb-5">
            <div className="mb-3 flex flex-wrap items-center gap-2">
              <span className={`rounded-md px-2 py-1 text-xs font-bold text-white ${categoryClass}`}>{gallery.category}</span>
              {gallery.group_count > 1 && (
                <button type="button" onClick={() => setGroupModalId(gallery.group_id)} className="pressable flex items-center gap-1.5 rounded-md border border-amber-500/25 bg-amber-500/10 px-2 py-1 text-xs text-amber-300 hover:bg-amber-500/15">
                  <Layers3 size={12} />{t('detail.versions', { count: gallery.group_count })}
                </button>
              )}
              {similarity && (
                <span className="rounded-md border border-violet-500/20 bg-violet-500/10 px-2 py-1 text-xs text-violet-300">
                  {t('detail.similarity', { value: similarity })}
                </span>
              )}
            </div>
            <h1 className="text-balance text-2xl font-semibold leading-tight text-white sm:text-3xl">{displayTitle}</h1>
            {secondaryTitle && <p className="mt-2 text-sm leading-6 text-gray-500">{secondaryTitle}</p>}
          </div>

          <section aria-label={t('detail.metrics')} className="mb-7 grid grid-cols-2 gap-2.5 sm:grid-cols-3 xl:grid-cols-6">
            <Metric icon={Star} label={t('detail.rating')} value={gallery.rating?.toFixed(2)} tone="text-amber-300" hint={gallery.rating_count != null ? t('detail.votes', { count: gallery.rating_count.toLocaleString() }) : null} />
            <Metric icon={Heart} label={t('detail.favorites')} value={gallery.fav_count?.toLocaleString()} tone="text-rose-300" />
            <Metric icon={MessageCircle} label={t('detail.comments')} value={gallery.comment_count?.toLocaleString()} />
            <Metric icon={FileText} label={t('detail.pages')} value={gallery.pages?.toLocaleString()} />
            <Metric icon={HardDrive} label={t('detail.fileSize')} value={gallery.file_size} />
            <Metric icon={Download} label={t('detail.torrents')} value={gallery.torrent_count?.toLocaleString()} />
          </section>

          {tagNamespaces.length > 0 && (
            <section className="mb-8">
              <h2 className="mb-3 text-sm font-semibold text-white">{t('detail.tags')}</h2>
              <div className="space-y-2 rounded-2xl border border-white/10 bg-white/[0.025] p-4">
                {tagNamespaces.map((namespace) => (
                  <div key={namespace} className="grid grid-cols-[4.5rem_minmax(0,1fr)] gap-2">
                    <span className="pt-1 text-right text-xs text-gray-600">{namespace}</span>
                    <div className="flex flex-wrap gap-1.5">
                      {gallery.tags[namespace].map((value) => (
                        <TagBadge key={`${namespace}:${value}`} namespace={namespace} value={value} showNs={false} onClick={() => handleTag(namespace, value)} />
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            </section>
          )}

          {!IS_PUBLIC && (
            <section>
              <div className="mb-3 flex items-end justify-between gap-3">
                <div>
                  <h2 className="text-sm font-semibold text-white">{t('detail.comments')}</h2>
                  <p className="mt-1 text-xs text-gray-600">{t('detail.comments.hint')}</p>
                </div>
                {commentsQuery.data?.length > 0 && <span className="text-xs tabular-nums text-gray-500">{commentsQuery.data.length.toLocaleString()}</span>}
              </div>
              {commentsQuery.isLoading ? (
                <div className="flex items-center gap-2 rounded-xl border border-white/10 px-4 py-5 text-sm text-gray-500">
                  <Loader2 size={16} className="animate-spin" />{t('loading')}
                </div>
              ) : commentsQuery.isError ? (
                <div className="rounded-xl border border-rose-500/20 bg-rose-500/[0.06] px-4 py-3 text-sm text-rose-300">{t('detail.comments.error')}</div>
              ) : commentsQuery.data?.length ? (
                <div className="space-y-2.5">
                  {commentsQuery.data.map((comment) => <CommentCard key={comment.id} comment={comment} />)}
                </div>
              ) : (
                <div className="rounded-xl border border-white/10 bg-white/[0.02] px-4 py-6 text-center text-sm text-gray-600">{t('detail.comments.empty')}</div>
              )}
            </section>
          )}
        </main>
      </div>

      {groupModalId && <GroupModal groupId={groupModalId} onClose={() => setGroupModalId(null)} />}
    </div>
  );
}
