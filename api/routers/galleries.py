from fastapi import APIRouter, Depends, Query, HTTPException
from typing import Optional, List
from db import get_db
from models import Gallery, GalleryList
from routers.admin import get_similarity_threshold
import math
import os
import json

router = APIRouter(prefix="/v1/galleries", tags=["galleries"])

def _parse_blacklist() -> list[list[tuple[str, str]]]:
    """Parse TAG_BLACKLIST env var.
    Format: "ns:val, ns:val, ns:val+ns:val+ns:val, ..."
    Single tag  → excluded if present.
    Composite   → excluded only when ALL tags in the group are present (AND).
    Returns list of rules; each rule is a list of (namespace, value) tuples.
    """
    raw = os.getenv("TAG_BLACKLIST", "")
    rules: list[list[tuple[str, str]]] = []
    for item in raw.split(","):
        item = item.strip()
        if not item:
            continue
        tags: list[tuple[str, str]] = []
        for part in item.split("+"):
            part = part.strip()
            if ":" in part:
                ns, val = part.split(":", 1)
                ns, val = ns.strip(), val.strip()
                if ns and val:
                    tags.append((ns, val))
        if tags:
            rules.append(tags)
    return rules

TAG_BLACKLIST: list[list[tuple[str, str]]] = _parse_blacklist()


def _build_where(category, language, min_rating, min_fav, tag):
    """Build shared WHERE clauses and params for gallery queries."""
    parts = ["TRUE"]
    params = []

    for rule in TAG_BLACKLIST:
        conds = []
        for ns, val in rule:
            conds.append("g.tags @> %s::jsonb")
            params.append(json.dumps({ns: [val]}))
        parts.append("NOT (" + " AND ".join(conds) + ")")

    if category:
        parts.append("g.category ILIKE %s")
        params.append(category)
    if language:
        parts.append("g.language ILIKE %s")
        params.append(language)
    if min_rating is not None:
        parts.append("g.rating >= %s")
        params.append(min_rating)
    if min_fav is not None:
        parts.append("g.fav_count >= %s")
        params.append(min_fav)
    if tag:
        tags = tag if isinstance(tag, list) else [tag]
        for t in tags:
            t = t.replace("\uff1a", ":").strip()
            if t and ":" in t:
                ns, val = t.split(":", 1)
                ns, val = ns.strip().lower(), val.strip()
                if ns and val:
                    parts.append("g.tags @> %s::jsonb")
                    params.append(json.dumps({ns: [val]}))

    return parts, params


def _rows_to_galleries(db, rows):
    col_names = [desc[0] for desc in db.description]
    return [Gallery(**dict(zip(col_names, row))) for row in rows]


def _get_recommended(*, db, category, language, min_rating, min_fav, tag, is_favorited, page, page_size, offset):
    """Recommended: query precomputed similarity in recommended_cache, ORDER BY index."""
    where_parts, params = _build_where(category, language, min_rating, min_fav, tag)

    threshold = get_similarity_threshold(db)
    where_parts.append("rc.similarity >= %s")
    params.append(threshold)
    where_parts.append("g.is_active = TRUE")

    if is_favorited is True:
        where_parts.append("(f.gid IS NOT NULL OR gf.group_id IS NOT NULL)")
    elif is_favorited is False:
        where_parts.append("(f.gid IS NULL AND gf.group_id IS NULL)")

    where_sql = " AND ".join(where_parts)

    query = f"""
        SELECT g.*,
               rc.similarity,
               (f.gid IS NOT NULL OR gf.group_id IS NOT NULL) AS is_favorited,
               f.favorited_at,
               ggm.group_id,
               COALESCE(gc.cnt, 0) AS group_count
        FROM recommended_cache rc
        JOIN eh_galleries g ON g.gid = rc.gid
        LEFT JOIN user_favorites f ON g.gid = f.gid
        LEFT JOIN gallery_group_members ggm ON g.gid = ggm.gid
        LEFT JOIN (
            SELECT group_id, COUNT(*) AS cnt
            FROM gallery_group_members
            GROUP BY group_id
        ) gc ON gc.group_id = ggm.group_id
        LEFT JOIN (
            SELECT DISTINCT ggm2.group_id
            FROM gallery_group_members ggm2
            JOIN user_favorites f2 ON f2.gid = ggm2.gid
        ) gf ON gf.group_id = ggm.group_id
        WHERE {where_sql}
        ORDER BY rc.similarity DESC, g.gid DESC
    """

    count_query = f"SELECT COUNT(*) FROM ({query}) AS sub"
    db.execute(count_query, params)
    total = db.fetchone()[0]

    query += " LIMIT %s OFFSET %s"
    params.extend([page_size, offset])

    db.execute(query, params)
    items = _rows_to_galleries(db, db.fetchall())

    return GalleryList(
        items=items, total=total, page=page, size=page_size,
        pages=math.ceil(total / page_size) if total else 0,
    )


@router.get("", response_model=GalleryList)
def get_galleries(
    category: Optional[str] = None,
    language: Optional[str] = None,
    min_rating: Optional[float] = None,
    min_fav: Optional[int] = None,
    tag: Optional[List[str]] = Query(None),
    is_favorited: Optional[bool] = None,
    sort: Optional[str] = "gid_desc",
    page: int = 1,
    page_size: int = 24,
    db = Depends(get_db)
):
    offset = (page - 1) * page_size

    if sort == "recommended":
        return _get_recommended(
            db=db, category=category, language=language, min_rating=min_rating,
            min_fav=min_fav, tag=tag, is_favorited=is_favorited,
            page=page, page_size=page_size, offset=offset,
        )

    # Standard query with LEFT JOIN for favorites info
    where_parts, params = _build_where(category, language, min_rating, min_fav, tag)

    if is_favorited is True:
        where_parts.append("f.gid IS NOT NULL")
    elif is_favorited is False:
        where_parts.append("(f.gid IS NULL AND gf.group_id IS NULL)")

    where_sql = " AND ".join(where_parts)

    query = f"""
        SELECT g.*,
               (f.gid IS NOT NULL OR gf.group_id IS NOT NULL) AS is_favorited,
               f.favorited_at,
               ggm.group_id,
               COALESCE(gc.cnt, 0) AS group_count
        FROM eh_galleries g
        LEFT JOIN user_favorites f ON g.gid = f.gid
        LEFT JOIN gallery_group_members ggm ON g.gid = ggm.gid
        LEFT JOIN (
            SELECT group_id, COUNT(*) AS cnt
            FROM gallery_group_members
            GROUP BY group_id
        ) gc ON gc.group_id = ggm.group_id
        LEFT JOIN (
            SELECT DISTINCT ggm2.group_id
            FROM gallery_group_members ggm2
            JOIN user_favorites f2 ON f2.gid = ggm2.gid
        ) gf ON gf.group_id = ggm.group_id
        WHERE {where_sql}
    """

    # Sort
    if sort == "rating":
        query += " ORDER BY g.rating DESC NULLS LAST"
    elif sort == "posted_at":
        query += " ORDER BY g.posted_at DESC NULLS LAST"
    elif sort == "fav_count":
        query += " ORDER BY g.fav_count DESC NULLS LAST"
    elif sort == "gid_asc":
        query += " ORDER BY g.gid ASC"
    else:  # default gid_desc
        query += " ORDER BY g.gid DESC"

    # Pagination
    count_query = f"SELECT COUNT(*) FROM ({query}) AS sub"
    db.execute(count_query, params)
    total = db.fetchone()[0]

    query += " LIMIT %s OFFSET %s"
    params.extend([page_size, offset])

    db.execute(query, params)
    items = _rows_to_galleries(db, db.fetchall())

    return GalleryList(items=items, total=total, page=page, size=page_size, pages=math.ceil(total / page_size) if total else 0)

@router.get("/group/{group_id}", response_model=List[Gallery])
def get_gallery_group(group_id: int, db = Depends(get_db)):
    db.execute(
        """
        SELECT g.*, (f.gid IS NOT NULL) AS is_favorited, f.favorited_at,
               ggm.group_id,
               COUNT(*) OVER (PARTITION BY ggm.group_id) AS group_count
        FROM gallery_group_members ggm
        JOIN eh_galleries g ON g.gid = ggm.gid
        LEFT JOIN user_favorites f ON g.gid = f.gid
        WHERE ggm.group_id = %s
        ORDER BY g.posted_at ASC
        """,
        (group_id,),
    )
    rows = db.fetchall()
    if not rows:
        raise HTTPException(status_code=404, detail="Group not found")
    return _rows_to_galleries(db, rows)


@router.get("/{gid}", response_model=Gallery)
def get_gallery(gid: int, db = Depends(get_db)):
    db.execute(
        """
        SELECT g.*, (f.gid IS NOT NULL) AS is_favorited, f.favorited_at,
               ggm.group_id,
               COALESCE(gc.cnt, 0) AS group_count
        FROM eh_galleries g
        LEFT JOIN user_favorites f ON g.gid = f.gid
        LEFT JOIN gallery_group_members ggm ON g.gid = ggm.gid
        LEFT JOIN (
            SELECT group_id, COUNT(*) AS cnt
            FROM gallery_group_members
            GROUP BY group_id
        ) gc ON gc.group_id = ggm.group_id
        WHERE g.gid = %s
        """,
        (gid,),
    )
    row = db.fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="Gallery not found")

    col_names = [desc[0] for desc in db.description]
    item = dict(zip(col_names, row))
    return Gallery(**item)
