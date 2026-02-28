from pydantic import BaseModel, Field, model_validator
from typing import List, Optional, Dict, Any, Literal
from datetime import datetime

VALID_CATEGORIES = [
    "Misc", "Doujinshi", "Manga", "Artist CG", "Game CG",
    "Image Set", "Cosplay", "Asian Porn", "Non-H", "Western",
]
MIXED_CATEGORY = "Mixed"

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

class GalleryList(BaseModel):
    items: List[Gallery]
    total: int
    page: int
    size: int
    pages: int  # total number of pages

class Stats(BaseModel):
    total_galleries: int
    by_category: Dict[str, int]
    last_synced_at: Optional[datetime] = None
    queue_status: Dict[str, Any]


class SyncTaskCreate(BaseModel):
    name: str
    type: Literal["full", "incremental"]
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


class ThumbQueueStats(BaseModel):
    pending: int
    processing: int
    done: int
    waiting: int
