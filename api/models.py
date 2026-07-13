from pydantic import BaseModel, Field, model_validator
from typing import List, Optional, Dict, Any, Literal
from datetime import datetime

VALID_CATEGORIES = [
    "Misc", "Doujinshi", "Manga", "Artist CG", "Game CG",
    "Image Set", "Cosplay", "Asian Porn", "Non-H", "Western",
]
MIXED_CATEGORY = "Mixed"
FAVORITES_CATEGORY = "Favorites"
REFRESH_CATEGORY = "Refresh Detail"

class Gallery(BaseModel):
    gid: int
    token: str
    category: Optional[str] = None
    title: Optional[str] = None
    title_jpn: Optional[str] = None
    uploader: Optional[str] = None
    posted_at: Optional[datetime] = None # Or string? DB has TIMESTAMPTZ, psycopg2 returns datetime
    language: Optional[str] = None
    pages: Optional[int] = None
    rating: Optional[float] = None
    fav_count: Optional[int] = 0
    comment_count: Optional[int] = 0
    thumb: Optional[str] = None
    tags: Optional[Dict[str, List[str]]] = None
    last_synced_at: Optional[datetime] = None
    is_active: bool = True
    is_favorited: bool = False
    favorited_at: Optional[datetime] = None
    similarity: Optional[float] = None
    group_id: Optional[int] = None
    group_count: Optional[int] = None
    # 006_detail_extras columns — nullable so old-style rows stay NULL.
    file_size: Optional[str] = None
    file_size_bytes: Optional[int] = None
    rating_count: Optional[int] = None
    visible: Optional[str] = None
    parent_gid: Optional[int] = None
    torrent_count: Optional[int] = 0
    is_expunged: bool = False

class GalleryList(BaseModel):
    items: List[Gallery]
    total: int
    page: int
    size: int
    pages: int  # total number of pages


class GalleryComment(BaseModel):
    id: int
    gid: int
    comment_index: int
    author: str = ""
    author_url: Optional[str] = None
    posted_at: Optional[str] = None
    score: Optional[int] = None
    body: str = ""
    is_uploader_comment: bool = False
    fetched_at: Optional[datetime] = None

class Stats(BaseModel):
    total_galleries: int
    by_category: Dict[str, int]
    last_synced_at: Optional[datetime] = None


class SyncTaskCreate(BaseModel):
    name: str
    type: Literal["full", "incremental", "favorites", "refresh_detail"]
    category: str
    config: Dict[str, Any] = Field(default_factory=dict)

    @model_validator(mode="after")
    def validate_by_task_type(self):
        if self.type == "full":
            if self.category not in VALID_CATEGORIES:
                raise ValueError(
                    f"Invalid category '{self.category}'. Must be one of: {', '.join(VALID_CATEGORIES)}"
                )
            return self

        if self.type == "favorites":
            if self.category != FAVORITES_CATEGORY:
                raise ValueError(f"Favorites task category must be '{FAVORITES_CATEGORY}'")
            return self

        if self.type == "refresh_detail":
            if self.category != REFRESH_CATEGORY:
                raise ValueError(f"refresh_detail task category must be '{REFRESH_CATEGORY}'")
            return self

        if self.category != MIXED_CATEGORY:
            raise ValueError(f"Incremental task category must be '{MIXED_CATEGORY}'")

        cats = self.config.get("categories")
        if not isinstance(cats, list) or not cats:
            raise ValueError("Incremental task requires config.categories as a non-empty list")

        normalized: list[str] = []
        seen: set[str] = set()
        for item in cats:
            if not isinstance(item, str):
                raise ValueError("config.categories must be a list of strings")
            value = item.strip()
            if value not in VALID_CATEGORIES:
                raise ValueError(
                    f"Invalid category '{value}' in config.categories. "
                    f"Must be one of: {', '.join(VALID_CATEGORIES)}"
                )
            if value not in seen:
                seen.add(value)
                normalized.append(value)

        config = dict(self.config)
        config["categories"] = normalized
        self.config = config
        return self


class SyncTaskUpdate(BaseModel):
    name: Optional[str] = None
    config: Optional[Dict[str, Any]] = None


class SyncTask(BaseModel):
    id: int
    name: str
    type: str
    category: str
    status: str
    desired_status: str
    config: Dict[str, Any]
    state: Dict[str, Any]
    progress_pct: float
    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None
    last_run_at: Optional[datetime] = None
    error_message: Optional[str] = None
    enabled: Optional[bool] = None
    task_kind: Optional[str] = None
    source: Optional[str] = None
    strategy: Optional[str] = None
    scope: Optional[Dict[str, Any]] = None
    checkpoint: Optional[Dict[str, Any]] = None
    progress: Optional[Dict[str, Any]] = None
    current_job_id: Optional[int] = None
    last_job_id: Optional[int] = None
    current_job_state: Optional[str] = None
    current_job_kind: Optional[str] = None
    current_job_attempt: Optional[int] = None
    current_job_max_attempts: Optional[int] = None
    current_job_scheduled_at: Optional[datetime] = None
    current_job_attempted_at: Optional[datetime] = None
    current_job_finalized_at: Optional[datetime] = None
    latest_job_state: Optional[str] = None
    latest_job_kind: Optional[str] = None
    latest_job_attempt: Optional[int] = None
    latest_job_max_attempts: Optional[int] = None
    latest_job_scheduled_at: Optional[datetime] = None
    latest_job_attempted_at: Optional[datetime] = None
    latest_job_finalized_at: Optional[datetime] = None
    schedule_kind: Optional[str] = None
    schedule_interval_sec: Optional[int] = None
    next_run_at: Optional[datetime] = None
    last_finished_at: Optional[datetime] = None
    requested_action: Optional[str] = None


class ThumbQueueStats(BaseModel):
    pending: int
    processing: int
    done: int
    waiting: int


class SimilarityDistribution(BaseModel):
    buckets: List[Dict[str, Any]]  # [{min, max, count}, ...]
    total: int
    threshold: float
    count_above: int


class EmbeddingsStatus(BaseModel):
    vocab_size: int
    dim_count: int
    total_galleries: int
    embedded_count: int
    pending_count: int
    profile_liked_count: int
    profile_ready: bool
